// Package reminders implements Hireloop's durable reminder core: owner-scoped
// PostgreSQL scheduling of reminder jobs for confirmed instant deadlines, plus
// a worker that claims due jobs with leases and dispatches them through a
// Dispatcher adapter.
//
// Delivery-state machine (explicit; only set by evidence). 'leased' is the
// transient claim a worker takes on a due job; outcomes must be strictly
// stronger than the current state (stateRank), so terminal states cannot be
// overwritten by weaker transitions and the inherent suppress/send race
// resolves in favor of recorded evidence: a suppress that lands after the
// worker started a dispatch cannot recall the email, but nothing weaker may
// then overwrite the recorded outcome.
//
//	scheduled   due_at reached; waiting for a worker to pick it up
//	leased      a worker holds the lease (lease_token, lease_until) and is
//	            dispatching
//	accepted    the adapter accepted the send request; delivery unconfirmed
//	delivered   the adapter confirmed delivery
//	uncertain   ambiguous timeout: the send may or may not have happened
//	failed      retryable failure; retry_at carries the backoff schedule
//	exhausted   retry limit reached; terminal until the task is rescheduled
//	suppressed  stale or disabled: the reminder must not fire
//	permanent_failed
//	            a permanent provider error (for example Resend rejecting the
//	            recipient); terminal until the task is rescheduled
//
// Identity and retries: the job id is the notification identity the provider
// sees; retries keep the same job row, so every dispatch uses the same
// notification id.
//
// Versioning and provider correlation (migration 009): delivery_version
// bumps on every Schedule reactivation; provider_email_id and
// provider_version map the provider's email id to that version and are
// cleared on reactivation and suppression, so old callbacks cannot
// overwrite rescheduled jobs. The worker's idempotency key is
// "job:<id>:v<version>" and the payload is byte-stable per version, so
// Resend's 24-hour idempotency key retention returns the original result
// for same-version retries while a rescheduled job gets a fresh key and a
// fresh provider email id.
//
// Event dedup and recovery (migration 010): reminder_email_events persists
// every verified provider event by its svix-id (the provider's replay
// identity); the unique provider_event_id is the persistent dedup, robust
// across reschedules. The ledger row and the job transition commit in one
// transaction, so a crash cannot lose an event by making its retry look like
// a replay. An event that beats the provider-email write is kept pending and
// reconciled when that write lands; a late event for a suppressed/rescheduled
// job is recorded as evidence through reminder_provider_emails and never
// revives the job.
//
// Retry and duplicate policy (documented, not assumed): a failed or uncertain
// dispatch is retried with exponential backoff while the failure count is at
// most MaxRetries; the failure that follows the retry limit moves the job to
// exhausted. Uncertain jobs may produce duplicate sends within the retry
// window because delivery was not confirmed. No delivery is implied:
// 'delivered' is set only by an adapter that confirms delivery.
package reminders

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"hireloop/api/internal/resend"
)

// Delivery states.
const (
	StateScheduled  = "scheduled"
	StateLeased     = "leased"
	StateAccepted   = "accepted"
	StateDelivered  = "delivered"
	StateUncertain  = "uncertain"
	StateFailed     = "failed"
	StateExhausted  = "exhausted"
	StateSuppressed = "suppressed"
	// StatePermanentFailed is terminal: a permanent provider error (for
	// example Resend rejecting the recipient) that retrying cannot fix.
	StatePermanentFailed = "permanent_failed"
)

// stateRank orders the states by the strength of the outcome they record.
// Transitions are monotonic when the target rank is strictly greater than
// the current rank: a recorded outcome never weakens.
func stateRank(state string) int {
	switch state {
	case StateScheduled:
		return 0
	case StateLeased:
		return 1
	case StateAccepted:
		return 2
	case StateFailed:
		return 3
	case StateUncertain:
		return 4
	case StateDelivered:
		return 5
	case StateExhausted:
		return 6
	case StateSuppressed:
		return 7
	case StatePermanentFailed:
		return 8
	}
	return -1
}

func isTerminal(state string) bool {
	switch state {
	case StateDelivered, StateExhausted, StateSuppressed, StatePermanentFailed:
		return true
	}
	return false
}

// transitionAllowed reports whether applying target after current preserves
// monotonic delivery evidence. 'leased' is the transient claim a worker
// takes on a due job, so it may follow any non-terminal state; every other
// transition must be strictly stronger than the current state.
func transitionAllowed(current, target string) bool {
	if target == StateLeased {
		return current != StateDelivered && !isTerminal(current)
	}
	return stateRank(target) > stateRank(current)
}

