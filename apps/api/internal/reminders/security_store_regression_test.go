package reminders

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"hireloop/api/internal/resend"
)

// Opt-in real-Postgres regressions for the Phase 4 security review. Set
// HIRELOOP_TEST_DATABASE_URL to run them. Every row is written under a fresh
// random owner and removed by registerCleanup; no other owner's data is read,
// reset or changed. Event and email ids are prefixed "sec-" so they cannot
// collide with other suites sharing the test database.

func securityLedgerCount(ctx context.Context, t *testing.T, db *sql.DB, eventID string) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM reminder_email_events WHERE provider_event_id = $1`, eventID).Scan(&n); err != nil {
		t.Fatalf("count ledger: %v", err)
	}
	return n
}

func securityLedgerRow(ctx context.Context, t *testing.T, db *sql.DB, eventID string) (resolution, jobID string, version int) {
	t.Helper()
	var v sql.NullInt64
	if err := db.QueryRowContext(ctx, `SELECT resolution, COALESCE(job_id::text, ''), delivery_version
		FROM reminder_email_events WHERE provider_event_id = $1`, eventID).
		Scan(&resolution, &jobID, &v); err != nil {
		t.Fatalf("read ledger row: %v", err)
	}
	return resolution, jobID, int(v.Int64)
}

// TestSecurityWebhookLedgerRollsBackWithFailedTransition injects a transition
// failure (an invalid suppress_reason violates an existing CHECK) and proves
// the ledger insert is rolled back with it: the next delivery reprocesses the
// event instead of treating it as an already-seen replay and losing it.
func TestSecurityWebhookLedgerRollsBackWithFailedTransition(t *testing.T) {
	store, db, owner := openStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	// Use a future true deadline so Schedule creates the advance reminder jobs.
	trueDeadline := time.Now().Add(48 * time.Hour)
	taskID := seedInstantTask(ctx, t, db, owner, trueDeadline)
	if err := store.Schedule(ctx, db, owner, taskID, trueDeadline); err != nil {
		t.Fatal(err)
	}
	jobID := jobIDForTask(ctx, t, db, taskID)
	if err := store.SetProviderEmailID(ctx, jobID, 1, "sec-email-rollback"); err != nil {
		t.Fatal(err)
	}

	bad := Transition{State: StateDelivered, Reason: "not-a-valid-suppress-reason", Note: "injected failure"}
	if _, err := store.RecordWebhookEvent(ctx, "sec-evt-rollback", "sec-email-rollback", "email.delivered", bad); err == nil {
		t.Fatal("expected the injected transition failure to fail the webhook")
	}
	if n := securityLedgerCount(ctx, t, db, "sec-evt-rollback"); n != 0 {
		t.Fatalf("ledger rows after rollback = %d, want 0", n)
	}
	job, err := store.Get(ctx, owner, jobID)
	if err != nil || job.DeliveryState != StateScheduled {
		t.Fatalf("job after rollback = %+v, %v", job, err)
	}

	good := Transition{State: StateDelivered, Note: "provider confirmed delivery"}
	applied, err := store.RecordWebhookEvent(ctx, "sec-evt-rollback", "sec-email-rollback", "email.delivered", good)
	if err != nil || !applied {
		t.Fatalf("replay after rollback = %v, %v", applied, err)
	}
	if n := securityLedgerCount(ctx, t, db, "sec-evt-rollback"); n != 1 {
		t.Fatalf("ledger rows after replay = %d, want 1", n)
	}
	resolution, _, _ := securityLedgerRow(ctx, t, db, "sec-evt-rollback")
	if resolution != "applied" {
		t.Fatalf("ledger resolution = %q, want applied", resolution)
	}
	job, err = store.Get(ctx, owner, jobID)
	if err != nil || job.DeliveryState != StateDelivered {
		t.Fatalf("job after replay = %+v, %v", job, err)
	}
}

// TestSecurityWebhookBeforeMappingIsReconciled proves a callback that races
// ahead of the provider-email write is kept pending and applied when the
// mapping is persisted, rather than being silently lost.
func TestSecurityWebhookBeforeMappingIsReconciled(t *testing.T) {
	store, db, owner := openStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	// Use a future true deadline so Schedule creates the advance reminder jobs.
	trueDeadline := time.Now().Add(48 * time.Hour)
	taskID := seedInstantTask(ctx, t, db, owner, trueDeadline)
	if err := store.Schedule(ctx, db, owner, taskID, trueDeadline); err != nil {
		t.Fatal(err)
	}
	// Get the 2h reminder job and set its due_at to the past so it's claimable.
	jobID2h := jobIDForTaskTiming(ctx, t, db, taskID, Timing2hBefore)
	if jobID2h == "" {
		t.Fatal("expected 2h job")
	}
	past := time.Now().Add(-time.Hour)
	setJobDueAt(ctx, t, db, jobID2h, past)
	jobs, err := store.ClaimDue(ctx, past, 5, "sec-token", past.Add(time.Minute), []string{owner})
	if err != nil || len(jobs) != 1 {
		t.Fatalf("claim = %+v, %v", jobs, err)
	}

	applied, err := store.RecordWebhookEvent(ctx, "sec-evt-early", "sec-email-early", "email.delivered",
		Transition{State: StateDelivered, Note: "arrived before the mapping"})
	if err != nil || applied {
		t.Fatalf("early callback = %v, %v", applied, err)
	}
	resolution, _, _ := securityLedgerRow(ctx, t, db, "sec-evt-early")
	if resolution != "pending" {
		t.Fatalf("early event resolution = %q, want pending", resolution)
	}
	job, err := store.Get(ctx, owner, jobID2h)
	if err != nil || job.DeliveryState != StateLeased {
		t.Fatalf("job before mapping = %+v, %v", job, err)
	}

	// The worker persists the mapping after the provider accepted the send.
	if err := store.SetProviderEmailID(ctx, jobID2h, 1, "sec-email-early"); err != nil {
		t.Fatal(err)
	}
	job, err = store.Get(ctx, owner, jobID2h)
	if err != nil || job.DeliveryState != StateDelivered {
		t.Fatalf("job after reconciliation = %+v, %v", job, err)
	}
	resolution, correlatedJob, version := securityLedgerRow(ctx, t, db, "sec-evt-early")
	if resolution != "applied" || correlatedJob != jobID2h || version != 1 {
		t.Fatalf("reconciled ledger row = %q/%s/v%d", resolution, correlatedJob, version)
	}
}

// TestSecurityLateEventsAreEvidenceOnlyAfterSuppressionAndReschedule proves
// a late delivered event is recorded honestly against the job that sent it,
// but never revives a suppressed or rescheduled job.
func TestSecurityLateEventsAreEvidenceOnlyAfterSuppressionAndReschedule(t *testing.T) {
	store, db, owner := openStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	// Use a future true deadline so Schedule creates the advance reminder jobs.
	trueDeadline := time.Now().Add(48 * time.Hour)
	taskID := seedInstantTask(ctx, t, db, owner, trueDeadline)
	if err := store.Schedule(ctx, db, owner, taskID, trueDeadline); err != nil {
		t.Fatal(err)
	}
	jobID := jobIDForTask(ctx, t, db, taskID)
	if err := store.SetProviderEmailID(ctx, jobID, 1, "sec-email-late"); err != nil {
		t.Fatal(err)
	}
	if err := store.Suppress(ctx, db, owner, taskID, SuppressCompleted); err != nil {
		t.Fatal(err)
	}

	applied, err := store.RecordWebhookEvent(ctx, "sec-evt-late-suppressed", "sec-email-late", "email.delivered",
		Transition{State: StateDelivered, Note: "late delivered"})
	if err != nil || applied {
		t.Fatalf("late event after suppression = %v, %v", applied, err)
	}
	job, err := store.Get(ctx, owner, jobID)
	if err != nil || job.DeliveryState != StateSuppressed {
		t.Fatalf("suppressed job was revived: %+v, %v", job, err)
	}
	resolution, correlatedJob, version := securityLedgerRow(ctx, t, db, "sec-evt-late-suppressed")
	if resolution != "ignored" || correlatedJob != jobID || version != 1 {
		t.Fatalf("evidence row = %q/%s/v%d", resolution, correlatedJob, version)
	}

	// Rescheduling bumps the version and clears the live mapping; an old
	// event for the same email must still be evidence only.
	newDeadline := trueDeadline.Add(time.Hour)
	if err := store.Schedule(ctx, db, owner, taskID, newDeadline); err != nil {
		t.Fatal(err)
	}
	job, err = store.Get(ctx, owner, jobID)
	if err != nil || job.DeliveryVersion != 2 || job.ProviderEmailID != "" || job.DeliveryState != StateScheduled {
		t.Fatalf("rescheduled job = %+v, %v", job, err)
	}
	applied, err = store.RecordWebhookEvent(ctx, "sec-evt-late-rescheduled", "sec-email-late", "email.delivered",
		Transition{State: StateDelivered, Note: "old version delivered"})
	if err != nil || applied {
		t.Fatalf("late event after reschedule = %v, %v", applied, err)
	}
	job, err = store.Get(ctx, owner, jobID)
	if err != nil || job.DeliveryState != StateScheduled || job.DeliveryVersion != 2 {
		t.Fatalf("rescheduled job was corrupted: %+v, %v", job, err)
	}
	resolution, correlatedJob, version = securityLedgerRow(ctx, t, db, "sec-evt-late-rescheduled")
	if resolution != "ignored" || correlatedJob != jobID || version != 1 {
		t.Fatalf("old-version evidence row = %q/%s/v%d", resolution, correlatedJob, version)
	}
}

// TestSecurityLateDeliveredAfterExhaustionDoesNotRevive covers the exhausted
// case: the live mapping is present, but a terminal state cannot be weakened.
func TestSecurityLateDeliveredAfterExhaustionDoesNotRevive(t *testing.T) {
	store, db, owner := openStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	// Use a future true deadline so Schedule creates the advance reminder jobs.
	trueDeadline := time.Now().Add(48 * time.Hour)
	taskID := seedInstantTask(ctx, t, db, owner, trueDeadline)
	if err := store.Schedule(ctx, db, owner, taskID, trueDeadline); err != nil {
		t.Fatal(err)
	}
	jobID := jobIDForTask(ctx, t, db, taskID)
	if err := store.SetProviderEmailID(ctx, jobID, 1, "sec-email-exhausted"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx,
		`UPDATE reminder_jobs SET delivery_state = 'exhausted', lease_token = NULL WHERE id = $1`, jobID); err != nil {
		t.Fatal(err)
	}
	applied, err := store.RecordWebhookEvent(ctx, "sec-evt-late-exhausted", "sec-email-exhausted", "email.delivered",
		Transition{State: StateDelivered, Note: "late delivered"})
	if err != nil || applied {
		t.Fatalf("late delivered on exhausted job = %v, %v", applied, err)
	}
	job, err := store.Get(ctx, owner, jobID)
	if err != nil || job.DeliveryState != StateExhausted {
		t.Fatalf("exhausted job was revived: %+v, %v", job, err)
	}
	resolution, _, _ := securityLedgerRow(ctx, t, db, "sec-evt-late-exhausted")
	if resolution != "ignored" {
		t.Fatalf("evidence resolution = %q, want ignored", resolution)
	}
}

// TestSecurityFailedWebhookPreservesRetryBookkeeping proves an email.failed
// event derives a bounded backoff from the current retry count instead of
// resetting it to zero/nil, which would let the worker hot-loop the job.
func TestSecurityFailedWebhookPreservesRetryBookkeeping(t *testing.T) {
	store, db, owner := openStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	// Use a future true deadline so Schedule creates the advance reminder jobs.
	trueDeadline := time.Now().Add(48 * time.Hour)
	taskID := seedInstantTask(ctx, t, db, owner, trueDeadline)
	if err := store.Schedule(ctx, db, owner, taskID, trueDeadline); err != nil {
		t.Fatal(err)
	}
	jobID := jobIDForTask(ctx, t, db, taskID)
	if err := store.SetProviderEmailID(ctx, jobID, 1, "sec-email-failed"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE reminder_jobs
		SET delivery_state = 'leased', lease_token = 'sec-tok', retry_count = 2, retry_at = $1 WHERE id = $2`,
		time.Now().Add(time.Hour), jobID); err != nil {
		t.Fatal(err)
	}
	tr, ok := TransitionForEvent(resend.EventFailed)
	if !ok || !tr.ScheduleRetry {
		t.Fatalf("email.failed transition = %+v, %v", tr, ok)
	}
	applied, err := store.RecordWebhookEvent(ctx, "sec-evt-failed-1", "sec-email-failed", "email.failed", tr)
	if err != nil || !applied {
		t.Fatalf("failed event = %v, %v", applied, err)
	}
	job, err := store.Get(ctx, owner, jobID)
	if err != nil || job.DeliveryState != StateFailed || job.RetryCount != 3 || job.RetryAt == nil {
		t.Fatalf("job after failed event = %+v, %v", job, err)
	}
	if delay := time.Until(*job.RetryAt); delay < 20*time.Second || delay > 5*time.Minute {
		t.Fatalf("retry delay = %v, want a bounded future backoff", delay)
	}

	// A different svix-id for the same failure cannot reset retries again.
	applied, err = store.RecordWebhookEvent(ctx, "sec-evt-failed-2", "sec-email-failed", "email.failed", tr)
	if err != nil || applied {
		t.Fatalf("repeated failed event = %v, %v", applied, err)
	}
	again, err := store.Get(ctx, owner, jobID)
	if err != nil || again.RetryCount != 3 || again.RetryAt == nil {
		t.Fatalf("retry bookkeeping changed after repeated failure: %+v, %v", again, err)
	}
}

