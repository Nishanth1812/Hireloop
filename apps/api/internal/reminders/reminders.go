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
// Event dedup: reminder_email_events persists every verified provider event
// by its svix-id (the provider's replay identity); the unique
// provider_event_id is the persistent dedup, robust across reschedules.
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

// Suppression reasons recorded on a job when it must not fire.
const (
	SuppressCompleted   = "completed"
	SuppressCancelled   = "cancelled"
	SuppressDeleted     = "deleted"
	SuppressRescheduled = "rescheduled"
	SuppressDisabled    = "disabled"
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
	DueAt          time.Time
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
}

// Store reads and writes reminder_jobs and user_notification_settings.
type Store struct{ DB *sql.DB }

// Schedule activates or reactivates the job for a task: one job per task,
// reactivated with the new instant deadline when the task is rescheduled, or
// created when the task gains an instant deadline. Date-only deadlines never
// get a job (no time is invented). Every reactivation bumps delivery_version
// and clears the provider mapping and callback bookkeeping, so the next
// dispatch uses a fresh per-version idempotency key and old provider events
// for previous versions cannot overwrite the reactivated job.
func (s *Store) Schedule(ctx context.Context, w Executor, userID, taskID string, dueAt time.Time) error {
	_, err := w.ExecContext(ctx, `INSERT INTO reminder_jobs (user_id, task_id, due_at, delivery_version)
		VALUES ($1, $2, $3, 1)
		ON CONFLICT (task_id) DO UPDATE SET
			delivery_state = '`+StateScheduled+`',
			due_at = $3,
			delivery_version = reminder_jobs.delivery_version + 1,
			lease_token = NULL, lease_until = NULL,
			retry_count = 0, retry_at = NULL, callback_key = NULL,
			delivery_note = NULL, suppress_reason = NULL,
			provider_email_id = NULL, provider_version = NULL,
			updated_at = now()`, userID, taskID, dueAt)
	return err
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
	err := s.DB.QueryRowContext(ctx, `SELECT id, user_id, task_id, due_at, delivery_state,
			suppress_reason, lease_token, lease_until, retry_count, retry_at, callback_key,
			delivery_note, delivery_version, provider_email_id, provider_version, updated_at
		FROM reminder_jobs WHERE user_id = $1 AND id = $2`, userID, id).
		Scan(&job.ID, &job.UserID, &job.TaskID, &job.DueAt, &job.DeliveryState,
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
// the provider for the job's current version. The write is guarded by the
// lease and the delivery version, so the acceptance of a stale dispatch can
// never overwrite the mapping of a newer version.
func (s *Store) SetProviderEmailID(ctx context.Context, jobID string, version int, emailID string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE reminder_jobs
		SET provider_email_id = $2, provider_version = $3
		WHERE id = $1 AND delivery_version = $4 AND delivery_state = 'leased'`,
		jobID, emailID, version, version)
	return err
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

// jobByProviderEmail is the read half of RecordWebhookEvent/RecordCallback
// correlation: it reports whether a job currently maps to the email id.
func (s *Store) jobByProviderEmail(ctx context.Context, emailID string) (Job, bool, error) {
	job, err := s.GetByProviderEmail(ctx, emailID)
	if errors.Is(err, ErrNotFound) {
		return Job{}, false, nil
	}
	if err != nil {
		return Job{}, false, err
	}
	return job, true, nil
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
	target := stateRank(tr.State)
	if target < 0 || strings.TrimSpace(tr.State) == "" {
		return false, errors.New("callback transition state required")
	}
	var state string
	var providerEmailID sql.NullString
	err := s.DB.QueryRowContext(ctx,
		`SELECT delivery_state, provider_email_id FROM reminder_jobs WHERE id = $1`, jobID).
		Scan(&state, &providerEmailID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if providerEmailID.String != eventEmailID {
		return false, nil
	}
	if !transitionAllowed(state, tr.State) {
		return false, nil
	}
	var suppress, retryAt any
	if tr.Reason != "" {
		suppress = tr.Reason
	}
	if tr.RetryAt != nil {
		retryAt = *tr.RetryAt
	}
	res, err := s.DB.ExecContext(ctx, `UPDATE reminder_jobs
		SET delivery_state = $2, delivery_note = $3, retry_count = $4, retry_at = $5,
			suppress_reason = $6, callback_key = $7, lease_token = NULL, lease_until = NULL,
			updated_at = now()
		WHERE id = $1 AND provider_email_id IS NOT DISTINCT FROM $8
			AND CASE delivery_state
				WHEN 'scheduled' THEN 0 WHEN 'leased' THEN 1 WHEN 'accepted' THEN 2
				WHEN 'failed' THEN 3 WHEN 'uncertain' THEN 4 WHEN 'delivered' THEN 5
				WHEN 'exhausted' THEN 6 WHEN 'suppressed' THEN 7
				WHEN 'permanent_failed' THEN 8 ELSE -1 END < $9`,
		jobID, tr.State, tr.Note, tr.RetryCount, retryAt, suppress, eventEmailID, eventEmailID, target)
	if err != nil {
		return false, err
	}
	rows, err := res.RowsAffected()
	return rows > 0, err
}

// RecordWebhookEvent persists a verified provider event and applies its
// transition to the correlated job; it reports whether the transition was
// applied. The event id is the provider's replay identity: the ledger
// insert is the persistent dedup, so an exact replay affects no row and is
// ignored. The event correlates by provider email id: when the job was
// reactivated or suppressed after the event was first seen, the email id is
// gone, so old callbacks cannot overwrite the rescheduled job. When applied,
// the transition must be strictly stronger than the job's current state
// (monotonic evidence).
func (s *Store) RecordWebhookEvent(ctx context.Context, eventID, eventEmailID, eventType string, tr Transition) (bool, error) {
	if strings.TrimSpace(eventID) == "" || strings.TrimSpace(eventEmailID) == "" {
		return false, errors.New("webhook event requires an id and email id")
	}
	job, found, err := s.jobByProviderEmail(ctx, eventEmailID)
	if err != nil {
		return false, err
	}
	res, err := s.DB.ExecContext(ctx, `INSERT INTO reminder_email_events
		(provider_event_id, provider_email_id, job_id, event_type, received_at)
		VALUES ($1, $2, (SELECT id FROM reminder_jobs WHERE provider_email_id = $2), $3, now())
		ON CONFLICT (provider_event_id) DO NOTHING`,
		eventID, eventEmailID, eventType)
	if err != nil {
		return false, err
	}
	rows, err := res.RowsAffected()
	if err != nil || rows == 0 {
		// The provider event id was already seen: a replay; ignore it.
		return false, err
	}
	if !found {
		// First-seen event, but no job maps to this email id (the job was
		// never sent to, suppressed earlier, or the send raced the
		// provider email id persist).
		return false, nil
	}
	return s.RecordCallback(ctx, job.ID, eventEmailID, tr)
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
		return Transition{State: StateFailed, Note: "provider reported a send failure"}, true
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
