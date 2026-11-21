package reminders

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"hireloop/api/db/migrations"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// Opt-in SQL-backed tests: set HIRELOOP_TEST_DATABASE_URL to a database used
// only for testing. Every row is written under a fresh random owner and
// removed in cleanup (jobs, tasks and applications); no other owner's data is
// read, written or reset.

func openStore(t *testing.T) (*Store, *sql.DB, string) {
	t.Helper()
	url := os.Getenv("HIRELOOP_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set HIRELOOP_TEST_DATABASE_URL to run SQL-backed reminder tests")
	}
	db, err := sql.Open("pgx", url)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(4)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("connect to database: %v", err)
	}
	if err := migrations.Apply(db); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	owner := randomOwner(t)
	registerCleanup(t, db, owner)
	return &Store{DB: db}, db, owner
}

func randomOwner(t *testing.T) string {
	t.Helper()
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("generate owner id: %v", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// registerCleanup deletes exactly the rows a random test owner can create.
func registerCleanup(t *testing.T, db *sql.DB, owner string) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		for _, stmt := range []string{
			`DELETE FROM reminder_email_events WHERE job_id IN (SELECT id FROM reminder_jobs WHERE user_id = $1)`,
			`DELETE FROM reminder_email_events WHERE provider_email_id IN (SELECT provider_email_id FROM reminder_jobs WHERE user_id = $1) AND job_id IS NULL`,
			`DELETE FROM reminder_jobs WHERE user_id = $1`,
			`DELETE FROM tasks WHERE user_id = $1`,
			`DELETE FROM applications WHERE user_id = $1`,
			`DELETE FROM user_notification_settings WHERE user_id = $1`,
		} {
			if _, err := db.ExecContext(ctx, stmt, owner); err != nil {
				t.Errorf("cleanup for test owner: %v", err)
			}
		}
	})
}

// seedInstantTask inserts an application and an open instant-deadline task
// directly: worker tests exercise the store and worker, not the tasks
// module's scheduling. The deadline shape CHECK requires a zone for instant
// precision, so the seed carries one. Returns the task id.
func seedInstantTask(ctx context.Context, t *testing.T, db *sql.DB, owner string, dueAt time.Time) string {
	t.Helper()
	var appID string
	if err := db.QueryRowContext(ctx,
		`INSERT INTO applications (user_id, company, role, status) VALUES ($1, 'Acme', 'Engineer', 'saved') RETURNING id`,
		owner).Scan(&appID); err != nil {
		t.Fatalf("seed application: %v", err)
	}
	var taskID string
	if err := db.QueryRowContext(ctx,
		`INSERT INTO tasks (user_id, application_id, title, status, due_precision, due_at, time_zone)
		 VALUES ($1, $2, 'Interview', 'open', 'instant', $3, 'UTC') RETURNING id`,
		owner, appID, dueAt).Scan(&taskID); err != nil {
		t.Fatalf("seed task: %v", err)
	}
	return taskID
}

func jobIDForTask(ctx context.Context, t *testing.T, db *sql.DB, taskID string) string {
	t.Helper()
	var jobID string
	if err := db.QueryRowContext(ctx, `SELECT id FROM reminder_jobs WHERE task_id = $1`, taskID).Scan(&jobID); err != nil {
		t.Fatalf("find job for task: %v", err)
	}
	return jobID
}

type fakeDispatcher struct {
	mu      sync.Mutex
	calls   []Notification
	outcome Outcome
}

func (d *fakeDispatcher) Send(_ context.Context, n Notification) Outcome {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls = append(d.calls, n)
	return d.outcome
}

func (d *fakeDispatcher) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.calls)
}