// TestSecurityDuplicateAndOutOfOrderCallbacks covers several svix-ids and
// reordered events: dedup is persistent and delivery evidence is monotonic.
func TestSecurityDuplicateAndOutOfOrderCallbacks(t *testing.T) {
	store, db, owner := openStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	// Use a future true deadline so Schedule creates the advance reminder jobs.
	trueDeadline := time.Now().Add(48 * time.Hour)
	taskID := seedInstantTask(ctx, t, db, owner, trueDeadline)
	if err := store.Schedule(ctx, db, owner, taskID, trueDeadline); err != nil {
		t.Fatal(err)
	}
	jobID := jobIDForTask(ctx, t, db, taskID)
	if err := store.SetProviderEmailID(ctx, jobID, 1, "sec-email-order"); err != nil {
		t.Fatal(err)
	}
	delivered := Transition{State: StateDelivered, Note: "provider confirmed delivery"}
	if applied, err := store.RecordWebhookEvent(ctx, "sec-evt-order-1", "sec-email-order", "email.delivered", delivered); err != nil || !applied {
		t.Fatalf("delivered event = %v, %v", applied, err)
	}
	if applied, err := store.RecordWebhookEvent(ctx, "sec-evt-order-1", "sec-email-order", "email.delivered", delivered); err != nil || applied {
		t.Fatalf("duplicate svix-id = %v, %v", applied, err)
	}
	if applied, err := store.RecordWebhookEvent(ctx, "sec-evt-order-2", "sec-email-order", "email.sent",
		Transition{State: StateAccepted}); err != nil || applied {
		t.Fatalf("out-of-order sent event = %v, %v", applied, err)
	}
	if applied, err := store.RecordWebhookEvent(ctx, "sec-evt-order-3", "sec-email-order", "email.delivered", delivered); err != nil || applied {
		t.Fatalf("second delivered event = %v, %v", applied, err)
	}
	if n := securityLedgerCount(ctx, t, db, "sec-evt-order-1"); n != 1 {
		t.Fatalf("dedup ledger rows = %d, want 1", n)
	}
	job, err := store.Get(ctx, owner, jobID)
	if err != nil || job.DeliveryState != StateDelivered {
		t.Fatalf("job = %+v, %v", job, err)
	}
}

