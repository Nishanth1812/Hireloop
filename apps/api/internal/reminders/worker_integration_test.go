package reminders

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
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

func jobIDsForTask(ctx context.Context, t *testing.T, db *sql.DB, taskID string) []string {
	t.Helper()
	rows, err := db.QueryContext(ctx, `SELECT id FROM reminder_jobs WHERE task_id = $1 ORDER BY reminder_timing`, taskID)
	if err != nil {
		t.Fatalf("find jobs for task: %v", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan job id: %v", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows error: %v", err)
	}
	return ids
}

// jobIDForTaskTiming returns the job ID for a specific timing.
func jobIDForTaskTiming(ctx context.Context, t *testing.T, db *sql.DB, taskID string, timing ReminderTiming) string {
	t.Helper()
	var jobID string
	err := db.QueryRowContext(ctx, `SELECT id FROM reminder_jobs WHERE task_id = $1 AND reminder_timing = $2`, taskID, timing).Scan(&jobID)
	if errors.Is(err, sql.ErrNoRows) {
		return ""
	}
	if err != nil {
		t.Fatalf("find job for task timing: %v", err)
	}
	return jobID
}

// jobIDForTask returns the 2h_before job ID (or the first job if 2h_before
// doesn't exist). This is a backward-compatible helper for tests that expect
// a single job per task.
func jobIDForTask(ctx context.Context, t *testing.T, db *sql.DB, taskID string) string {
	t.Helper()
	// Try 2h_before first, then 24h_before, then any.
	for _, timing := range []ReminderTiming{Timing2hBefore, Timing24hBefore, TimingAtDeadline} {
		if id := jobIDForTaskTiming(ctx, t, db, taskID, timing); id != "" {
			return id
		}
	}
	t.Fatalf("no job found for task %s", taskID)
	return ""
}

// setJobDueAt sets the due_at (reminder time) for a job to a specific time.
func setJobDueAt(ctx context.Context, t *testing.T, db *sql.DB, jobID string, dueAt time.Time) {
	t.Helper()
	if _, err := db.ExecContext(ctx, `UPDATE reminder_jobs SET due_at = $1 WHERE id = $2`, dueAt, jobID); err != nil {
		t.Fatalf("set job due_at: %v", err)
	}
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

	// Use a deadline far enough in the future that both reminders are eligible.
	trueDeadline := time.Now().Add(48 * time.Hour)
	taskID := seedInstantTask(ctx, t, db, owner, trueDeadline)
	if err := store.Schedule(ctx, db, owner, taskID, trueDeadline); err != nil {
		t.Fatalf("schedule job: %v", err)
	}

	// Get both job IDs and set their due_at (reminder time) to be in the past
	// so the worker will claim them.
	jobID24 := jobIDForTaskTiming(ctx, t, db, taskID, Timing24hBefore)
	jobID2 := jobIDForTaskTiming(ctx, t, db, taskID, Timing2hBefore)
	if jobID24 == "" || jobID2 == "" {
		t.Fatalf("expected two jobs, got 24h=%q 2h=%q", jobID24, jobID2)
	}
	past := time.Now().Add(-time.Hour)
	setJobDueAt(ctx, t, db, jobID24, past)
	setJobDueAt(ctx, t, db, jobID2, past)

	disp := &fakeDispatcher{outcome: OutcomeDelivered}
	w := NewWorker(store, Config{Dispatcher: disp, Scope: []string{owner}, BatchSize: 5})
	n, err := w.poll(ctx, 5)
	if err != nil || n != 2 {
		t.Fatalf("poll = %d, %v", n, err)
	}
	if disp.count() != 2 {
		t.Fatalf("dispatches = %d, want 2", disp.count())
	}

	// Verify both jobs were dispatched with correct notification fields.
	for _, call := range disp.calls {
		if call.UserID != owner || call.TaskID != taskID || call.Title != "Interview" {
			t.Fatalf("notification = %+v", call)
		}
		// DueAt should be the reminder time (past), TrueDeadline should be the true deadline.
		if diff := call.DueAt.Sub(past); diff < -time.Millisecond || diff > time.Millisecond {
			t.Fatalf("notification due_at = %v, want %v", call.DueAt, past)
		}
		if diff := call.TrueDeadline.Sub(trueDeadline); diff < -time.Millisecond || diff > time.Millisecond {
			t.Fatalf("notification true_deadline = %v, want %v", call.TrueDeadline, trueDeadline)
		}
		if call.ReminderTiming != Timing24hBefore && call.ReminderTiming != Timing2hBefore {
			t.Fatalf("notification reminder_timing = %v, want 24h_before or 2h_before", call.ReminderTiming)
		}
	}

	// Both jobs should be terminal.
	for _, jid := range []string{jobID24, jobID2} {
		job, err := store.Get(ctx, owner, jid)
		if err != nil || job.DeliveryState != StateDelivered || job.LeaseToken != "" {
			t.Fatalf("job %s = %+v, %v", jid, job, err)
		}
	}

	// Second poll dispatches nothing.
	if n, err := w.poll(ctx, 5); err != nil || n != 0 || disp.count() != 2 {
		t.Fatalf("second poll = %d, %v, dispatches = %d", n, err, disp.count())
	}
}

