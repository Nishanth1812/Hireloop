package reminders

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"hireloop/api/internal/resend"
)

// Opt-in real-Postgres tests for the Resend wiring: set
// HIRELOOP_TEST_DATABASE_URL to run them; otherwise they skip. Every test
// writes only rows under its fresh random owner and deletes exactly those
// rows in cleanup (jobs, tasks, applications, notification settings, events).

// scriptedDispatcher returns the queued outcomes in order and records calls.
type scriptedDispatcher struct {
	mu       sync.Mutex
	outcomes []Outcome
	calls    []Notification
}

func (d *scriptedDispatcher) Send(_ context.Context, n Notification) Outcome {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls = append(d.calls, n)
	if len(d.outcomes) == 0 {
		return OutcomeFailed
	}
	outcome := d.outcomes[0]
	d.outcomes = d.outcomes[1:]
	return outcome
}

func (d *scriptedDispatcher) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.calls)
}

func TestWorkerTimeoutProducesUncertainAndRetries(t *testing.T) {
	store, db, owner := openStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	past := time.Now().Add(-time.Minute)
	taskID := seedInstantTask(ctx, t, db, owner, past)
	if err := store.Schedule(ctx, db, owner, taskID, past); err != nil {
		t.Fatal(err)
	}
	jobID := jobIDForTask(ctx, t, db, taskID)

	disp := &scriptedDispatcher{outcomes: []Outcome{OutcomeUncertain, OutcomeAccepted}}
	w := NewWorker(store, Config{Dispatcher: disp, Scope: []string{owner}, BatchSize: 5})
	if _, err := w.poll(ctx, 5); err != nil {
		t.Fatal(err)
	}
	job, err := store.Get(ctx, owner, jobID)
	if err != nil || job.DeliveryState != StateUncertain || job.RetryAt == nil {
		t.Fatalf("job after ambiguous timeout = %+v, %v", job, err)
	}
	// The retry is not due yet: nothing dispatches.
	if _, err := w.poll(ctx, 5); err != nil || disp.count() != 1 {
		t.Fatalf("poll before retry: %v, dispatches = %d", err, disp.count())
	}
	// Push the retry into the past: the same job is re-claimed and, this
	// time, accepted.
	if _, err := db.ExecContext(ctx, `UPDATE reminder_jobs SET retry_at = $1 WHERE id = $2`, time.Now().Add(-time.Second), jobID); err != nil {
		t.Fatal(err)
	}
	if _, err := w.poll(ctx, 5); err != nil || disp.count() != 2 {
		t.Fatalf("poll after retry push: %v, dispatches = %d", err, disp.count())
	}
	job, err = store.Get(ctx, owner, jobID)
	if err != nil || job.DeliveryState != StateAccepted {
		t.Fatalf("job after retry = %+v, %v", job, err)
	}
	for _, n := range disp.calls {
		if n.ID != jobID {
			t.Fatalf("notification identity changed: %s", n.ID)
		}
	}
}

func TestWorkerPermanentFailureIsTerminalAndBlocksCallbacks(t *testing.T) {
	store, db, owner := openStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	past := time.Now().Add(-time.Minute)
	taskID := seedInstantTask(ctx, t, db, owner, past)
	if err := store.Schedule(ctx, db, owner, taskID, past); err != nil {
		t.Fatal(err)
	}
	jobID := jobIDForTask(ctx, t, db, taskID)

	disp := &scriptedDispatcher{outcomes: []Outcome{OutcomePermanent}}
	w := NewWorker(store, Config{Dispatcher: disp, Scope: []string{owner}, BatchSize: 5})
	if _, err := w.poll(ctx, 5); err != nil {
		t.Fatal(err)
	}
	job, err := store.Get(ctx, owner, jobID)
	if err != nil || job.DeliveryState != StatePermanentFailed {
		t.Fatalf("job after permanent failure = %+v, %v", job, err)
	}
	// A late provider callback cannot overwrite the terminal state.
	applied, err := store.RecordWebhookEvent(ctx, "evt-late", "email-perm", "email.delivered",
		Transition{State: StateDelivered, Note: "provider confirmed delivery"})
	if err != nil || applied {
		t.Fatalf("callback on terminal job = %v, %v", applied, err)
	}
}