// TestSecurityOwnerIsolationAndCrossOwnerConflict covers owner-scoped task
// lookup, cross-owner reactivation and cross-owner callbacks together.
func TestSecurityOwnerIsolationAndCrossOwnerConflict(t *testing.T) {
	store, db, ownerA := openStore(t)
	ownerB := randomOwner(t)
	registerCleanup(t, db, ownerB)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	// Use a future true deadline so Schedule creates the advance reminder jobs.
	trueDeadline := time.Now().Add(48 * time.Hour)
	taskA := seedInstantTask(ctx, t, db, ownerA, trueDeadline)
	if err := store.Schedule(ctx, db, ownerA, taskA, trueDeadline); err != nil {
		t.Fatal(err)
	}
	jobA := jobIDForTask(ctx, t, db, taskA)
	if err := store.Schedule(ctx, db, ownerB, taskA, trueDeadline.Add(time.Hour)); err == nil {
		t.Fatal("cross-owner Schedule must be rejected")
	}
	job, err := store.Get(ctx, ownerA, jobA)
	if err != nil || job.UserID != ownerA || job.DeliveryVersion != 1 {
		t.Fatalf("job after cross-owner schedule = %+v, %v", job, err)
	}
	wB := NewWorker(store, Config{Scope: []string{ownerB}})
	if applied, err := wB.RecordCallback(ctx, ownerB, jobA, OutcomeDelivered); err != nil || applied {
		t.Fatalf("cross-owner callback = %v, %v", applied, err)
	}

	// A job owned by A that references a task owned by B must be suppressed,
	// never dispatched with B's title.
	taskB := seedInstantTask(ctx, t, db, ownerB, trueDeadline)
	var badID string
	// Set due_at to past so worker will claim it
	past := time.Now().Add(-time.Hour)
	if err := db.QueryRowContext(ctx, `INSERT INTO reminder_jobs
		(user_id, task_id, due_at, delivery_version) VALUES ($1, $2, $3, 1) RETURNING id`, ownerA, taskB, past).
		Scan(&badID); err != nil {
		t.Fatal(err)
	}
	disp := &fakeDispatcher{outcome: OutcomeDelivered}
	wA := NewWorker(store, Config{Dispatcher: disp, Scope: []string{ownerA}, BatchSize: 10})
	if _, err := wA.poll(ctx, 10); err != nil {
		t.Fatal(err)
	}
	if disp.count() != 0 {
		t.Fatal("worker dispatched a job whose task belongs to another owner")
	}
	bad, err := store.Get(ctx, ownerA, badID)
	if err != nil || bad.DeliveryState != StateSuppressed || bad.SuppressReason != SuppressDeleted {
		t.Fatalf("cross-owner task job = %+v, %v", bad, err)
	}
}