func TestWorkerRetriesUncertainUntilExhaustedWithStableIdentity(t *testing.T) {
	store, db, owner := openStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	trueDeadline := time.Now().Add(48 * time.Hour)
	taskID := seedInstantTask(ctx, t, db, owner, trueDeadline)
	if err := store.Schedule(ctx, db, owner, taskID, trueDeadline); err != nil {
		t.Fatal(err)
	}

	// Use the 2h reminder job for this test.
	jobID := jobIDForTaskTiming(ctx, t, db, taskID, Timing2hBefore)
	if jobID == "" {
		t.Fatal("expected 2h job")
	}
	past := time.Now().Add(-time.Hour)
	setJobDueAt(ctx, t, db, jobID, past)

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

	trueDeadline := time.Now().Add(48 * time.Hour)
	taskID := seedInstantTask(ctx, t, db, owner, trueDeadline)
	if err := store.Schedule(ctx, db, owner, taskID, trueDeadline); err != nil {
		t.Fatal(err)
	}

	// Use the 2h reminder job for this test.
	jobID := jobIDForTaskTiming(ctx, t, db, taskID, Timing2hBefore)
	if jobID == "" {
		t.Fatal("expected 2h job")
	}
	past := time.Now().Add(-time.Hour)
	setJobDueAt(ctx, t, db, jobID, past)

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

	trueDeadline := time.Now().Add(48 * time.Hour)
	taskID := seedInstantTask(ctx, t, db, owner, trueDeadline)
	if err := store.Schedule(ctx, db, owner, taskID, trueDeadline); err != nil {
		t.Fatal(err)
	}

	// Use the 2h reminder job for this test.
	jobID := jobIDForTaskTiming(ctx, t, db, taskID, Timing2hBefore)
	if jobID == "" {
		t.Fatal("expected 2h job")
	}
	past := time.Now().Add(-time.Hour)
	setJobDueAt(ctx, t, db, jobID, past)

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

	trueDeadline := time.Now().Add(48 * time.Hour)
	taskID := seedInstantTask(ctx, t, db, owner, trueDeadline)
	if err := store.Schedule(ctx, db, owner, taskID, trueDeadline); err != nil {
		t.Fatal(err)
	}

	// Get both job IDs.
	jobIDs := jobIDsForTask(ctx, t, db, taskID)
	if len(jobIDs) != 2 {
		t.Fatalf("expected 2 jobs, got %d", len(jobIDs))
	}

	// Account deletion cascades the task row away while the jobs remain.
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
	// Both jobs should be suppressed.
	for _, jobID := range jobIDs {
		job, err := store.Get(ctx, owner, jobID)
		if err != nil || job.DeliveryState != StateSuppressed || job.SuppressReason != SuppressDeleted {
			t.Fatalf("job %s = %+v, %v", jobID, job, err)
		}
	}
}