// stateRankSQL mirrors stateRank for guarded SQL updates. permanent_failed is
// included: leaving it out would make its CASE value -1 and allow an
// equal-rank transition to overwrite a terminal permanent failure.
const stateRankSQL = `CASE delivery_state
	WHEN 'scheduled' THEN 0 WHEN 'leased' THEN 1 WHEN 'accepted' THEN 2
	WHEN 'failed' THEN 3 WHEN 'uncertain' THEN 4 WHEN 'delivered' THEN 5
	WHEN 'exhausted' THEN 6 WHEN 'suppressed' THEN 7
	WHEN 'permanent_failed' THEN 8 ELSE -1 END`

// providerRetryBackoff is the bounded delay used when a provider event
// schedules a retry. It uses the documented worker defaults (30s, doubling,
// capped at 5m) so a webhook can never shorten the retry window.
func providerRetryBackoff(attempt int) time.Duration {
	d := 30 * time.Second
	for shift := 1; shift < attempt && d < 5*time.Minute; shift++ {
		d *= 2
	}
	if d > 5*time.Minute {
		d = 5 * time.Minute
	}
	return d
}

// ReminderTiming identifies when an advance reminder fires relative to
// the true deadline.
type ReminderTiming string

const (
	Timing24hBefore ReminderTiming = "24h_before"
	Timing2hBefore  ReminderTiming = "2h_before"
	// TimingAtDeadline is the legacy single-job behavior: the reminder
// fires at the exact deadline. Kept for forward-compatible migration
// of existing jobs; new scheduling never creates this timing.
TimingAtDeadline ReminderTiming = "at_deadline"
)

// Suppression reasons recorded on a job when it must not fire.
const (
	SuppressCompleted      = "completed"
	SuppressCancelled      = "cancelled"
	SuppressDeleted        = "deleted"
	SuppressRescheduled    = "rescheduled"
	SuppressDisabled       = "disabled"
	SuppressDeadlinePassed = "deadline_passed"
)

var ErrNotFound = errors.New("reminder job not found")

// Executor is the write surface of the store: the transaction a caller's own
// write lives in (the tasks and applications modules pass their tx so job
// changes commit with the task change), or the store's *sql.DB in tests.
type Executor interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// Job is the stored entity. The job id is the stable notification identity
// the provider sees on every dispatch.
type Job struct {
	ID             string
	UserID         string
	TaskID         string
	// DueAt is the reminder time (when this advance reminder should fire).
	// For 24h_before: true_deadline - 24h. For 2h_before: true_deadline - 2h.
	// For legacy at_deadline: equals true_deadline.
	DueAt          time.Time
	// TrueDeadline is the task's actual instant deadline. Used for the
	// dispatch guard (never send past deadline) and rendered in the email.
	TrueDeadline   time.Time
	// ReminderTiming identifies which advance reminder this job is.
	ReminderTiming ReminderTiming
	DeliveryState  string
	SuppressReason string
	LeaseToken     string
	LeaseUntil     time.Time
	RetryCount     int
	RetryAt        *time.Time
	CallbackKey    string
	DeliveryNote   string
	// DeliveryVersion is bumped by Schedule on every reactivation. Together
	// with ProviderEmailID it identifies one logical send.
	DeliveryVersion int
	// ProviderEmailID is the Resend email id the worker received for the
	// current version; ProviderVersion is the delivery version it belongs
	// to. Both are cleared on reactivation and suppression.
	ProviderEmailID string
	ProviderVersion int
	UpdatedAt       time.Time
}

// Transition is the atomic write applied to a job: target state, suppression
// reason, retry bookkeeping and the adapter note.
type Transition struct {
	State      string
	Reason     string
	Note       string
	RetryCount int
	RetryAt    *time.Time
	// ScheduleRetry asks the store to compute a bounded backoff from the
	// job's existing retry count instead of replacing retry bookkeeping with
	// zero/nil. Provider 'email.failed' events use it so a webhook cannot
	// reset retries and put the worker into a hot dispatch loop.
	ScheduleRetry bool
}

// Store reads and writes reminder_jobs and user_notification_settings.
type Store struct{ DB *sql.DB }