func TestWorkerDispatchesDueInstantDeadline(t *testing.T) {
	store, db, owner := openStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	past := time.Now().Add(-time.Hour)
	taskID := seedInstantTask(ctx, t, db, owner, past)
	if err := store.Schedule(ctx, db, owner, taskID, past); err != nil {
		t.Fatalf("schedule job: %v", err)
	}
	jobID := jobIDForTask(ctx, t, db, taskID)

	disp := &fakeDispatcher{outcome: OutcomeDelivered}
	w := NewWorker(store, Config{Dispatcher: disp, Scope: []string{owner}, BatchSize: 5})
	n, err := w.poll(ctx, 5)
	if err != nil || n != 1 {
		t.Fatalf("poll = %d, %v", n, err)
	}
	if disp.count() != 1 {
		t.Fatalf("dispatches = %d, want 1", disp.count())
	}
	call := disp.calls[0]
	if call.ID != jobID || call.UserID != owner || call.TaskID != taskID || call.Title != "Interview" {
		t.Fatalf("notification = %+v", call)
	}
	// The database stores timestamps at microsecond precision, so compare
	// against the instant with a small tolerance rather than exactly.
	if diff := call.DueAt.Sub(past); diff < -time.Millisecond || diff > time.Millisecond {
		t.Fatalf("notification due_at = %v, want %v", call.DueAt, past)
	}
	job, err := store.Get(ctx, owner, jobID)
	if err != nil || job.DeliveryState != StateDelivered || job.LeaseToken != "" {
		t.Fatalf("job = %+v, %v", job, err)
	}
	// The job is terminal: a second poll does not dispatch again.
	if n, err := w.poll(ctx, 5); err != nil || n != 0 || disp.count() != 1 {
		t.Fatalf("second poll = %d, %v, dispatches = %d", n, err, disp.count())
	}
}