func TestWorkerSuppressesWhenUserDisabled(t *testing.T) {
	store, db, owner := openStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	trueDeadline := time.Now().Add(48 * time.Hour)
	taskID := seedInstantTask(ctx, t, db, owner, trueDeadline)
	if err := store.Schedule(ctx, db, owner, taskID, trueDeadline); err != nil {
		t.Fatal(err)
	}

	// Get both job IDs.
	jobIDs := jobIDsForTask(ctx, t, db, taskID)
	if len(jobIDs) != 2 {
		t.Fatalf("expected 2 jobs, got %d", len(jobIDs))
	}

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
	// Both jobs should be suppressed.
	for _, jobID := range jobIDs {
		job, err := store.Get(ctx, owner, jobID)
		if err != nil || job.DeliveryState != StateSuppressed || job.SuppressReason != SuppressDisabled {
			t.Fatalf("job %s = %+v, %v", jobID, job, err)
		}
	}
	// Re-enabling does not resurrect the suppressed jobs.
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
	trueDeadlineA := future.Add(24 * time.Hour) // true deadline far in future
	taskA := seedInstantTask(ctx, t, db, owner, trueDeadlineA)
	if err := store.Schedule(ctx, db, owner, taskA, trueDeadlineA); err != nil {
		t.Fatal(err)
	}
	// Manually set the 2h reminder to be due now.
	jobID2h := jobIDForTaskTiming(ctx, t, db, taskA, Timing2hBefore)
	if jobID2h == "" {
		t.Fatal("expected 2h job")
	}
	if _, err := db.ExecContext(ctx, `UPDATE reminder_jobs SET due_at = $1 WHERE id = $2`, future, jobID2h); err != nil {
		t.Fatal(err)
	}
	jobs, err := store.ClaimDue(ctx, time.Now(), 5, "tok", time.Now().Add(30*time.Second), []string{owner})
	if err != nil || len(jobs) != 0 {
		t.Fatalf("claim due = %+v, %v", jobs, err)
	}

	// A due job is claimable for its owner only.
	due := time.Now().Add(-time.Minute)
	trueDeadlineB := due.Add(2 * time.Hour) // true deadline 2h in future
	taskB := seedInstantTask(ctx, t, db, owner, trueDeadlineB)
	if err := store.Schedule(ctx, db, owner, taskB, trueDeadlineB); err != nil {
		t.Fatal(err)
	}
	jobID2hB := jobIDForTaskTiming(ctx, t, db, taskB, Timing2hBefore)
	if jobID2hB == "" {
		t.Fatal("expected 2h job for B")
	}
	if _, err := db.ExecContext(ctx, `UPDATE reminder_jobs SET due_at = $1 WHERE id = $2`, due, jobID2hB); err != nil {
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

	trueDeadline := now.Add(48 * time.Hour)
	taskID := seedInstantTask(ctx, t, db, owner, trueDeadline)
	if err := store.Schedule(ctx, db, owner, taskID, trueDeadline); err != nil {
		t.Fatal(err)
	}

	// Use the 2h reminder job and set it to be due now.
	jobID := jobIDForTaskTiming(ctx, t, db, taskID, Timing2hBefore)
	if jobID == "" {
		t.Fatal("expected 2h job")
	}
	if _, err := db.ExecContext(ctx, `UPDATE reminder_jobs SET due_at = $1 WHERE id = $2`, now, jobID); err != nil {
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

	trueDeadline := time.Now().Add(48 * time.Hour)
	taskID := seedInstantTask(ctx, t, db, owner, trueDeadline)
	if err := store.Schedule(ctx, db, owner, taskID, trueDeadline); err != nil {
		t.Fatal(err)
	}
	jobIDs := jobIDsForTask(ctx, t, db, taskID)
	if len(jobIDs) != 2 {
		t.Fatalf("expected 2 jobs, got %d", len(jobIDs))
	}

	if err := store.Suppress(ctx, db, ownerB, taskID, SuppressCompleted); err != nil {
		t.Fatal(err)
	}
	// Neither job should be suppressed by other owner.
	for _, jobID := range jobIDs {
		job, err := store.Get(ctx, owner, jobID)
		if err != nil || job.DeliveryState != StateScheduled {
			t.Fatalf("other owner's suppress took effect: job %s = %+v, %v", jobID, job, err)
		}
	}
	if err := store.Suppress(ctx, db, owner, taskID, SuppressCompleted); err != nil {
		t.Fatal(err)
	}
	// Both jobs should be suppressed by owner.
	for _, jobID := range jobIDs {
		job, err := store.Get(ctx, owner, jobID)
		if err != nil || job.DeliveryState != StateSuppressed || job.SuppressReason != SuppressCompleted {
			t.Fatalf("job %s = %+v, %v", jobID, job, err)
		}
	}
}

func TestStoreGetReturnsNotFoundForOtherOwners(t *testing.T) {
	store, db, owner := openStore(t)
	ownerB := randomOwner(t)
	registerCleanup(t, db, ownerB)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	trueDeadline := time.Now().Add(48 * time.Hour)
	taskID := seedInstantTask(ctx, t, db, owner, trueDeadline)
	if err := store.Schedule(ctx, db, owner, taskID, trueDeadline); err != nil {
		t.Fatal(err)
	}
	jobIDs := jobIDsForTask(ctx, t, db, taskID)
	if len(jobIDs) != 2 {
		t.Fatalf("expected 2 jobs, got %d", len(jobIDs))
	}

	for _, jobID := range jobIDs {
		if _, err := store.Get(ctx, ownerB, jobID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("other owner's get = %v, want ErrNotFound", err)
		}
		if _, err := store.Get(ctx, owner, jobID); err != nil {
			t.Fatalf("owner get: %v", err)
		}
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

	trueDeadline := time.Now().Add(48 * time.Hour)
	taskID := seedInstantTask(ctx, t, db, owner, trueDeadline)
	if err := store.Schedule(ctx, db, owner, taskID, trueDeadline); err != nil {
		t.Fatal(err)
	}
	// Use the 2h reminder job.
	jobID := jobIDForTaskTiming(ctx, t, db, taskID, Timing2hBefore)
	if jobID == "" {
		t.Fatal("expected 2h job")
	}
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
	taskID2 := seedInstantTask(ctx, t, db, owner, trueDeadline)
	if err := store.Schedule(ctx, db, owner, taskID2, trueDeadline); err != nil {
		t.Fatal(err)
	}
	jobID2 := jobIDForTaskTiming(ctx, t, db, taskID2, Timing2hBefore)
	if jobID2 == "" {
		t.Fatal("expected 2h job for task2")
	}
	if applied, err := w.RecordCallback(ctx, owner, jobID2, OutcomeFailed); err != nil || !applied {
		t.Fatalf("callback on scheduled job = %v, %v", applied, err)
	}
	job2, err := store.Get(ctx, owner, jobID2)
	if err != nil || job2.DeliveryState != StateFailed || job2.RetryAt == nil {
		t.Fatalf("job2 = %+v, %v", job2, err)
	}
}

// TestRetentionCleanupRemovesPayloadsAndPendingEventsAfterTerminalState
// verifies the cleanup_reminder_retention function removes payloads for
// terminal jobs and stale pending events, while preserving evidence.
func TestRetentionCleanupRemovesPayloadsAndPendingEventsAfterTerminalState(t *testing.T) {
	store, db, owner := openStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	past := time.Now().Add(-time.Minute)

	// Job 1: delivered terminal state with payload and pending event
	task1 := seedInstantTask(ctx, t, db, owner, past)
	if err := store.Schedule(ctx, db, owner, task1, past); err != nil {
		t.Fatal(err)
	}
	jobID1 := jobIDForTask(ctx, t, db, task1)
	if err := store.SetProviderEmailID(ctx, jobID1, 1, "email-delivered"); err != nil {
		t.Fatal(err)
	}
	// Simulate a delivered job with payload
	if _, err := db.ExecContext(ctx, `UPDATE reminder_jobs SET delivery_state = 'delivered', updated_at = now() - interval '8 days' WHERE id = $1`, jobID1); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO reminder_delivery_payloads (job_id, delivery_version, recipient, subject, text_body) VALUES ($1, 1, 'test@example.com', 'Subject', 'Body')`, jobID1); err != nil {
		t.Fatal(err)
	}
	// Pending event for delivered job (will never be applied)
	if _, err := db.ExecContext(ctx, `INSERT INTO reminder_email_events (provider_event_id, provider_email_id, job_id, event_type, target_state, delivery_version, resolution, received_at) VALUES ('evt-pending-delivered', 'email-delivered', $1, 'email.delivered', 'delivered', 1, 'pending', now() - interval '8 days')`, jobID1); err != nil {
		t.Fatal(err)
	}

	// Job 2: exhausted terminal state with payload
	task2 := seedInstantTask(ctx, t, db, owner, past)
	if err := store.Schedule(ctx, db, owner, task2, past); err != nil {
		t.Fatal(err)
	}
	jobID2 := jobIDForTask(ctx, t, db, task2)
	if err := store.SetProviderEmailID(ctx, jobID2, 1, "email-exhausted"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE reminder_jobs SET delivery_state = 'exhausted', updated_at = now() - interval '8 days' WHERE id = $1`, jobID2); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO reminder_delivery_payloads (job_id, delivery_version, recipient, subject, text_body) VALUES ($1, 1, 'test2@example.com', 'Subject', 'Body')`, jobID2); err != nil {
		t.Fatal(err)
	}

	// Job 3: non-terminal (scheduled) with payload - should NOT be cleaned up
	task3 := seedInstantTask(ctx, t, db, owner, past.Add(time.Hour))
	if err := store.Schedule(ctx, db, owner, task3, past.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	jobID3 := jobIDForTask(ctx, t, db, task3)
	if _, err := db.ExecContext(ctx, `INSERT INTO reminder_delivery_payloads (job_id, delivery_version, recipient, subject, text_body) VALUES ($1, 1, 'test3@example.com', 'Subject', 'Body')`, jobID3); err != nil {
		t.Fatal(err)
	}

	// Job 4: rescheduled (version bumped) with old pending event - should clean up old pending event
	task4 := seedInstantTask(ctx, t, db, owner, past)
	if err := store.Schedule(ctx, db, owner, task4, past); err != nil {
		t.Fatal(err)
	}
	jobID4 := jobIDForTask(ctx, t, db, task4)
	if err := store.SetProviderEmailID(ctx, jobID4, 1, "email-rescheduled-v1"); err != nil {
		t.Fatal(err)
	}
	// Reschedule bumps version to 2
	if err := store.Schedule(ctx, db, owner, task4, past.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	// Pending event for old version (v1) - should be cleaned up
	if _, err := db.ExecContext(ctx, `INSERT INTO reminder_email_events (provider_event_id, provider_email_id, job_id, event_type, target_state, delivery_version, resolution, received_at) VALUES ('evt-pending-rescheduled', 'email-rescheduled-v1', $1, 'email.delivered', 'delivered', 1, 'pending', now() - interval '8 days')`, jobID4); err != nil {
		t.Fatal(err)
	}

	// Job 5: terminal but updated recently (within 7 days) - should NOT be cleaned up
	task5 := seedInstantTask(ctx, t, db, owner, past)
	if err := store.Schedule(ctx, db, owner, task5, past); err != nil {
		t.Fatal(err)
	}
	jobID5 := jobIDForTask(ctx, t, db, task5)
	if _, err := db.ExecContext(ctx, `UPDATE reminder_jobs SET delivery_state = 'suppressed', updated_at = now() - interval '1 day' WHERE id = $1`, jobID5); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO reminder_delivery_payloads (job_id, delivery_version, recipient, subject, text_body) VALUES ($1, 1, 'test5@example.com', 'Subject', 'Body')`, jobID5); err != nil {
		t.Fatal(err)
	}

	// Verify pre-cleanup state
	var payloadCount, pendingCount int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM reminder_delivery_payloads WHERE job_id IN ($1,$2,$3,$4,$5)`, jobID1, jobID2, jobID3, jobID4, jobID5).Scan(&payloadCount); err != nil {
		t.Fatal(err)
	}
	if payloadCount != 5 {
		t.Fatalf("pre-cleanup payload count = %d, want 5", payloadCount)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM reminder_email_events WHERE provider_event_id IN ('evt-pending-delivered', 'evt-pending-rescheduled')`).Scan(&pendingCount); err != nil {
		t.Fatal(err)
	}
	if pendingCount != 2 {
		t.Fatalf("pre-cleanup pending event count = %d, want 2", pendingCount)
	}

	// Run cleanup
	var deletedPayloads, deletedEvents int
	if err := db.QueryRowContext(ctx, `SELECT * FROM cleanup_reminder_retention()`).Scan(&deletedPayloads, &deletedEvents); err != nil {
		t.Fatal(err)
	}

	// Verify post-cleanup state
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM reminder_delivery_payloads WHERE job_id IN ($1,$2,$3,$4,$5)`, jobID1, jobID2, jobID3, jobID4, jobID5).Scan(&payloadCount); err != nil {
		t.Fatal(err)
	}
	// Job1, Job2 payloads deleted (terminal >7 days); Job3 kept (non-terminal); Job4 kept (terminal but version changed? no, job4 is scheduled now); Job5 kept (terminal but <7 days)
	// Actually job4 after reschedule is in 'scheduled' state (non-terminal), so its payload should be kept.
	// Expected remaining: job3 (scheduled), job4 (scheduled after reschedule), job5 (suppressed but <7 days) = 3
	if payloadCount != 3 {
		t.Fatalf("post-cleanup payload count = %d, want 3 (job3, job4, job5)", payloadCount)
	}

	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM reminder_email_events WHERE provider_event_id IN ('evt-pending-delivered', 'evt-pending-rescheduled')`).Scan(&pendingCount); err != nil {
		t.Fatal(err)
	}
	// Both pending events should be deleted: one for delivered job, one for rescheduled job
	if pendingCount != 0 {
		t.Fatalf("post-cleanup pending event count = %d, want 0", pendingCount)
	}

	// Verify applied/ignored events are preserved
	if _, err := db.ExecContext(ctx, `INSERT INTO reminder_email_events (provider_event_id, provider_email_id, job_id, event_type, target_state, delivery_version, resolution, received_at) VALUES ('evt-applied', 'email-delivered', $1, 'email.delivered', 'delivered', 1, 'applied', now() - interval '8 days')`, jobID1); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO reminder_email_events (provider_event_id, provider_email_id, job_id, event_type, target_state, delivery_version, resolution, received_at) VALUES ('evt-ignored', 'email-delivered', $1, 'email.delivered', 'delivered', 1, 'ignored', now() - interval '8 days')`, jobID1); err != nil {
		t.Fatal(err)
	}
	// Run cleanup again
	if _, err := db.ExecContext(ctx, `SELECT * FROM cleanup_reminder_retention()`); err != nil {
		t.Fatal(err)
	}
	var appliedCount, ignoredCount int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM reminder_email_events WHERE provider_event_id = 'evt-applied'`).Scan(&appliedCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM reminder_email_events WHERE provider_event_id = 'evt-ignored'`).Scan(&ignoredCount); err != nil {
		t.Fatal(err)
	}
	if appliedCount != 1 || ignoredCount != 1 {
		t.Fatalf("applied/ignored events not preserved: applied=%d ignored=%d", appliedCount, ignoredCount)
	}
}

// TestProviderMappingUniqueness verifies the unique constraint on
// (job_id, delivery_version) in reminder_provider_emails prevents
// duplicate history rows for the same logical send.
func TestProviderMappingUniqueness(t *testing.T) {
	store, db, owner := openStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	past := time.Now().Add(-time.Minute)
	taskID := seedInstantTask(ctx, t, db, owner, past)
	if err := store.Schedule(ctx, db, owner, taskID, past); err != nil {
		t.Fatal(err)
	}
	jobID := jobIDForTask(ctx, t, db, taskID)

	// First mapping succeeds
	if err := store.SetProviderEmailID(ctx, jobID, 1, "email-unique-1"); err != nil {
		t.Fatalf("first mapping failed: %v", err)
	}

	// Second mapping for same job/version with different email_id should fail
	// due to the unique constraint on (job_id, delivery_version)
	_, err := db.ExecContext(ctx, `INSERT INTO reminder_provider_emails (provider_email_id, job_id, user_id, delivery_version) VALUES ('email-unique-2', $1, $2, 1)`, jobID, owner)
	if err == nil {
		t.Fatal("expected unique constraint violation for duplicate job/version")
	}
	// The error should be a unique violation on reminder_provider_emails_job_version_idx
	if !strings.Contains(err.Error(), "reminder_provider_emails_job_version_idx") {
		t.Fatalf("expected unique constraint violation, got: %v", err)
	}

	// But a mapping for a different version of the same job should succeed
	if err := store.SetProviderEmailID(ctx, jobID, 2, "email-unique-v2"); err != nil {
		t.Fatalf("mapping for new version failed: %v", err)
	}
}

// TestRetentionCleanupMultipleQualifyingRows verifies cleanup handles
// multiple qualifying rows in a single execution (DELETE RETURNING INTO fix).
func TestRetentionCleanupMultipleQualifyingRows(t *testing.T) {
	store, db, owner := openStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	past := time.Now().Add(-time.Minute)

	// Create 10 terminal jobs with payloads, all >7 days old
	jobIDs := make([]string, 10)
	for i := 0; i < 10; i++ {
		taskID := seedInstantTask(ctx, t, db, owner, past)
		if err := store.Schedule(ctx, db, owner, taskID, past); err != nil {
			t.Fatal(err)
		}
		jobID := jobIDForTask(ctx, t, db, taskID)
		jobIDs[i] = jobID
		if _, err := db.ExecContext(ctx, `UPDATE reminder_jobs SET delivery_state = 'delivered', updated_at = now() - interval '8 days' WHERE id = $1`, jobID); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO reminder_delivery_payloads (job_id, delivery_version, recipient, subject, text_body) VALUES ($1, 1, $2, 'Subject', 'Body')`, jobID, fmt.Sprintf("test%d@example.com", i)); err != nil {
			t.Fatal(err)
		}
	}

	// Verify pre-cleanup: 10 payloads
	var payloadCount int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM reminder_delivery_payloads WHERE job_id = ANY($1)`, jobIDs).Scan(&payloadCount); err != nil {
		t.Fatal(err)
	}
	if payloadCount != 10 {
		t.Fatalf("pre-cleanup payload count = %d, want 10", payloadCount)
	}

	// Run cleanup - should not fail with "multiple rows returned" error
	var deletedPayloads, deletedEvents int
	if err := db.QueryRowContext(ctx, `SELECT * FROM cleanup_reminder_retention()`).Scan(&deletedPayloads, &deletedEvents); err != nil {
		t.Fatalf("cleanup failed: %v", err)
	}

	// Verify all 10 payloads deleted
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM reminder_delivery_payloads WHERE job_id = ANY($1)`, jobIDs).Scan(&payloadCount); err != nil {
		t.Fatal(err)
	}
	if payloadCount != 0 {
		t.Fatalf("post-cleanup payload count = %d, want 0", payloadCount)
	}
	if deletedPayloads != 10 {
		t.Fatalf("deleted payloads returned = %d, want 10", deletedPayloads)
	}
}