// Schedule activates or reactivates the two advance reminder jobs for a
// task: one at 24h before the true deadline, one at 2h before. Date-only
// deadlines never get a job (no time is invented). Only future-eligible
// slots are scheduled: if the true deadline is less than 24h away, only the
// 2h reminder is scheduled (if still in the future); if less than 2h away,
// no advance reminders are scheduled. Every reactivation bumps
// delivery_version and clears the provider mapping and callback bookkeeping,
// so the next dispatch uses a fresh per-version idempotency key and old
// provider events for previous versions cannot overwrite the reactivated job.
func (s *Store) Schedule(ctx context.Context, w Executor, userID, taskID string, trueDeadline time.Time) error {
	now := time.Now()
	// Suppress any existing jobs for this task first (handles reschedule,
	// completion, etc.). The unique index on (task_id, reminder_timing)
	// allows us to upsert each timing independently.
	_, err := w.ExecContext(ctx, `UPDATE reminder_jobs
		SET delivery_state = '`+StateSuppressed+`', suppress_reason = '`+SuppressRescheduled+`',
			lease_token = NULL, lease_until = NULL, retry_at = NULL,
			provider_email_id = NULL, provider_version = NULL, updated_at = now()
		WHERE user_id = $1 AND task_id = $2 AND delivery_state IN ('scheduled','leased','failed','uncertain')`,
		userID, taskID)
	if err != nil {
		return err
	}

	// Schedule 24h before reminder if eligible.
	if d24 := trueDeadline.Add(-24 * time.Hour); d24.After(now) {
		if err := s.scheduleOne(ctx, w, userID, taskID, trueDeadline, d24, Timing24hBefore); err != nil {
			return err
		}
	}
	// Schedule 2h before reminder if eligible.
	if d2 := trueDeadline.Add(-2 * time.Hour); d2.After(now) {
		if err := s.scheduleOne(ctx, w, userID, taskID, trueDeadline, d2, Timing2hBefore); err != nil {
			return err
		}
	}
	return nil
}

// scheduleOne inserts or reactivates a single advance reminder job.
// It reactivates any existing job for this task/timing/owner when Schedule
// is called, because Schedule is only invoked when the deadline genuinely
// changed (or on initial create). The delivery_version bump and provider
// mapping clear ensure a fresh idempotency key. Terminal states are not
// excluded: a delivered/exhausted/permanent_failed job from an old deadline
// becomes a new logical send for the new deadline. Suppressed jobs from the
// initial suppression in Schedule (reason='rescheduled') are also reactivated.
func (s *Store) scheduleOne(ctx context.Context, w Executor, userID, taskID string, trueDeadline, dueAt time.Time, timing ReminderTiming) error {
	res, err := w.ExecContext(ctx, `INSERT INTO reminder_jobs (user_id, task_id, due_at, true_deadline, reminder_timing, delivery_version)
		VALUES ($1, $2, $3, $4, $5, 1)
		ON CONFLICT (task_id, reminder_timing) DO UPDATE SET
			delivery_state = '`+StateScheduled+`',
			due_at = $3,
			true_deadline = $4,
			delivery_version = reminder_jobs.delivery_version + 1,
			lease_token = NULL, lease_until = NULL,
			retry_count = 0, retry_at = NULL, callback_key = NULL,
			delivery_note = NULL, suppress_reason = NULL,
			provider_email_id = NULL, provider_version = NULL,
			updated_at = now()
		WHERE reminder_jobs.user_id = $6`, userID, taskID, dueAt, trueDeadline, timing, userID)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return errors.New("reminders: task id belongs to another owner")
	}
	return nil
}

// Suppress marks a job for a task as suppressed with the given reason, but
// only while it is still an unsent state: a delivered or exhausted job has
// already had its effect and is left alone.
func (s *Store) Suppress(ctx context.Context, w Executor, userID, taskID, reason string) error {
	_, err := w.ExecContext(ctx, `UPDATE reminder_jobs
		SET delivery_state = '`+StateSuppressed+`', suppress_reason = $3,
			lease_token = NULL, lease_until = NULL, retry_at = NULL,
			provider_email_id = NULL, provider_version = NULL, updated_at = now()
		WHERE user_id = $1 AND task_id = $2 AND delivery_state IN ('scheduled','leased','failed','uncertain')`,
		userID, taskID, reason)
	return err
}