func TestWorkerRetriesUncertainUntilExhaustedWithStableIdentity(t *testing.T) {
	store, db, owner := openStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	past := time.Now().Add(-time.Minute)
	taskID := seedInstantTask(ctx, t, db, owner, past)
	if err := store.Schedule(ctx, db, owner, taskID, past); err != nil {
		t.Fatal(err)
	}
	jobID := jobIDForTask(ctx, t, db, taskID)

	disp := &fakeDispatcher{outcome: OutcomeUncertain}
	w := NewWorker(store, Config{Dispatcher: disp, Scope: []string{owner}, BatchSize: 5, MaxRetries: 2})
	if _, err := w.poll(ctx, 5); err != nil {
		t.Fatal(err)
	}
	job, err := store.Get(ctx, owner, jobID)
	if err != nil || job.DeliveryState != StateUncertain || job.RetryCount != 1 || job.RetryAt == nil {
		t.Fatalf("job after first attempt = %+v, %v", job, err)
	}
	// Not due again: the backoff is in the future.
	if _, err := w.poll(ctx, 5); err != nil || disp.count() != 1 {
		t.Fatalf("poll before retry: %v, dispatches = %d", err, disp.count())
	}
	// Push the retry time into the past; the same job is re-claimed.
	if _, err := db.ExecContext(ctx, `UPDATE reminder_jobs SET retry_at = $1 WHERE id = $2`, time.Now().Add(-time.Second), jobID); err != nil {
		t.Fatal(err)
	}
	if _, err := w.poll(ctx, 5); err != nil || disp.count() != 2 {
		t.Fatalf("poll after retry push: %v, dispatches = %d", err, disp.count())
	}
	job, err = store.Get(ctx, owner, jobID)
	if err != nil || job.DeliveryState != StateUncertain || job.RetryCount != 2 {
		t.Fatalf("job after second attempt = %+v, %v", job, err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE reminder_jobs SET retry_at = $1 WHERE id = $2`, time.Now().Add(-time.Second), jobID); err != nil {
		t.Fatal(err)
	}
	if _, err := w.poll(ctx, 5); err != nil || disp.count() != 3 {
		t.Fatalf("poll after retry push 2: %v, dispatches = %d", err, disp.count())
	}
	job, err = store.Get(ctx, owner, jobID)
	if err != nil || job.DeliveryState != StateExhausted {
		t.Fatalf("final job = %+v, %v", job, err)
	}
	for _, n := range disp.calls {
		if n.ID != jobID {
			t.Fatalf("notification identity changed: %s", n.ID)
		}
	}
}

func TestWorkerFailedRetriesAndExhausts(t *testing.T) {
	store, db, owner := openStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	past := time.Now().Add(-time.Minute)
	taskID := seedInstantTask(ctx, t, db, owner, past)
	if err := store.Schedule(ctx, db, owner, taskID, past); err != nil {
		t.Fatal(err)
	}
	jobID := jobIDForTask(ctx, t, db, taskID)

	disp := &fakeDispatcher{outcome: OutcomeFailed}
	w := NewWorker(store, Config{Dispatcher: disp, Scope: []string{owner}, BatchSize: 5, MaxRetries: 2})
	// Failure 1: retry scheduled.
	if _, err := w.poll(ctx, 5); err != nil {
		t.Fatal(err)
	}
	job, err := store.Get(ctx, owner, jobID)
	if err != nil || job.DeliveryState != StateFailed || job.RetryAt == nil {
		t.Fatalf("job after failure = %+v, %v", job, err)
	}
	// Backoff not elapsed: nothing dispatches.
	if _, err := w.poll(ctx, 5); err != nil || disp.count() != 1 {
		t.Fatalf("poll before retry: %v, dispatches = %d", err, disp.count())
	}
	// Failure 2 (retry limit reached on the next failure): exhausted.
	if _, err := db.ExecContext(ctx, `UPDATE reminder_jobs SET retry_at = $1 WHERE id = $2`, time.Now().Add(-time.Second), jobID); err != nil {
		t.Fatal(err)
	}
	if _, err := w.poll(ctx, 5); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE reminder_jobs SET retry_at = $1 WHERE id = $2`, time.Now().Add(-time.Second), jobID); err != nil {
		t.Fatal(err)
	}
	if _, err := w.poll(ctx, 5); err != nil {
		t.Fatal(err)
	}
	job, err = store.Get(ctx, owner, jobID)
	if err != nil || job.DeliveryState != StateExhausted || job.RetryAt != nil {
		t.Fatalf("final job = %+v, %v", job, err)
	}
	for _, n := range disp.calls {
		if n.ID != jobID {
			t.Fatalf("notification identity changed: %s", n.ID)
		}
	}
}

func TestWorkerRecoversExpiredLease(t *testing.T) {
	store, db, owner := openStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	past := time.Now().Add(-time.Hour)
	taskID := seedInstantTask(ctx, t, db, owner, past)
	if err := store.Schedule(ctx, db, owner, taskID, past); err != nil {
		t.Fatal(err)
	}
	jobID := jobIDForTask(ctx, t, db, taskID)

	// A worker claims the job and stops before dispatching: the lease expires.
	if _, err := store.ClaimDue(ctx, time.Now(), 5, "dead-worker", time.Now().Add(30*time.Second), []string{owner}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE reminder_jobs SET lease_until = $1 WHERE id = $2`, time.Now().Add(-time.Second), jobID); err != nil {
		t.Fatal(err)
	}

	disp := &fakeDispatcher{outcome: OutcomeDelivered}
	w := NewWorker(store, Config{Dispatcher: disp, Scope: []string{owner}, BatchSize: 5})
	if n, err := w.poll(ctx, 5); err != nil || n != 1 || disp.count() != 1 {
		t.Fatalf("recovered poll = %d, %v, dispatches = %d", n, err, disp.count())
	}
	job, err := store.Get(ctx, owner, jobID)
	if err != nil || job.DeliveryState != StateDelivered {
		t.Fatalf("job = %+v, %v", job, err)
	}
}

func TestWorkerSuppressesWhenTaskIsDeleted(t *testing.T) {
	store, db, owner := openStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	past := time.Now().Add(-time.Hour)
	taskID := seedInstantTask(ctx, t, db, owner, past)
	if err := store.Schedule(ctx, db, owner, taskID, past); err != nil {
		t.Fatal(err)
	}
	jobID := jobIDForTask(ctx, t, db, taskID)

	// Account deletion cascades the task row away while the job remains.
	if _, err := db.ExecContext(ctx, `DELETE FROM tasks WHERE id = $1`, taskID); err != nil {
		t.Fatal(err)
	}

	disp := &fakeDispatcher{outcome: OutcomeDelivered}
	w := NewWorker(store, Config{Dispatcher: disp, Scope: []string{owner}, BatchSize: 5})
	if _, err := w.poll(ctx, 5); err != nil {
		t.Fatal(err)
	}
	if disp.count() != 0 {
		t.Fatal("worker dispatched a job for a deleted task")
	}
	job, err := store.Get(ctx, owner, jobID)
	if err != nil || job.DeliveryState != StateSuppressed || job.SuppressReason != SuppressDeleted {
		t.Fatalf("job = %+v, %v", job, err)
	}
}

func TestWorkerSuppressesWhenUserDisabled(t *testing.T) {
	store, db, owner := openStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	past := time.Now().Add(-time.Hour)
	taskID := seedInstantTask(ctx, t, db, owner, past)
	if err := store.Schedule(ctx, db, owner, taskID, past); err != nil {
		t.Fatal(err)
	}
	jobID := jobIDForTask(ctx, t, db, taskID)

	if err := store.SetRemindersEnabled(ctx, owner, false); err != nil {
		t.Fatal(err)
	}
	disp := &fakeDispatcher{outcome: OutcomeDelivered}
	w := NewWorker(store, Config{Dispatcher: disp, Scope: []string{owner}, BatchSize: 5})
	if _, err := w.poll(ctx, 5); err != nil {
		t.Fatal(err)
	}
	if disp.count() != 0 {
		t.Fatal("worker dispatched for a disabled user")
	}
	job, err := store.Get(ctx, owner, jobID)
	if err != nil || job.DeliveryState != StateSuppressed || job.SuppressReason != SuppressDisabled {
		t.Fatalf("job = %+v, %v", job, err)
	}
	// Re-enabling does not resurrect the suppressed job.
	if err := store.SetRemindersEnabled(ctx, owner, true); err != nil {
		t.Fatal(err)
	}
	if _, err := w.poll(ctx, 5); err != nil || disp.count() != 0 {
		t.Fatalf("poll after re-enable: %v, dispatches = %d", err, disp.count())
	}
}

func TestStoreClaimDueRequiresDueAndIsScoped(t *testing.T) {
	store, db, owner := openStore(t)
	ownerB := randomOwner(t)
	registerCleanup(t, db, ownerB)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// A future-due job is not claimable.
	future := time.Now().Add(time.Hour)
	taskA := seedInstantTask(ctx, t, db, owner, future)
	if err := store.Schedule(ctx, db, owner, taskA, future); err != nil {
		t.Fatal(err)
	}
	jobs, err := store.ClaimDue(ctx, time.Now(), 5, "tok", time.Now().Add(30*time.Second), []string{owner})
	if err != nil || len(jobs) != 0 {
		t.Fatalf("claim due = %+v, %v", jobs, err)
	}

	// A due job is claimable for its owner only.
	due := time.Now().Add(-time.Minute)
	taskB := seedInstantTask(ctx, t, db, owner, due)
	if err := store.Schedule(ctx, db, owner, taskB, due); err != nil {
		t.Fatal(err)
	}
	jobs, err = store.ClaimDue(ctx, due, 5, "tok", due.Add(30*time.Second), []string{ownerB})
	if err != nil || len(jobs) != 0 {
		t.Fatalf("other owner's scope claimed owner A's job: %+v, %v", jobs, err)
	}
	jobs, err = store.ClaimDue(ctx, due, 5, "tok", due.Add(30*time.Second), []string{owner})
	if err != nil || len(jobs) != 1 || jobs[0].TaskID != taskB {
		t.Fatalf("claim due for owner = %+v, %v", jobs, err)
	}
	if jobs[0].LeaseToken != "tok" {
		t.Fatalf("claimed lease token = %q, want tok", jobs[0].LeaseToken)
	}
}

func TestStoreClaimDueIsAtomicBetweenWorkers(t *testing.T) {
	store, db, owner := openStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	now := time.Now()
	past := now.Add(-time.Hour)
	taskID := seedInstantTask(ctx, t, db, owner, past)
	if err := store.Schedule(ctx, db, owner, taskID, past); err != nil {
		t.Fatal(err)
	}

	// Two workers claim the same due job concurrently: the atomic claim lets
	// exactly one win, so a job is never dispatched by two workers at once.
	var wg sync.WaitGroup
	claims := make([][]Job, 2)
	for slot := range claims {
		wg.Add(1)
		go func(slot int) {
			defer wg.Done()
			jobs, err := store.ClaimDue(ctx, now, 5, fmt.Sprintf("worker-%d", slot), now.Add(30*time.Second), []string{owner})
			if err != nil {
				t.Errorf("claim: %v", err)
			}
			claims[slot] = jobs
		}(slot)
	}
	wg.Wait()
	if len(claims[0])+len(claims[1]) != 1 {
		t.Fatalf("concurrent claims = %+v + %+v, want exactly one winner", claims[0], claims[1])
	}
}

func TestStoreSuppressIsOwnerScoped(t *testing.T) {
	store, db, owner := openStore(t)
	ownerB := randomOwner(t)
	registerCleanup(t, db, ownerB)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	past := time.Now().Add(-time.Hour)
	taskID := seedInstantTask(ctx, t, db, owner, past)
	if err := store.Schedule(ctx, db, owner, taskID, past); err != nil {
		t.Fatal(err)
	}
	jobID := jobIDForTask(ctx, t, db, taskID)

	if err := store.Suppress(ctx, db, ownerB, taskID, SuppressCompleted); err != nil {
		t.Fatal(err)
	}
	job, err := store.Get(ctx, owner, jobID)
	if err != nil || job.DeliveryState != StateScheduled {
		t.Fatalf("other owner's suppress took effect: job = %+v, %v", job, err)
	}
	if err := store.Suppress(ctx, db, owner, taskID, SuppressCompleted); err != nil {
		t.Fatal(err)
	}
	job, err = store.Get(ctx, owner, jobID)
	if err != nil || job.DeliveryState != StateSuppressed || job.SuppressReason != SuppressCompleted {
		t.Fatalf("job = %+v, %v", job, err)
	}
}

func TestStoreGetReturnsNotFoundForOtherOwners(t *testing.T) {
	store, db, owner := openStore(t)
	ownerB := randomOwner(t)
	registerCleanup(t, db, ownerB)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	past := time.Now().Add(-time.Hour)
	taskID := seedInstantTask(ctx, t, db, owner, past)
	if err := store.Schedule(ctx, db, owner, taskID, past); err != nil {
		t.Fatal(err)
	}
	jobID := jobIDForTask(ctx, t, db, taskID)
	if _, err := store.Get(ctx, ownerB, jobID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other owner's get = %v, want ErrNotFound", err)
	}
	if _, err := store.Get(ctx, owner, jobID); err != nil {
		t.Fatalf("owner get: %v", err)
	}
}

func TestSettingsRoundTripAndDefault(t *testing.T) {
	store, db, owner := openStore(t)
	ownerB := randomOwner(t)
	registerCleanup(t, db, ownerB)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	if enabled, err := store.RemindersEnabled(ctx, owner); err != nil || !enabled {
		t.Fatalf("default = %v, %v", enabled, err)
	}
	if err := store.SetRemindersEnabled(ctx, owner, false); err != nil {
		t.Fatal(err)
	}
	if enabled, err := store.RemindersEnabled(ctx, owner); err != nil || enabled {
		t.Fatalf("after disable = %v, %v", enabled, err)
	}
	if enabled, err := store.RemindersEnabled(ctx, ownerB); err != nil || !enabled {
		t.Fatalf("other owner affected: %v, %v", enabled, err)
	}
	if err := store.SetRemindersEnabled(ctx, owner, true); err != nil {
		t.Fatal(err)
	}
	if enabled, err := store.RemindersEnabled(ctx, owner); err != nil || !enabled {
		t.Fatalf("after re-enable = %v, %v", enabled, err)
	}
}

func TestRecordCallbackDeduplicatesAndApplies(t *testing.T) {
	store, db, owner := openStore(t)
	ownerB := randomOwner(t)
	registerCleanup(t, db, ownerB)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	past := time.Now().Add(-time.Hour)
	taskID := seedInstantTask(ctx, t, db, owner, past)
	if err := store.Schedule(ctx, db, owner, taskID, past); err != nil {
		t.Fatal(err)
	}
	jobID := jobIDForTask(ctx, t, db, taskID)
	w := NewWorker(store, Config{Scope: []string{owner}, MaxRetries: 3})

	applied, err := w.RecordCallback(ctx, owner, jobID, OutcomeDelivered)
	if err != nil || !applied {
		t.Fatalf("first callback = %v, %v", applied, err)
	}
	job, err := store.Get(ctx, owner, jobID)
	if err != nil || job.DeliveryState != StateDelivered {
		t.Fatalf("job = %+v, %v", job, err)
	}
	// A repeated callback changes nothing: the transition is not strictly
	// stronger than the recorded state.
	if applied, err := w.RecordCallback(ctx, owner, jobID, OutcomeDelivered); err != nil || applied {
		t.Fatalf("repeated callback = %v, %v", applied, err)
	}
	// Callbacks for terminal jobs are ignored, whatever the outcome.
	if applied, err := w.RecordCallback(ctx, owner, jobID, OutcomeFailed); err != nil || applied {
		t.Fatalf("callback on terminal job = %v, %v", applied, err)
	}
	// Another owner's callback for the same notification id is not found.
	if applied, err := w.RecordCallback(ctx, ownerB, jobID, OutcomeDelivered); err != nil || applied {
		t.Fatalf("callback with another owner = %v, %v", applied, err)
	}

	// A callback on a non-terminal job applies the retry transition.
	taskID2 := seedInstantTask(ctx, t, db, owner, past)
	if err := store.Schedule(ctx, db, owner, taskID2, past); err != nil {
		t.Fatal(err)
	}
	jobID2 := jobIDForTask(ctx, t, db, taskID2)
	if applied, err := w.RecordCallback(ctx, owner, jobID2, OutcomeFailed); err != nil || !applied {
		t.Fatalf("callback on scheduled job = %v, %v", applied, err)
	}
	job2, err := store.Get(ctx, owner, jobID2)
	if err != nil || job2.DeliveryState != StateFailed || job2.RetryAt == nil {
		t.Fatalf("job2 = %+v, %v", job2, err)
	}
}