// TestSecurityStaleVersionMappingIsRejected blocks a stale dispatch from
// writing a provider mapping for a version it does not own.
func TestSecurityStaleVersionMappingIsRejected(t *testing.T) {
	store, db, owner := openStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	// Use a future true deadline so Schedule creates the advance reminder jobs.
	trueDeadline := time.Now().Add(48 * time.Hour)
	taskID := seedInstantTask(ctx, t, db, owner, trueDeadline)
	if err := store.Schedule(ctx, db, owner, taskID, trueDeadline); err != nil {
		t.Fatal(err)
	}
	// Get the 2h reminder job and set its due_at to the past so it's claimable.
	jobID := jobIDForTaskTiming(ctx, t, db, taskID, Timing2hBefore)
	if jobID == "" {
		t.Fatal("expected 2h job")
	}
	past := time.Now().Add(-time.Hour)
	setJobDueAt(ctx, t, db, jobID, past)
	if _, err := store.ClaimDue(ctx, past, 5, "sec-tok", past.Add(time.Minute), []string{owner}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetProviderEmailID(ctx, jobID, 2, "sec-email-stale"); err == nil {
		t.Fatal("stale version mapping must be rejected")
	}
	job, err := store.Get(ctx, owner, jobID)
	if err != nil || job.ProviderEmailID != "" {
		t.Fatalf("stale mapping wrote to job: %+v, %v", job, err)
	}
}

// TestSecurityNoDispatcherNeverLeasesOrSends pins the no-send default: an
// unconfigured worker claims nothing, sends nothing and exhausts nothing.
func TestSecurityNoDispatcherNeverLeasesOrSends(t *testing.T) {
	store, db, owner := openStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	// Use a future true deadline so Schedule creates the advance reminder jobs.
	trueDeadline := time.Now().Add(48 * time.Hour)
	taskID := seedInstantTask(ctx, t, db, owner, trueDeadline)
	if err := store.Schedule(ctx, db, owner, taskID, trueDeadline); err != nil {
		t.Fatal(err)
	}
	// Get the 2h reminder job and set its due_at to the past so it's claimable.
	jobID := jobIDForTaskTiming(ctx, t, db, taskID, Timing2hBefore)
	if jobID == "" {
		t.Fatal("expected 2h job")
	}
	past := time.Now().Add(-time.Hour)
	setJobDueAt(ctx, t, db, jobID, past)
	w := NewWorker(store, Config{Scope: []string{owner}})
	if w.Ready() {
		t.Fatal("worker must not be ready without a dispatcher")
	}
	if n, err := w.poll(ctx, 5); err != nil || n != 0 {
		t.Fatalf("poll = %d, %v", n, err)
	}
	job, err := store.Get(ctx, owner, jobID)
	if err != nil || job.DeliveryState != StateScheduled || job.LeaseToken != "" || job.RetryCount != 0 {
		t.Fatalf("job after no-dispatcher poll = %+v, %v", job, err)
	}
	if _, err := store.GetByProviderEmail(ctx, "sec-email-never"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unexpected provider mapping: %v", err)
	}
}
