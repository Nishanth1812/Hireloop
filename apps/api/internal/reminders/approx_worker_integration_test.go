package reminders

import (
	"context"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// TestWorkerApproxReminderGuard exercises the full claim/dispatch path for an
// approximate job: same-local-day jobs dispatch with the day/zone carried into
// the notification, while jobs claimed on a later local day are suppressed
// with no invented true deadline.
func TestWorkerApproxReminderGuard(t *testing.T) {
	store, db, owner := openStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var appID, taskID string
	if err := db.QueryRowContext(ctx,
		`INSERT INTO applications (user_id, company, role, status) VALUES ($1, 'Acme', 'Engineer', 'saved') RETURNING id`,
		owner).Scan(&appID); err != nil {
		t.Fatalf("seed application: %v", err)
	}
	if err := db.QueryRowContext(ctx,
		`INSERT INTO tasks (user_id, application_id, title, status, due_precision, due_date, reminder_zone)
		 VALUES ($1, $2, 'Submit form', 'open', 'date', '2020-01-02', 'UTC') RETURNING id`,
		owner, appID).Scan(&taskID); err != nil {
		t.Fatalf("seed date task: %v", err)
	}

	slots := []ApproxSlot{
		{Timing: TimingDayBeforeApprox, DueAt: time.Date(2020, 1, 1, 18, 0, 0, 0, time.UTC), DueDay: "2020-01-02", Zone: "UTC"},
		{Timing: TimingMorningOfApprox, DueAt: time.Date(2020, 1, 2, 9, 0, 0, 0, time.UTC), DueDay: "2020-01-02", Zone: "UTC"},
	}
	if err := store.ScheduleApprox(ctx, db, owner, taskID, slots); err != nil {
		t.Fatalf("schedule approximate jobs: %v", err)
	}

	dispatcher := &fakeDispatcher{outcome: OutcomeAccepted}
	worker := NewWorker(store, Config{
		Dispatcher: dispatcher,
		Now:        func() time.Time { return time.Date(2020, 1, 2, 10, 0, 0, 0, time.UTC) },
		BatchSize:  10,
	})
	if _, err := worker.poll(ctx, 10); err != nil {
		t.Fatalf("poll on due day: %v", err)
	}
	if got := dispatcher.count(); got != 2 {
		t.Fatalf("dispatches on the due day = %d, want 2", got)
	}
	for _, call := range dispatcher.calls {
		if call.DueDay != "2020-01-02" || call.ReminderZone != "UTC" || !call.TrueDeadline.IsZero() {
			t.Errorf("notification = %+v, want day/zone and no true deadline", call)
		}
		body := buildReminderText(call.Title, call.TrueDeadline, call.ReminderTiming, call.DueDay, call.ReminderZone)
		if !contains(body, "approximate") || !contains(body, "not a guaranteed cutoff") {
			t.Errorf("approximate label missing: %s", body)
		}
	}

	// A fresh task claimed on a later local day must be suppressed, not sent.
	var taskID2 string
	if err := db.QueryRowContext(ctx,
		`INSERT INTO tasks (user_id, application_id, title, status, due_precision, due_date, reminder_zone)
		 VALUES ($1, $2, 'Old form', 'open', 'date', '2020-01-02', 'UTC') RETURNING id`,
		owner, appID).Scan(&taskID2); err != nil {
		t.Fatalf("seed second date task: %v", err)
	}
	if err := store.ScheduleApprox(ctx, db, owner, taskID2, slots); err != nil {
		t.Fatalf("schedule second task: %v", err)
	}
	late := NewWorker(store, Config{
		Dispatcher: dispatcher,
		Now:        func() time.Time { return time.Date(2020, 1, 3, 10, 0, 0, 0, time.UTC) },
		BatchSize:  10,
	})
	if _, err := late.poll(ctx, 10); err != nil {
		t.Fatalf("poll after due day: %v", err)
	}
	if got := dispatcher.count(); got != 2 {
		t.Fatalf("dispatches after due day = %d, want none beyond the first two", got)
	}
	for _, id := range jobIDsForTask(ctx, t, db, taskID2) {
		job, err := store.Get(ctx, owner, id)
		if err != nil {
			t.Fatalf("get late job: %v", err)
		}
		if job.DeliveryState != StateSuppressed || job.SuppressReason != SuppressDeadlinePassed {
			t.Errorf("late job = %+v, want suppressed/deadline_passed", job)
		}
	}
}