// TestRetentionCleanupRescheduledJobOldVersionPayload verifies that payloads
// for superseded versions (rescheduled jobs) are cleaned up immediately,
// regardless of job state.
func TestRetentionCleanupRescheduledJobOldVersionPayload(t *testing.T) {
	store, db, owner := openStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	past := time.Now().Add(-time.Minute)

	// Job 1: rescheduled from v1 to v2, v1 payload should be cleaned
	task1 := seedInstantTask(ctx, t, db, owner, past)
	if err := store.Schedule(ctx, db, owner, task1, past); err != nil {
		t.Fatal(err)
	}
	jobID1 := jobIDForTask(ctx, t, db, task1)
	// Insert v1 payload (simulating first attempt)
	if _, err := db.ExecContext(ctx, `INSERT INTO reminder_delivery_payloads (job_id, delivery_version, recipient, subject, text_body) VALUES ($1, 1, 'v1@example.com', 'Subject v1', 'Body')`, jobID1); err != nil {
		t.Fatal(err)
	}
	// Reschedule to v2
	if err := store.Schedule(ctx, db, owner, task1, past.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	// Insert v2 payload (current version)
	if _, err := db.ExecContext(ctx, `INSERT INTO reminder_delivery_payloads (job_id, delivery_version, recipient, subject, text_body) VALUES ($1, 2, 'v2@example.com', 'Subject v2', 'Body')`, jobID1); err != nil {
		t.Fatal(err)
	}

	// Job 2: non-rescheduled, v1 payload should be kept (current version)
	task2 := seedInstantTask(ctx, t, db, owner, past)
	if err := store.Schedule(ctx, db, owner, task2, past); err != nil {
		t.Fatal(err)
	}
	jobID2 := jobIDForTask(ctx, t, db, task2)
	if _, err := db.ExecContext(ctx, `INSERT INTO reminder_delivery_payloads (job_id, delivery_version, recipient, subject, text_body) VALUES ($1, 1, 'current@example.com', 'Subject', 'Body')`, jobID2); err != nil {
		t.Fatal(err)
	}

	// Verify pre-cleanup: 3 payloads (v1 and v2 for job1, v1 for job2)
	var payloadCount int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM reminder_delivery_payloads WHERE job_id IN ($1, $2)`, jobID1, jobID2).Scan(&payloadCount); err != nil {
		t.Fatal(err)
	}
	if payloadCount != 3 {
		t.Fatalf("pre-cleanup payload count = %d, want 3", payloadCount)
	}

	// Run cleanup
	if _, err := db.ExecContext(ctx, `SELECT * FROM cleanup_reminder_retention()`); err != nil {
		t.Fatal(err)
	}

	// Verify: job1 v1 payload deleted (superseded), job1 v2 kept, job2 v1 kept
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM reminder_delivery_payloads WHERE job_id IN ($1, $2)`, jobID1, jobID2).Scan(&payloadCount); err != nil {
		t.Fatal(err)
	}
	if payloadCount != 2 {
		t.Fatalf("post-cleanup payload count = %d, want 2 (job1 v2, job2 v1)", payloadCount)
	}
	// Verify specific versions
	var v1Count, v2Count int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM reminder_delivery_payloads WHERE job_id = $1 AND delivery_version = 1`, jobID1).Scan(&v1Count); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM reminder_delivery_payloads WHERE job_id = $1 AND delivery_version = 2`, jobID1).Scan(&v2Count); err != nil {
		t.Fatal(err)
	}
	if v1Count != 0 {
		t.Fatalf("job1 v1 payload not cleaned: count = %d, want 0", v1Count)
	}
	if v2Count != 1 {
		t.Fatalf("job1 v2 payload not kept: count = %d, want 1", v2Count)
	}
}