// SuppressByApplication marks the jobs of every open task in the application
// as suppressed with the given reason (the applications module uses it for
// application deletion).
func (s *Store) SuppressByApplication(ctx context.Context, w Executor, userID, appID, reason string) error {
	_, err := w.ExecContext(ctx, `UPDATE reminder_jobs
		SET delivery_state = '`+StateSuppressed+`', suppress_reason = $3,
			lease_token = NULL, lease_until = NULL, retry_at = NULL,
			provider_email_id = NULL, provider_version = NULL, updated_at = now()
		WHERE user_id = $1 AND delivery_state IN ('scheduled','leased','failed','uncertain')
			AND task_id IN (SELECT id FROM tasks WHERE application_id = $2 AND user_id = $1)`,
		userID, appID, reason)
	return err
}

// Get reads one job, scoped to the owner: another owner sees not-found.
func (s *Store) Get(ctx context.Context, userID, id string) (Job, error) {
	var job Job
	var suppress, leaseToken, callbackKey, note, providerEmailID sql.NullString
	var leaseUntil, retryAt sql.NullTime
	var providerVersion sql.NullInt64
	var reminderTiming, trueDeadline sql.NullString
	err := s.DB.QueryRowContext(ctx, `SELECT id, user_id, task_id, due_at, true_deadline, reminder_timing, delivery_state,
			suppress_reason, lease_token, lease_until, retry_count, retry_at, callback_key,
			delivery_note, delivery_version, provider_email_id, provider_version, updated_at
		FROM reminder_jobs WHERE user_id = $1 AND id = $2`, userID, id).
		Scan(&job.ID, &job.UserID, &job.TaskID, &job.DueAt, &trueDeadline, &reminderTiming, &job.DeliveryState,
			&suppress, &leaseToken, &leaseUntil, &job.RetryCount, &retryAt,
			&callbackKey, &note, &job.DeliveryVersion, &providerEmailID,
			&providerVersion, &job.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, ErrNotFound
	}
	if err != nil {
		return Job{}, err
	}
	job.SuppressReason = suppress.String
	job.LeaseToken = leaseToken.String
	job.CallbackKey = callbackKey.String
	job.DeliveryNote = note.String
	job.ProviderEmailID = providerEmailID.String
	job.ProviderVersion = int(providerVersion.Int64)
	if leaseUntil.Valid {
		job.LeaseUntil = leaseUntil.Time
	}
	if retryAt.Valid {
		at := retryAt.Time
		job.RetryAt = &at
	}
	if trueDeadline.Valid {
		if parsed, err := time.Parse(time.RFC3339Nano, trueDeadline.String); err == nil {
			job.TrueDeadline = parsed
		}
	}
	job.ReminderTiming = ReminderTiming(reminderTiming.String)
	return job, nil
}

