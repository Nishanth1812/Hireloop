// Package reminders implements Hireloop's durable reminder core: owner-scoped
// Postgres scheduling of reminder jobs for confirmed instant deadlines, plus
// a worker that claims due jobs with leases and dispatches them through a
// Dispatcher adapter.
//
// Delivery-state machine (explicit; only set by evidence):
//
//	scheduled   due_at reached; waiting for a worker
//	leased      a worker holds the lease (lease_token, lease_until)
//	accepted    the adapter accepted the send request; delivery unconfirmed
//	delivered   the adapter confirmed delivery
//	uncertain   ambiguous timeout: the send may or may not have happened
//	failed      retryable failure; retry_at carries the backoff schedule
//	exhausted   retry limit reached; terminal until the task is rescheduled
//	suppressed  stale or disabled: the reminder must not fire
//
// Identity and retries: the job id is the notification identity the provider
// sees; retries keep the same job row, so every dispatch uses the same
// notification id and provider callbacks can be deduplicated by callback_key.
//
// Retry and duplicate policy (documented, not assumed): a failed or uncertain
// dispatch is retried with exponential backoff while the failure count is at
// most MaxRetries; the failure that follows the retry limit moves the job to
// exhausted. Uncertain jobs may produce duplicate sends within the retry
// window because delivery was not confirmed. No delivery is implied:
// 'delivered' is set only by an adapter that confirms delivery, and the null
// dispatcher reports failure so jobs land in 'exhausted' instead of
// pretending to be delivered.
package reminders

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
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
)

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
	UpdatedAt      time.Time
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
// get a job (no time is invented).
func (s *Store) Schedule(ctx context.Context, w Executor, userID, taskID string, dueAt time.Time) error {
	_, err := w.ExecContext(ctx, `INSERT INTO reminder_jobs (user_id, task_id, due_at) VALUES ($1, $2, $3)
		ON CONFLICT (task_id) DO UPDATE SET
			delivery_state = '`+StateScheduled+`',
			due_at = $3,
			lease_token = NULL, lease_until = NULL,
			retry_count = 0, retry_at = NULL, callback_key = NULL,
			delivery_note = NULL, suppress_reason = NULL,
			updated_at = now()`, userID, taskID, dueAt)
	return err
}

// Suppress marks a job for a task as suppressed with the given reason, but
// only while it is still an unsent state: a delivered or exhausted job has
// already had its effect and is left alone.
func (s *Store) Suppress(ctx context.Context, w Executor, userID, taskID, reason string) error {
	_, err := w.ExecContext(ctx, `UPDATE reminder_jobs
		SET delivery_state = '`+StateSuppressed+`', suppress_reason = $3,
			lease_token = NULL, lease_until = NULL, retry_at = NULL, updated_at = now()
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
			lease_token = NULL, lease_until = NULL, retry_at = NULL, updated_at = now()
		WHERE user_id = $1 AND delivery_state IN ('scheduled','leased','failed','uncertain')
			AND task_id IN (SELECT id FROM tasks WHERE application_id = $2 AND user_id = $1)`,
		userID, appID, reason)
	return err
}

// Get reads one job, scoped to the owner: another owner sees not-found.
func (s *Store) Get(ctx context.Context, userID, id string) (Job, error) {
	var job Job
	var suppress, leaseToken, callbackKey, note sql.NullString
	var leaseUntil, retryAt sql.NullTime
	err := s.DB.QueryRowContext(ctx, `SELECT id, user_id, task_id, due_at, delivery_state,
			suppress_reason, lease_token, lease_until, retry_count, retry_at, callback_key,
			delivery_note, updated_at
		FROM reminder_jobs WHERE user_id = $1 AND id = $2`, userID, id).
		Scan(&job.ID, &job.UserID, &job.TaskID, &job.DueAt, &job.DeliveryState,
			&suppress, &leaseToken, &leaseUntil, &job.RetryCount, &retryAt,
			&callbackKey, &note, &job.UpdatedAt)
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

// RecordCallback applies an asynchronous provider callback to a job and
// reports whether the callback was new (applied). Repeated callbacks with the
// same callback_key are deduplicated and ignored, as are callbacks for
// terminal jobs (delivered, exhausted, suppressed).
func (s *Store) RecordCallback(ctx context.Context, jobID, callbackKey string, tr Transition) (bool, error) {
	if strings.TrimSpace(callbackKey) == "" {
		return false, errors.New("callback key required")
	}
	var suppress, retryAt any
	if tr.Reason != "" {
		suppress = tr.Reason
	}
	if tr.RetryAt != nil {
		retryAt = *tr.RetryAt
	}
	res, err := s.DB.ExecContext(ctx, `UPDATE reminder_jobs
		SET delivery_state = $2, suppress_reason = $6, retry_count = $3, retry_at = $4,
			delivery_note = $5, lease_token = NULL, lease_until = NULL, callback_key = $7,
			updated_at = now()
		WHERE id = $1 AND callback_key IS DISTINCT FROM $7
			AND delivery_state NOT IN ('delivered', 'exhausted', 'suppressed')`,
		jobID, tr.State, tr.RetryCount, retryAt, tr.Note, suppress, callbackKey)
	if err != nil {
		return false, err
	}
	rows, err := res.RowsAffected()
	return rows > 0, err
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