func TestResendWebhookEventsApplyMonotonicallyAndClearOnReschedule(t *testing.T) {
	store, db, owner := openStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	past := time.Now().Add(-time.Minute)
	taskID := seedInstantTask(ctx, t, db, owner, past)
	if err := store.Schedule(ctx, db, owner, taskID, past); err != nil {
		t.Fatal(err)
	}
	jobID := jobIDForTask(ctx, t, db, taskID)
	job, err := store.Get(ctx, owner, jobID)
	if err != nil {
		t.Fatal(err)
	}
	if job.DeliveryVersion != 1 {
		t.Fatalf("first schedule version = %d, want 1", job.DeliveryVersion)
	}
	if err := store.SetProviderEmailID(ctx, jobID, job.DeliveryVersion, "email-v1"); err != nil {
		t.Fatal(err)
	}

	// First event for version 1: delivery confirmed.
	applied, err := store.RecordWebhookEvent(ctx, "evt-1", "email-v1", "email.delivered",
		Transition{State: StateDelivered, Note: "provider confirmed delivery"})
	if err != nil || !applied {
		t.Fatalf("delivered event = %v, %v", applied, err)
	}
	job, err = store.Get(ctx, owner, jobID)
	if err != nil || job.DeliveryState != StateDelivered {
		t.Fatalf("job = %+v, %v", job, err)
	}

	// A replay of the same event is a no-op (persistent dedup).
	applied, err = store.RecordWebhookEvent(ctx, "evt-1", "email-v1", "email.delivered",
		Transition{State: StateDelivered, Note: "provider confirmed delivery"})
	if err != nil || applied {
		t.Fatalf("replay = %v, %v", applied, err)
	}
	// A weaker event cannot overwrite the stronger recorded state
	// (monotonic delivery evidence).
	applied, err = store.RecordWebhookEvent(ctx, "evt-2", "email-v1", "email.sent",
		Transition{State: StateAccepted, Note: "provider accepted the send request"})
	if err != nil || applied {
		t.Fatalf("weaker callback on delivered job = %v, %v", applied, err)
	}

	// Reschedule: the version bumps and the old provider email id is gone.
	if err := store.Schedule(ctx, db, owner, taskID, past.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	job, err = store.Get(ctx, owner, jobID)
	if err != nil {
		t.Fatal(err)
	}
	if job.DeliveryVersion != 2 || job.ProviderEmailID != "" {
		t.Fatalf("job after reschedule = %+v", job)
	}
	if _, err := store.GetByProviderEmail(ctx, "email-v1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old provider email still correlates: %v", err)
	}
	// Old callbacks for the old email id cannot overwrite the rescheduled job.
	applied, err = store.RecordWebhookEvent(ctx, "evt-3", "email-v1", "email.delivered",
		Transition{State: StateDelivered, Note: "old delivered"})
	if err != nil || applied {
		t.Fatalf("stale callback on rescheduled job = %v, %v", applied, err)
	}
	// The new version maps its own provider email id.
	if err := store.SetProviderEmailID(ctx, jobID, job.DeliveryVersion, "email-v2"); err != nil {
		t.Fatal(err)
	}
	provJob, err := store.GetByProviderEmail(ctx, "email-v2")
	if err != nil || provJob.ID != jobID {
		t.Fatalf("new provider email = %+v, %v", provJob, err)
	}
	applied, err = store.RecordWebhookEvent(ctx, "evt-4", "email-v2", "email.delivered",
		Transition{State: StateDelivered, Note: "provider confirmed delivery"})
	if err != nil || !applied {
		t.Fatalf("delivered event for new version = %v, %v", applied, err)
	}
}