// TestRetentionCleanupUncorrelatedPendingEvents verifies that pending events
// with no job_id (arrived before provider mapping) are cleaned up after 7 days.
func TestRetentionCleanupUncorrelatedPendingEvents(t *testing.T) {
	store, db, owner := openStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	past := time.Now().Add(-time.Minute)

	// Create a job and provider mapping
	task1 := seedInstantTask(ctx, t, db, owner, past)
	if err := store.Schedule(ctx, db, owner, task1, past); err != nil {
		t.Fatal(err)
	}
	jobID1 := jobIDForTask(ctx, t, db, task1)
	if err := store.SetProviderEmailID(ctx, jobID1, 1, "email-correlated"); err != nil {
		t.Fatal(err)
	}

	// Correlated pending event (has job_id) for terminal job - should be cleaned
	if _, err := db.ExecContext(ctx, `UPDATE reminder_jobs SET delivery_state = 'delivered', updated_at = now() - interval '8 days' WHERE id = $1`, jobID1); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO reminder_email_events (provider_event_id, provider_email_id, job_id, event_type, target_state, delivery_version, resolution, received_at) VALUES ('evt-correlated-terminal', 'email-correlated', $1, 'email.delivered', 'delivered', 1, 'pending', now() - interval '8 days')`, jobID1); err != nil {
		t.Fatal(err)
	}

	// Uncorrelated pending event (no job_id, no provider_email_id mapping) - should be cleaned
	if _, err := db.ExecContext(ctx, `INSERT INTO reminder_email_events (provider_event_id, provider_email_id, job_id, event_type, target_state, delivery_version, resolution, received_at) VALUES ('evt-uncorrelated-old', 'email-unknown', NULL, 'email.delivered', 'delivered', 1, 'pending', now() - interval '8 days')`); err != nil {
		t.Fatal(err)
	}

	// Uncorrelated pending event recent (<7 days) - should NOT be cleaned
	if _, err := db.ExecContext(ctx, `INSERT INTO reminder_email_events (provider_event_id, provider_email_id, job_id, event_type, target_state, delivery_version, resolution, received_at) VALUES ('evt-uncorrelated-recent', 'email-unknown-2', NULL, 'email.delivered', 'delivered', 1, 'pending', now() - interval '1 day')`); err != nil {
		t.Fatal(err)
	}

	// Correlated pending event for non-terminal job - should NOT be cleaned
	task2 := seedInstantTask(ctx, t, db, owner, past)
	if err := store.Schedule(ctx, db, owner, task2, past); err != nil {
		t.Fatal(err)
	}
	jobID2 := jobIDForTask(ctx, t, db, task2)
	if err := store.SetProviderEmailID(ctx, jobID2, 1, "email-active"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO reminder_email_events (provider_event_id, provider_email_id, job_id, event_type, target_state, delivery_version, resolution, received_at) VALUES ('evt-correlated-active', 'email-active', $1, 'email.delivered', 'delivered', 1, 'pending', now() - interval '8 days')`, jobID2); err != nil {
		t.Fatal(err)
	}

	// Verify pre-cleanup: 4 pending events
	var pendingCount int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM reminder_email_events WHERE resolution = 'pending'`).Scan(&pendingCount); err != nil {
		t.Fatal(err)
	}
	if pendingCount != 4 {
		t.Fatalf("pre-cleanup pending count = %d, want 4", pendingCount)
	}

	// Run cleanup
	var deletedPayloads, deletedEvents int
	if err := db.QueryRowContext(ctx, `SELECT * FROM cleanup_reminder_retention()`).Scan(&deletedPayloads, &deletedEvents); err != nil {
		t.Fatal(err)
	}

	// Verify: 2 deleted (correlated terminal + uncorrelated old), 2 kept (uncorrelated recent + correlated active)
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM reminder_email_events WHERE resolution = 'pending'`).Scan(&pendingCount); err != nil {
		t.Fatal(err)
	}
	if pendingCount != 2 {
		t.Fatalf("post-cleanup pending count = %d, want 2", pendingCount)
	}
	if deletedEvents != 2 {
		t.Fatalf("deleted events returned = %d, want 2", deletedEvents)
	}

	// Verify specific events kept
	var keptRecent, keptActive int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM reminder_email_events WHERE provider_event_id = 'evt-uncorrelated-recent'`).Scan(&keptRecent); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM reminder_email_events WHERE provider_event_id = 'evt-correlated-active'`).Scan(&keptActive); err != nil {
		t.Fatal(err)
	}
	if keptRecent != 1 || keptActive != 1 {
		t.Fatalf("wrong events kept: recent=%d active=%d", keptRecent, keptActive)
	}
}

