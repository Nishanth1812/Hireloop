package reminders

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"time"
)

// Worker claims due reminder jobs and dispatches them through the Dispatcher.
// Polling is the deployment unit: one worker claims a batch of due jobs with
// leases; the same worker recovers jobs whose lease expired when a previous
// worker stopped.
type Worker struct {
	store      *Store
	dispatcher Dispatcher
	cfg        Config
	now        func() time.Time
}

// Config holds the worker's bounds and its delivery-adapter boundary.
type Config struct {
	// PollInterval sleeps between polls (default 1s).
	PollInterval time.Duration
	// LeaseTTL is how long a claim is held before another worker may recover
	// the job (default 30s).
	LeaseTTL time.Duration
	// MaxRetries is the number of failed attempts that may still be retried;
	// the failure that follows the retry limit moves the job to exhausted
	// (default 5).
	MaxRetries int
	// BackoffBase is the first retry delay; it doubles per retry, capped at
	// BackoffMax (default 30s).
	BackoffBase time.Duration
	// BackoffMax caps the retry delay (default 5m).
	BackoffMax time.Duration
	// BatchSize bounds claimed jobs per poll (default 25).
	BatchSize int
	// Dispatcher is the delivery adapter; nil means NullDispatcher.
	Dispatcher Dispatcher
	// Scope limits claims to these users; empty means every user. Production
	// workers leave this empty.
	Scope []string
	// Now is the clock; tests override it. Defaults to time.Now.
	Now func() time.Time
}

func NewWorker(store *Store, cfg Config) *Worker {
	cfg.normalize()
	dispatcher := cfg.Dispatcher
	if dispatcher == nil {
		dispatcher = NullDispatcher{}
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &Worker{store: store, dispatcher: dispatcher, cfg: cfg, now: now}
}

func (c *Config) normalize() {
	if c.PollInterval <= 0 {
		c.PollInterval = time.Second
	}
	if c.LeaseTTL <= 0 {
		c.LeaseTTL = 30 * time.Second
	}
	if c.MaxRetries < 0 {
		c.MaxRetries = 0
	} else if c.MaxRetries == 0 {
		c.MaxRetries = 5
	}
	if c.BackoffBase <= 0 {
		c.BackoffBase = 30 * time.Second
	}
	if c.BackoffMax <= 0 {
		c.BackoffMax = 5 * time.Minute
	}
	if c.BatchSize <= 0 {
		c.BatchSize = 25
	}
}

// Run polls until ctx is canceled; it returns nil on cancellation.
func (w *Worker) Run(ctx context.Context) error {
	ticker := time.NewTicker(w.cfg.PollInterval)
	defer ticker.Stop()
	for {
		if _, err := w.poll(ctx, w.cfg.BatchSize); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// poll claims due jobs (within Config.Scope when set) and dispatches each one.
// The re-read in dispatchOne means suppression that lands between the claim
// and the dispatch takes effect.
func (w *Worker) poll(ctx context.Context, limit int) (int, error) {
	now := w.now()
	jobs, err := w.store.ClaimDue(ctx, now, limit, rand.Text(), now.Add(w.cfg.LeaseTTL), w.cfg.Scope)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, job := range jobs {
		if err := w.dispatchOne(ctx, job); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// dispatchOne dispatches one claimed job through the adapter and records the
// outcome atomically on the worker's lease.
func (w *Worker) dispatchOne(ctx context.Context, job Job) error {
	current, err := w.store.Get(ctx, job.UserID, job.ID)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	// Suppression or a takeover after lease expiry: never dispatch.
	if current.DeliveryState != StateLeased || current.SuppressReason != "" {
		return nil
	}
	// Privacy guard: the task must still exist. Application deletion cascades
	// task deletion, so the worker re-checks here and marks the job
	// suppressed instead of dispatching a reminder for a deleted account.
	var title string
	err = w.store.DB.QueryRowContext(ctx, `SELECT title FROM tasks WHERE id = $1`, current.TaskID).Scan(&title)
	if errors.Is(err, sql.ErrNoRows) {
		return w.store.RecordDelivery(ctx, current.ID, current.LeaseToken, Transition{
			State: StateSuppressed, Reason: SuppressDeleted, Note: "task deleted",
		})
	}
	if err != nil {
		return err
	}
	if enabled, err := w.store.RemindersEnabled(ctx, current.UserID); err != nil {
		return err
	} else if !enabled {
		return w.store.RecordDelivery(ctx, current.ID, current.LeaseToken, Transition{
			State: StateSuppressed, Reason: SuppressDisabled, Note: "reminders disabled",
		})
	}
	outcome := w.dispatcher.Send(ctx, Notification{
		ID: current.ID, UserID: current.UserID, TaskID: current.TaskID, DueAt: current.DueAt, Title: title,
	})
	return w.store.RecordDelivery(ctx, current.ID, current.LeaseToken, w.applyOutcome(current, outcome))
}

// RecordCallback applies an asynchronous provider callback to a job and
// reports whether the callback was new (applied). Repeated callbacks with the
// same callback_key are deduplicated and ignored; so are callbacks for
// terminal jobs.
func (w *Worker) RecordCallback(ctx context.Context, userID, notificationID, callbackKey string, outcome Outcome) (bool, error) {
	job, err := w.store.Get(ctx, userID, notificationID)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	switch job.DeliveryState {
	case StateDelivered, StateExhausted, StateSuppressed:
		return false, nil
	}
	return w.store.RecordCallback(ctx, job.ID, callbackKey, w.applyOutcome(job, outcome))
}

// applyOutcome maps a dispatcher outcome to an atomic transition: only a
// confirming adapter sets 'delivered' or 'accepted'; failures and ambiguous
// timeouts schedule retries.
func (w *Worker) applyOutcome(job Job, outcome Outcome) Transition {
	now := w.now()
	switch outcome {
	case OutcomeDelivered:
		return Transition{State: StateDelivered, Note: "delivered"}
	case OutcomeAccepted:
		return Transition{State: StateAccepted, Note: "accepted"}
	case OutcomeUncertain:
		return w.retryTransition(job, now, StateUncertain,
			"dispatch timed out or returned an ambiguous result; delivery may or may not have happened")
	default:
		return w.retryTransition(job, now, StateFailed, "dispatch failed")
	}
}

// retryTransition applies the retry limit: the failure that follows the
// limit moves the job to exhausted; otherwise the retry delay is recorded.
func (w *Worker) retryTransition(job Job, now time.Time, state, note string) Transition {
	if attempts := job.RetryCount + 1; attempts > w.cfg.MaxRetries {
		return Transition{State: StateExhausted, RetryCount: attempts, Note: note + "; retry limit reached"}
	} else {
		at := now.Add(w.backoff(attempts))
		return Transition{State: state, RetryCount: attempts, RetryAt: &at, Note: note}
	}
}

// backoff doubles the base delay per attempt, capped at BackoffMax.
func (w *Worker) backoff(attempt int) time.Duration {
	d := w.cfg.BackoffBase
	for shift := 1; shift < attempt && d < w.cfg.BackoffMax; shift++ {
		d *= 2
	}
	if d > w.cfg.BackoffMax {
		d = w.cfg.BackoffMax
	}
	return d
}