func TestResendWebhookEventConcurrentDedup(t *testing.T) {
	store, db, owner := openStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	past := time.Now().Add(-time.Minute)
	taskID := seedInstantTask(ctx, t, db, owner, past)
	if err := store.Schedule(ctx, db, owner, taskID, past); err != nil {
		t.Fatal(err)
	}
	jobID := jobIDForTask(ctx, t, db, taskID)
	job, err := store.Get(ctx, owner, jobID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetProviderEmailID(ctx, jobID, job.DeliveryVersion, "email-concurrent"); err != nil {
		t.Fatal(err)
	}

	const attempts = 8
	applied := make([]bool, attempts)
	var wg sync.WaitGroup
	for i := range attempts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := store.RecordWebhookEvent(ctx, "evt-concurrent", "email-concurrent", "email.delivered",
				Transition{State: StateDelivered, Note: "provider confirmed delivery"})
			if err != nil {
				t.Errorf("record event: %v", err)
			}
			applied[i] = ok
		}()
	}
	wg.Wait()
	var wins int
	for _, ok := range applied {
		if ok {
			wins++
		}
	}
	if wins != 1 {
		t.Fatalf("concurrent dedup applied %d times, want 1", wins)
	}
	var ledger int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM reminder_email_events WHERE provider_event_id = 'evt-concurrent'`).Scan(&ledger); err != nil {
		t.Fatal(err)
	}
	if ledger != 1 {
		t.Fatalf("ledger rows = %d, want 1", ledger)
	}
}

func TestResendDispatcherWithRealStore(t *testing.T) {
	store, db, owner := openStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	past := time.Now().Add(-time.Minute)
	taskID := seedInstantTask(ctx, t, db, owner, past)
	if err := store.Schedule(ctx, db, owner, taskID, past); err != nil {
		t.Fatal(err)
	}
	jobID := jobIDForTask(ctx, t, db, taskID)

	sender := &captureSender{queue: []captureSenderEntry{{result: resend.Result{Status: resend.StatusAccepted, MessageID: "email-e2e"}}}}
	dispatcher := NewResendDispatcher(store, sender, "hireloop@example.com",
		func(_ context.Context, ownerID string) (string, error) {
			if ownerID != owner {
				t.Errorf("resolver owner = %q, want %q", ownerID, owner)
			}
			return "student@example.com", nil
		})
	w := NewWorker(store, Config{Dispatcher: dispatcher, Scope: []string{owner}, BatchSize: 5})
	if _, err := w.poll(ctx, 5); err != nil {
		t.Fatal(err)
	}
	job, err := store.Get(ctx, owner, jobID)
	if err != nil || job.DeliveryState != StateAccepted {
		t.Fatalf("job after dispatch = %+v, %v", job, err)
	}
	if sender.calls() != 1 || sender.keys[0] != fmt.Sprintf("job:%s:v%d", jobID, job.DeliveryVersion) {
		t.Fatalf("sender calls = %d, keys = %v", sender.calls(), sender.keys)
	}
	// The provider email id persisted through the real store correlates to
	// the job, so a provider callback can now be applied.
	provJob, err := store.GetByProviderEmail(ctx, "email-e2e")
	if err != nil || provJob.ID != jobID {
		t.Fatalf("provider email = %+v, %v", provJob, err)
	}
	applied, err := store.RecordWebhookEvent(ctx, "evt-e2e", "email-e2e", "email.delivered",
		Transition{State: StateDelivered, Note: "provider confirmed delivery"})
	if err != nil || !applied {
		t.Fatalf("callback = %v, %v", applied, err)
	}
	job, err = store.Get(ctx, owner, jobID)
	if err != nil || job.DeliveryState != StateDelivered {
		t.Fatalf("job after callback = %+v, %v", job, err)
	}
}

func TestWorkerDefaultNoSendWithoutDispatcher(t *testing.T) {
	store, db, owner := openStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	past := time.Now().Add(-time.Minute)
	taskID := seedInstantTask(ctx, t, db, owner, past)
	if err := store.Schedule(ctx, db, owner, taskID, past); err != nil {
		t.Fatal(err)
	}
	jobID := jobIDForTask(ctx, t, db, taskID)

	w := NewWorker(store, Config{Scope: []string{owner}, BatchSize: 5})
	if w.Ready() {
		t.Fatal("worker must not be ready without a dispatcher")
	}
	if n, err := w.poll(ctx, 5); err != nil || n != 0 {
		t.Fatalf("poll = %d, %v; a missing provider must not lease or exhaust jobs", n, err)
	}
	job, err := store.Get(ctx, owner, jobID)
	if err != nil || job.DeliveryState != StateScheduled || job.LeaseToken != "" || job.RetryCount != 0 {
		t.Fatalf("job = %+v, %v", job, err)
	}
	// Even with the job due, a missing provider still leases nothing and
	// never exhausts it.
	if _, err := db.ExecContext(ctx, `UPDATE reminder_jobs SET due_at = $1 WHERE id = $2`, past.Add(-time.Hour), jobID); err != nil {
		t.Fatal(err)
	}
	if n, err := w.poll(ctx, 5); err != nil || n != 0 {
		t.Fatalf("poll = %d, %v", n, err)
	}
	job, err = store.Get(ctx, owner, jobID)
	if err != nil || job.DeliveryState != StateScheduled {
		t.Fatalf("job = %+v, %v", job, err)
	}
}

func TestWebhookEventsRespectOwner(t *testing.T) {
	store, db, owner := openStore(t)
	ownerB := randomOwner(t)
	registerCleanup(t, db, ownerB)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	past := time.Now().Add(-time.Minute)
	taskID := seedInstantTask(ctx, t, db, owner, past)
	if err := store.Schedule(ctx, db, owner, taskID, past); err != nil {
		t.Fatal(err)
	}
	jobID := jobIDForTask(ctx, t, db, taskID)
	job, err := store.Get(ctx, owner, jobID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetProviderEmailID(ctx, jobID, job.DeliveryVersion, "email-owner"); err != nil {
		t.Fatal(err)
	}

	applied, err := store.RecordWebhookEvent(ctx, "evt-owner", "email-owner", "email.delivered",
		Transition{State: StateDelivered, Note: "provider confirmed delivery"})
	if err != nil || !applied {
		t.Fatalf("delivered event = %v, %v", applied, err)
	}
	// The job belongs to the owner: another owner sees not-found and cannot
	// act on it through the worker's owner-scoped callback path.
	if _, err := store.Get(ctx, ownerB, jobID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other owner Get = %v", err)
	}
	w := NewWorker(store, Config{Scope: []string{owner}})
	if applied, err := w.RecordCallback(ctx, ownerB, jobID, OutcomeFailed); err != nil || applied {
		t.Fatalf("other owner callback = %v, %v", applied, err)
	}
	job, err = store.Get(ctx, owner, jobID)
	if err != nil || job.DeliveryState != StateDelivered {
		t.Fatalf("job = %+v, %v", job, err)
	}
}