// TestRetentionCleanupActivePayloadPreservation verifies that payloads for
// active (non-terminal, current version) jobs are never cleaned up.
func TestRetentionCleanupActivePayloadPreservation(t *testing.T) {
	store, db, owner := openStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	past := time.Now().Add(-time.Minute)

	// Scheduled job with payload (non-terminal, current version)
	task1 := seedInstantTask(ctx, t, db, owner, past)
	if err := store.Schedule(ctx, db, owner, task1, past); err != nil {
		t.Fatal(err)
	}
	jobID1 := jobIDForTask(ctx, t, db, task1)
	if _, err := db.ExecContext(ctx, `INSERT INTO reminder_delivery_payloads (job_id, delivery_version, recipient, subject, text_body) VALUES ($1, 1, 'scheduled@example.com', 'Subject', 'Body')`, jobID1); err != nil {
		t.Fatal(err)
	}

	// Leased job with payload (non-terminal, current version)
	task2 := seedInstantTask(ctx, t, db, owner, past)
	if err := store.Schedule(ctx, db, owner, task2, past); err != nil {
		t.Fatal(err)
	}
	jobID2 := jobIDForTask(ctx, t, db, task2)
	if _, err := db.ExecContext(ctx, `UPDATE reminder_jobs SET delivery_state = 'leased', lease_token = 'tok', lease_until = now() + interval '1 hour' WHERE id = $1`, jobID2); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO reminder_delivery_payloads (job_id, delivery_version, recipient, subject, text_body) VALUES ($1, 1, 'leased@example.com', 'Subject', 'Body')`, jobID2); err != nil {
		t.Fatal(err)
	}

	// Failed job with payload (non-terminal, retryable, current version)
	task3 := seedInstantTask(ctx, t, db, owner, past)
	if err := store.Schedule(ctx, db, owner, task3, past); err != nil {
		t.Fatal(err)
	}
	jobID3 := jobIDForTask(ctx, t, db, task3)
	if _, err := db.ExecContext(ctx, `UPDATE reminder_jobs SET delivery_state = 'failed', retry_at = now() + interval '1 hour' WHERE id = $1`, jobID3); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO reminder_delivery_payloads (job_id, delivery_version, recipient, subject, text_body) VALUES ($1, 1, 'failed@example.com', 'Subject', 'Body')`, jobID3); err != nil {
		t.Fatal(err)
	}

	// Uncertain job with payload (non-terminal, retryable, current version)
	task4 := seedInstantTask(ctx, t, db, owner, past)
	if err := store.Schedule(ctx, db, owner, task4, past); err != nil {
		t.Fatal(err)
	}
	jobID4 := jobIDForTask(ctx, t, db, task4)
	if _, err := db.ExecContext(ctx, `UPDATE reminder_jobs SET delivery_state = 'uncertain', retry_at = now() + interval '1 hour' WHERE id = $1`, jobID4); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO reminder_delivery_payloads (job_id, delivery_version, recipient, subject, text_body) VALUES ($1, 1, 'uncertain@example.com', 'Subject', 'Body')`, jobID4); err != nil {
		t.Fatal(err)
	}

	// Accepted job with payload (non-terminal, current version)
	task5 := seedInstantTask(ctx, t, db, owner, past)
	if err := store.Schedule(ctx, db, owner, task5, past); err != nil {
		t.Fatal(err)
	}
	jobID5 := jobIDForTask(ctx, t, db, task5)
	if _, err := db.ExecContext(ctx, `UPDATE reminder_jobs SET delivery_state = 'accepted' WHERE id = $1`, jobID5); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO reminder_delivery_payloads (job_id, delivery_version, recipient, subject, text_body) VALUES ($1, 1, 'accepted@example.com', 'Subject', 'Body')`, jobID5); err != nil {
		t.Fatal(err)
	}

	// Verify pre-cleanup: 5 payloads
	var payloadCount int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM reminder_delivery_payloads WHERE job_id IN ($1,$2,$3,$4,$5)`, jobID1, jobID2, jobID3, jobID4, jobID5).Scan(&payloadCount); err != nil {
		t.Fatal(err)
	}
	if payloadCount != 5 {
		t.Fatalf("pre-cleanup payload count = %d, want 5", payloadCount)
	}

	// Run cleanup
	if _, err := db.ExecContext(ctx, `SELECT * FROM cleanup_reminder_retention()`); err != nil {
		t.Fatal(err)
	}

	// Verify all 5 payloads kept (none terminal, all current version)
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM reminder_delivery_payloads WHERE job_id IN ($1,$2,$3,$4,$5)`, jobID1, jobID2, jobID3, jobID4, jobID5).Scan(&payloadCount); err != nil {
		t.Fatal(err)
	}
	if payloadCount != 5 {
		t.Fatalf("post-cleanup payload count = %d, want 5 (all active payloads preserved)", payloadCount)
	}
}