// ClaimDue atomically leases due jobs and returns them: state 'leased' with
// the caller's lease token. Jobs are claimable while due_at is at or past
// now, not retry-pending, and either unscoped or within the given scope;
// expired-lease jobs are re-claimed so a crashed worker's jobs are recovered
// by another worker with the same notification identity.
func (s *Store) ClaimDue(ctx context.Context, now time.Time, limit int, token string, leaseUntil time.Time, scope []string) ([]Job, error) {
	query := `UPDATE reminder_jobs
		SET delivery_state = '` + StateLeased + `', lease_token = $2, lease_until = $3, updated_at = now()
		WHERE id IN (
			SELECT id FROM reminder_jobs
			WHERE delivery_state IN ('scheduled', 'leased', 'failed', 'uncertain')
				AND due_at <= $1
				AND (retry_at IS NULL OR retry_at <= $1)
				AND (delivery_state <> 'leased' OR lease_until IS NULL OR lease_until <= $1)`
	args := []any{now, token, leaseUntil}
	if len(scope) > 0 {
		args = append(args, scope)
		query += fmt.Sprintf(` AND user_id = ANY($%d::text[])`, len(args))
	}
	args = append(args, limit)
	query += fmt.Sprintf(`
				ORDER BY due_at ASC, id ASC LIMIT $%d)
		RETURNING id, user_id, task_id, due_at, lease_token, lease_until`, len(args))
	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	jobs := []Job{}
	for rows.Next() {
		var job Job
		if err := rows.Scan(&job.ID, &job.UserID, &job.TaskID, &job.DueAt, &job.LeaseToken, &job.LeaseUntil); err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return jobs, nil
}

// RecordDelivery applies a dispatch outcome to a job whose lease is still
// ours (delivery_state = 'leased'). When the job was suppressed or re-claimed
// in the meantime, nothing is written.
func (s *Store) RecordDelivery(ctx context.Context, jobID, token string, tr Transition) error {
	var suppress, retryAt any
	if tr.Reason != "" {
		suppress = tr.Reason
	}
	if tr.RetryAt != nil {
		retryAt = *tr.RetryAt
	}
	_, err := s.DB.ExecContext(ctx, `UPDATE reminder_jobs
		SET delivery_state = $2, suppress_reason = $5, retry_count = $3, retry_at = $4,
			delivery_note = $6, lease_token = NULL, lease_until = NULL, updated_at = now()
		WHERE id = $1 AND lease_token = $7 AND delivery_state = '`+StateLeased+`'`,
		jobID, tr.State, tr.RetryCount, retryAt, suppress, tr.Note, token)
	return err
}

// SetProviderEmailID persists the provider email id the worker received from
// the provider for the job's current version. The live mapping is guarded by
// the version and by a still-claimable state, so a stale dispatch cannot
// overwrite a newer one. In
// the same transaction it records a durable email-history entry and reconciles
// webhooks that arrived before this mapping existed. A failure rolls the whole
// write back and is returned to the caller: the worker retries the send under
// the same idempotency key instead of silently losing the mapping or an event.
func (s *Store) SetProviderEmailID(ctx context.Context, jobID string, version int, emailID string) error {
	if strings.TrimSpace(emailID) == "" {
		return errors.New("reminders: provider email id required")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// One statement both guards the live write and returns the owner for the
	// durable history entry. No row means a stale version or state.
	var userID string
	err = tx.QueryRowContext(ctx, `UPDATE reminder_jobs
		SET provider_email_id = $2, provider_version = $3
		WHERE id = $1 AND delivery_version = $4 AND delivery_state IN ('scheduled','leased')
		RETURNING user_id`, jobID, emailID, version, version).Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("reminders: provider mapping rejected: job %s is not claimable at version %d", jobID, version)
	}
	if err != nil {
		return err
	}
	// A provider email id belongs to exactly one send. If an existing row
	// points at another job, the write fails and the caller retries.
	var mappedJobID string
	err = tx.QueryRowContext(ctx, `INSERT INTO reminder_provider_emails
		(provider_email_id, job_id, user_id, delivery_version)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (provider_email_id) DO UPDATE SET job_id = EXCLUDED.job_id
		WHERE reminder_provider_emails.job_id = EXCLUDED.job_id
		RETURNING job_id`, emailID, jobID, userID, version).Scan(&mappedJobID)
	if err != nil {
		return err
	}
	if err := reconcilePendingEvents(ctx, tx, emailID); err != nil {
		return err
	}
	return tx.Commit()
}

// GetDeliveryPayload returns the immutable first-attempt payload for a
// (job, version), if one was persisted.
func (s *Store) GetDeliveryPayload(ctx context.Context, jobID string, version int) (Payload, bool, error) {
	p, _, ok, err := s.GetDeliveryPayloadWithTimestamp(ctx, jobID, version)
	return p, ok, err
}

// GetDeliveryPayloadWithTimestamp returns the payload and its creation
// timestamp for a (job, version), if one was persisted. The timestamp is
// used to detect when the first attempt was made, for idempotency key
// expiry checks (Resend retains keys for 24h).
func (s *Store) GetDeliveryPayloadWithTimestamp(ctx context.Context, jobID string, version int) (Payload, time.Time, bool, error) {
	var p Payload
	var createdAt time.Time
	err := s.DB.QueryRowContext(ctx,
		`SELECT recipient, subject, text_body, created_at FROM reminder_delivery_payloads
		 WHERE job_id = $1 AND delivery_version = $2`, jobID, version).
		Scan(&p.Recipient, &p.Subject, &p.Text, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Payload{}, time.Time{}, false, nil
	}
	if err != nil {
		return Payload{}, time.Time{}, false, err
	}
	return p, createdAt, true, nil
}

// SaveDeliveryPayload persists p as the first-attempt payload of a
// (job, version). Concurrent attempts race on the primary key: the first
// insert wins and every caller receives the winning row, so retries can never
// send a different body under the same idempotency key.
func (s *Store) SaveDeliveryPayload(ctx context.Context, jobID string, version int, p Payload) (Payload, error) {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO reminder_delivery_payloads
		(job_id, delivery_version, recipient, subject, text_body)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (job_id, delivery_version) DO NOTHING`,
		jobID, version, p.Recipient, p.Subject, p.Text)
	if err != nil {
		return Payload{}, err
	}
	winner, ok, err := s.GetDeliveryPayload(ctx, jobID, version)
	if err != nil {
		return Payload{}, err
	}
	if !ok {
		return Payload{}, errors.New("reminders: delivery payload disappeared after insert")
	}
	return winner, nil
}

// GetByProviderEmail returns the job whose current provider email id matches
// emailID. Provider callbacks and the webhook route use it to correlate
// events to the job that produced the email.
func (s *Store) GetByProviderEmail(ctx context.Context, emailID string) (Job, error) {
	if strings.TrimSpace(emailID) == "" {
		return Job{}, ErrNotFound
	}
	var job Job
	err := s.DB.QueryRowContext(ctx,
		`SELECT id, user_id FROM reminder_jobs WHERE provider_email_id = $1`, emailID).
		Scan(&job.ID, &job.UserID)
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, ErrNotFound
	}
	return job, err
}

// RecordCallback applies an asynchronous provider callback to a job and
// reports whether it was applied. The callback must carry the job's current
// provider email id: when the job was reactivated or suppressed after the
// event arrived, the email id is gone or replaced, so old callbacks are
// ignored and cannot overwrite the rescheduled job. The target state must be
// strictly stronger than the job's current state (monotonic delivery
// evidence); repeated or reordered callbacks with equal or weaker targets
// are ignored.
func (s *Store) RecordCallback(ctx context.Context, jobID, eventEmailID string, tr Transition) (bool, error) {
	return applyCallback(ctx, s.DB, jobID, eventEmailID, tr)
}

// callbackState is the read half of the guarded callback write.
type callbackState struct {
	state           string
	retryCount      int
	providerEmailID sql.NullString
}

// applyCallback applies tr only when the event's email id is still the job's
// current mapping and the target strictly strengthens the recorded evidence.
// ScheduleRetry transitions derive a bounded backoff from the job's existing
// retry count; they never reset it to zero or clear retry_at into a hot loop.
func applyCallback(ctx context.Context, q Executor, jobID, eventEmailID string, tr Transition) (bool, error) {
	var current callbackState
	err := q.QueryRowContext(ctx,
		`SELECT delivery_state, retry_count, provider_email_id FROM reminder_jobs WHERE id = $1`, jobID).
		Scan(&current.state, &current.retryCount, &current.providerEmailID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return applyCallbackWithState(ctx, q, jobID, eventEmailID, tr, current)
}

// applyCallbackWithState is the write half of applyCallback; the webhook path
// passes the state it already read while correlating the event, avoiding a
// second round trip inside the transaction.
func applyCallbackWithState(ctx context.Context, q Executor, jobID, eventEmailID string, tr Transition, current callbackState) (bool, error) {
	target := stateRank(tr.State)
	if target < 0 || strings.TrimSpace(tr.State) == "" {
		return false, errors.New("callback transition state required")
	}
	if current.providerEmailID.String != eventEmailID {
		return false, nil
	}
	if !transitionAllowed(current.state, tr.State) {
		return false, nil
	}
	retryCount := tr.RetryCount
	var retryAt any
	if tr.ScheduleRetry {
		retryCount = current.retryCount + 1
		at := time.Now().Add(providerRetryBackoff(retryCount))
		retryAt = at
	} else if tr.RetryAt != nil {
		retryAt = *tr.RetryAt
	}
	var reason any
	if tr.Reason != "" {
		reason = tr.Reason
	}
	res, err := q.ExecContext(ctx, `UPDATE reminder_jobs
		SET delivery_state = $2, delivery_note = $3, retry_count = $4, retry_at = $5,
			suppress_reason = $6, callback_key = $7, lease_token = NULL, lease_until = NULL,
			updated_at = now()
		WHERE id = $1 AND COALESCE(provider_email_id, '') = $8
			AND `+stateRankSQL+` < $9`,
		jobID, tr.State, tr.Note, retryCount, retryAt, reason, eventEmailID, eventEmailID, target)
	if err != nil {
		return false, err
	}
	rows, err := res.RowsAffected()
	return rows > 0, err
}

// RecordWebhookEvent persists a verified provider event and applies its
// transition to the correlated job in ONE transaction; it reports whether the
// transition was applied. The svix-id is the provider's replay identity: the
// ledger insert is the persistent dedup, and an exact replay is a no-op.
//
// The insert and the job update commit together, so a crash or a failed update
// rolls the ledger insert back and a retry can reprocess the event instead of
// losing it. An event that arrives before the provider mapping is recorded
// stays pending in the ledger and is reconciled by SetProviderEmailID; an
// event for a suppressed or rescheduled job is recorded as evidence through
// the durable email history and never revives that job.
func (s *Store) RecordWebhookEvent(ctx context.Context, eventID, eventEmailID, eventType string, tr Transition) (bool, error) {
	if strings.TrimSpace(eventID) == "" || strings.TrimSpace(eventEmailID) == "" || strings.TrimSpace(tr.State) == "" {
		return false, errors.New("webhook event requires an id, an email id and a transition state")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	applied, err := processWebhookEvent(ctx, tx, eventID, eventEmailID, eventType, tr)
	if err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return applied, nil
}

// eventContext is the correlation result for an email id: the job and version
// that sent it, whether that mapping is still live, and the state needed to
// apply a callback without a second read.
type eventContext struct {
	jobID   string
	version int
	live    bool
	state   callbackState
}

// resolveEvent finds the job an email id belongs to. The live mapping is
// preferred; when it was cleared by suppression or rescheduling, the durable
// history still identifies the job and version that actually sent the email,
// so late evidence can be attributed without reviving the job.
func resolveEvent(ctx context.Context, q Executor, emailID string) (eventContext, error) {
	var ec eventContext
	var version sql.NullInt64
	err := q.QueryRowContext(ctx,
		`SELECT id, COALESCE(provider_version, delivery_version), delivery_state, retry_count, provider_email_id
		 FROM reminder_jobs WHERE provider_email_id = $1`, emailID).
		Scan(&ec.jobID, &version, &ec.state.state, &ec.state.retryCount, &ec.state.providerEmailID)
	if err == nil {
		ec.version = int(version.Int64)
		ec.live = true
		return ec, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return eventContext{}, err
	}
	var historyJobID string
	var historyVersion int
	err = q.QueryRowContext(ctx,
		`SELECT job_id, delivery_version FROM reminder_provider_emails WHERE provider_email_id = $1`, emailID).
		Scan(&historyJobID, &historyVersion)
	if err == nil {
		return eventContext{jobID: historyJobID, version: historyVersion}, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return eventContext{}, nil
	}
	return eventContext{}, err
}

// processWebhookEvent inserts the durable dedup row and resolves it in one
// transaction. A replay that was already resolved is a no-op; a replay that is
// still pending is retried, because the mapping may have appeared since it was
// recorded. Any error leaves the insert uncommitted for a future retry.
func processWebhookEvent(ctx context.Context, tx *sql.Tx, eventID, emailID, eventType string, tr Transition) (bool, error) {
	ec, err := resolveEvent(ctx, tx, emailID)
	if err != nil {
		return false, err
	}
	var jobValue, versionValue any
	if ec.jobID != "" {
		jobValue = ec.jobID
	}
	if ec.version > 0 {
		versionValue = ec.version
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO reminder_email_events
		(provider_event_id, provider_email_id, job_id, event_type, target_state, delivery_version, resolution, received_at)
		VALUES ($1, $2, $3, $4, $5, $6, 'pending', now())
		ON CONFLICT (provider_event_id) DO NOTHING`,
		eventID, emailID, jobValue, eventType, tr.State, versionValue)
	if err != nil {
		return false, err
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if rows == 0 {
		var resolution string
		if err := tx.QueryRowContext(ctx,
			`SELECT resolution FROM reminder_email_events WHERE provider_event_id = $1`, eventID).Scan(&resolution); err != nil {
			return false, err
		}
		if resolution != "pending" {
			return false, nil
		}
	}
	return resolveWebhookEvent(ctx, tx, eventID, emailID, tr, ec)
}

// resolveWebhookEvent applies an event when the live mapping still points at
// its job and the transition strengthens the recorded evidence. A history-only
// mapping records honest evidence with its original job/version but never
// reactivates a suppressed or rescheduled job. With no mapping at all the
// event remains pending for reconciliation.
func resolveWebhookEvent(ctx context.Context, q Executor, eventID, emailID string, tr Transition, ec eventContext) (bool, error) {
	if ec.jobID == "" {
		return false, nil
	}
	applied := false
	if ec.live {
		var err error
		applied, err = applyCallbackWithState(ctx, q, ec.jobID, emailID, tr, ec.state)
		if err != nil {
			return false, err
		}
	}
	resolution := "ignored"
	if applied {
		resolution = "applied"
	}
	_, err := q.ExecContext(ctx, `UPDATE reminder_email_events
		SET job_id = $2, delivery_version = $3, resolution = $4, resolved_at = now()
		WHERE provider_event_id = $1 AND resolution = 'pending'`,
		eventID, ec.jobID, ec.version, resolution)
	if err != nil {
		return false, err
	}
	return applied, nil
}

// reconcilePendingEvents applies webhooks that arrived before SetProviderEmailID
// persisted the mapping. It runs inside the mapping transaction, so a failure
// rolls back both writes and the next send attempt retries the whole batch.
func reconcilePendingEvents(ctx context.Context, tx *sql.Tx, emailID string) error {
	rows, err := tx.QueryContext(ctx, `SELECT provider_event_id, target_state
		FROM reminder_email_events
		WHERE provider_email_id = $1 AND resolution = 'pending'`, emailID)
	if err != nil {
		return err
	}
	type pending struct{ id, state string }
	items := []pending{}
	for rows.Next() {
		var p pending
		var state sql.NullString
		if err := rows.Scan(&p.id, &state); err != nil {
			_ = rows.Close()
			return err
		}
		if !state.Valid {
			// A ledger row written before recovery metadata existed has no
			// transition to replay; it is historical evidence only.
			continue
		}
		p.state = state.String
		items = append(items, p)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	_ = rows.Close()
	for _, p := range items {
		tr := Transition{State: p.state, Note: "reconciled from a webhook that arrived before the provider mapping"}
		if p.state == StateFailed {
			tr.ScheduleRetry = true
		}
		ec, err := resolveEvent(ctx, tx, emailID)
		if err != nil {
			return err
		}
		if ec.jobID == "" {
			continue
		}
		if _, err := resolveWebhookEvent(ctx, tx, p.id, emailID, tr, ec); err != nil {
			return err
		}
	}
	return nil
}

// TransitionForEvent maps a verified Resend event type to the transition the
// worker records for the job. ok is false for events that do not change
// delivery state (scheduled, opened, clicked, received).
func TransitionForEvent(eventType resend.EventType) (Transition, bool) {
	switch eventType {
	case resend.EventSent:
		return Transition{State: StateAccepted, Note: "provider accepted the send request"}, true
	case resend.EventDelivered:
		return Transition{State: StateDelivered, Note: "provider confirmed delivery"}, true
	case resend.EventDeliverySlow:
		return Transition{State: StateUncertain, Note: "provider reported a delayed delivery"}, true
	case resend.EventFailed:
		return Transition{State: StateFailed, Note: "provider reported a send failure", ScheduleRetry: true}, true
	case resend.EventBounced:
		return Transition{State: StatePermanentFailed, Note: "provider reported a permanent failure: recipient bounce"}, true
	case resend.EventComplained:
		return Transition{State: StateDelivered, Note: "provider confirmed delivery; recipient complained"}, true
	case resend.EventSuppressed:
		return Transition{State: StateSuppressed, Note: "provider suppressed the send"}, true
	case resend.EventScheduled, resend.EventOpened, resend.EventClicked, resend.EventReceived:
		return Transition{}, false
	}
	return Transition{}, false
}

// RemindersEnabled reports the user's notification preference; a missing
// settings row means the default: enabled.
func (s *Store) RemindersEnabled(ctx context.Context, userID string) (bool, error) {
	var enabled bool
	err := s.DB.QueryRowContext(ctx, `SELECT COALESCE((SELECT reminders_enabled FROM user_notification_settings WHERE user_id = $1), true)`, userID).Scan(&enabled)
	return enabled, err
}

// SetRemindersEnabled upserts the user's notification preference.
func (s *Store) SetRemindersEnabled(ctx context.Context, userID string, enabled bool) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO user_notification_settings (user_id, reminders_enabled, updated_at)
		VALUES ($1, $2, now())
		ON CONFLICT (user_id) DO UPDATE SET reminders_enabled = $2, updated_at = now()`, userID, enabled)
	return err
}