// TestWorkerNoSendCleanupRuns verifies that the worker runs retention cleanup
// periodically even when sending is disabled (dispatcher is nil).
func TestWorkerNoSendCleanupRuns(t *testing.T) {
	store, db, owner := openStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	past := time.Now().Add(-time.Minute)

	// Create a terminal job with payload >7 days old
	task1 := seedInstantTask(ctx, t, db, owner, past)
	if err := store.Schedule(ctx, db, owner, task1, past); err != nil {
		t.Fatal(err)
	}
	jobID1 := jobIDForTask(ctx, t, db, task1)
	if _, err := db.ExecContext(ctx, `UPDATE reminder_jobs SET delivery_state = 'delivered', updated_at = now() - interval '8 days' WHERE id = $1`, jobID1); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO reminder_delivery_payloads (job_id, delivery_version, recipient, subject, text_body) VALUES ($1, 1, 'test@example.com', 'Subject', 'Body')`, jobID1); err != nil {
		t.Fatal(err)
	}

	// Create worker with nil dispatcher (no-send mode) and fast cleanup interval
	w := NewWorker(store, Config{
		Dispatcher:      nil, // no-send mode
		Scope:           []string{owner},
		BatchSize:       5,
		CleanupInterval: 100 * time.Millisecond, // fast for test
		PollInterval:    100 * time.Millisecond,
	})

	// Verify pre-cleanup: payload exists
	var payloadCount int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM reminder_delivery_payloads WHERE job_id = $1`, jobID1).Scan(&payloadCount); err != nil {
		t.Fatal(err)
	}
	if payloadCount != 1 {
		t.Fatalf("pre-cleanup payload count = %d, want 1", payloadCount)
	}

	// Run worker for a short time to trigger cleanup
	runCtx, runCancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer runCancel()
	_ = w.Run(runCtx) // Ignore context cancellation error

	// Verify payload cleaned up by worker's periodic cleanup
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM reminder_delivery_payloads WHERE job_id = $1`, jobID1).Scan(&payloadCount); err != nil {
		t.Fatal(err)
	}
	if payloadCount != 0 {
		t.Fatalf("post-worker-run payload count = %d, want 0 (cleanup should have run)", payloadCount)
	}
}

// TestWorkerCleanupDoesNotBlockPolling verifies that cleanup errors don't
// stop the worker's polling loop.
func TestWorkerCleanupDoesNotBlockPolling(t *testing.T) {
	store, db, owner := openStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	past := time.Now().Add(-time.Minute)

	// Create a due job
	task1 := seedInstantTask(ctx, t, db, owner, past)
	if err := store.Schedule(ctx, db, owner, task1, past); err != nil {
		t.Fatal(err)
	}
	jobID1 := jobIDForTask(ctx, t, db, task1)

	// Worker with nil dispatcher (idle) but fast intervals
	disp := &fakeDispatcher{outcome: OutcomeDelivered}
	w := NewWorker(store, Config{
		Dispatcher:      disp,
		Scope:           []string{owner},
		BatchSize:       5,
		CleanupInterval: 50 * time.Millisecond,
		PollInterval:    50 * time.Millisecond,
	})

	// Run worker briefly
	runCtx, runCancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer runCancel()
	_ = w.Run(runCtx)

	// Job should still be scheduled (no dispatcher = no dispatch)
	job, err := store.Get(ctx, owner, jobID1)
	if err != nil {
		t.Fatal(err)
	}
	if job.DeliveryState != StateScheduled {
		t.Fatalf("job state = %s, want scheduled (no dispatcher)", job.DeliveryState)
	}
}
