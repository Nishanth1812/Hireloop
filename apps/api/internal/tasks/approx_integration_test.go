package tasks

import (
	"context"
	"strings"
	"testing"
	"time"

	"hireloop/api/internal/reminders"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// These SQL-backed tests are opt-in through HIRELOOP_TEST_DATABASE_URL and use
// only fresh random owners, cleaned up by openIntegrationStore. They exercise
// the create/edit/lifecycle invariants that the in-memory tests cannot prove:
// the job shape, the absence of an invented true_deadline, and suppression.

func approxJobsForTask(ctx context.Context, t *testing.T, store PostgresStore, owner, taskID string) []reminders.Job {
	t.Helper()
	rows, err := store.DB.QueryContext(ctx,
		`SELECT id FROM reminder_jobs WHERE user_id = $1 AND task_id = $2 ORDER BY reminder_timing`, owner, taskID)
	if err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan job id: %v", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("job rows: %v", err)
	}
	jobs := []reminders.Job{}
	for _, id := range ids {
		job, err := store.Jobs.Get(ctx, owner, id)
		if err != nil {
			t.Fatalf("get job %s: %v", id, err)
		}
		jobs = append(jobs, job)
	}
	return jobs
}

func jobByTiming(t *testing.T, jobs []reminders.Job, timing reminders.ReminderTiming) reminders.Job {
	t.Helper()
	for _, job := range jobs {
		if job.ReminderTiming == timing {
			return job
		}
	}
	t.Fatalf("no job with timing %s in %+v", timing, jobs)
	return reminders.Job{}
}

func TestPostgresApproxReminderLifecycle(t *testing.T) {
	store, _, owner := openIntegrationStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	appID := seedApplication(ctx, t, store.DB, owner)
	day := time.Now().AddDate(0, 0, 10).Format("2006-01-02")

	created, err := store.Create(ctx, owner, Input{
		ApplicationID: appID, Title: "Submit form", DuePrecision: PrecisionDate,
		DueDate: day, ReminderZone: "Asia/Kolkata",
	})
	if err != nil {
		t.Fatalf("create date task: %v", err)
	}
	if created.ReminderZone != "Asia/Kolkata" || created.TimeZone != "" || created.DueAt != "" {
		t.Fatalf("created task = %+v, want zone only in reminder_zone", created)
	}
	ics, err := ExportIcs(created)
	if err != nil || icsLine(t, ics, "DUE") != "DUE;VALUE=DATE:"+dayToIcs(day) {
		t.Fatalf("date export = %q, err = %v", ics, err)
	}

	jobs := approxJobsForTask(ctx, t, store, owner, created.ID)
	if len(jobs) != 2 {
		t.Fatalf("jobs = %+v, want two approximate reminders", jobs)
	}
	want := []reminders.ReminderTiming{reminders.TimingDayBeforeApprox, reminders.TimingMorningOfApprox}
	slots := reminders.DateApproxSlots(day, "Asia/Kolkata", time.Now())
	if len(slots) != 2 {
		t.Fatalf("computed slots = %+v", slots)
	}
	for i, timing := range want {
		job := jobByTiming(t, jobs, timing)
		if job.DueDay != day || job.ReminderZone != "Asia/Kolkata" || !job.TrueDeadline.IsZero() {
			t.Errorf("job %s = %+v, want day/zone and no true deadline", timing, job)
		}
		if !job.DueAt.Equal(slots[i].DueAt) {
			t.Errorf("job %s due at %v, want %v", timing, job.DueAt, slots[i].DueAt)
		}
		if job.DeliveryState != reminders.StateScheduled || job.DeliveryVersion != 1 {
			t.Errorf("job %s state = %s v%d", timing, job.DeliveryState, job.DeliveryVersion)
		}
	}

	// Completion suppresses every unsent approximate job; finishing never
	// claims a reminder as delivered.
	if _, err := store.Patch(ctx, owner, created.ID, PatchInput{Status: ptr(StatusDone)}); err != nil {
		t.Fatalf("complete task: %v", err)
	}
	for _, job := range approxJobsForTask(ctx, t, store, owner, created.ID) {
		if job.DeliveryState != reminders.StateSuppressed || job.SuppressReason != reminders.SuppressCompleted {
			t.Errorf("completed job = %+v", job)
		}
	}

	// A reschedule while open reactivates the pair with the new calendar day
	// and a fresh delivery version, so old provider events cannot match.
	newDay := time.Now().AddDate(0, 0, 14).Format("2006-01-02")
	if _, err := store.Patch(ctx, owner, created.ID, PatchInput{
		DueDate: ptr(newDay), Status: ptr(StatusOpen),
	}); err != nil {
		t.Fatalf("reschedule task: %v", err)
	}
	rescheduled := approxJobsForTask(ctx, t, store, owner, created.ID)
	if len(rescheduled) != 2 {
		t.Fatalf("rescheduled jobs = %+v, want two", rescheduled)
	}
	for _, job := range rescheduled {
		if job.DueDay != newDay || job.DeliveryVersion != 2 {
			t.Errorf("rescheduled job = %+v, want new day and version 2", job)
		}
	}

	// Switching to an exact deadline replaces the approximate pair with the
	// two advance jobs carrying a true deadline.
	instant := time.Now().AddDate(0, 0, 15).Format("2006-01-02") + "T09:00"
	if _, err := store.Patch(ctx, owner, created.ID, PatchInput{
		DuePrecision: ptr(PrecisionInstant), DueLocal: ptr(instant), TimeZone: ptr("UTC"),
	}); err != nil {
		t.Fatalf("switch to instant: %v", err)
	}
	for _, job := range approxJobsForTask(ctx, t, store, owner, created.ID) {
		if job.ReminderTiming == reminders.TimingDayBeforeApprox || job.ReminderTiming == reminders.TimingMorningOfApprox {
			t.Errorf("approximate job survived precision switch: %+v", job)
		}
		if job.TrueDeadline.IsZero() || job.DueDay != "" {
			t.Errorf("exact job = %+v, want a true deadline and no due day", job)
		}
	}

	// Clearing the deadline suppresses everything.
	if _, err := store.Patch(ctx, owner, created.ID, PatchInput{DuePrecision: ptr(PrecisionNone)}); err != nil {
		t.Fatalf("clear deadline: %v", err)
	}
	for _, job := range approxJobsForTask(ctx, t, store, owner, created.ID) {
		if job.DeliveryState != reminders.StateSuppressed || job.SuppressReason != reminders.SuppressRescheduled {
			t.Errorf("cleared job = %+v", job)
		}
	}
}

func TestPostgresApproxReminderNeedsBrowserZone(t *testing.T) {
	store, _, owner := openIntegrationStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	appID := seedApplication(ctx, t, store.DB, owner)
	day := time.Now().AddDate(0, 0, 10).Format("2006-01-02")

	created, err := store.Create(ctx, owner, Input{
		ApplicationID: appID, Title: "No zone", DuePrecision: PrecisionDate, DueDate: day,
	})
	if err != nil {
		t.Fatalf("create zone-less date task: %v", err)
	}
	if jobs := approxJobsForTask(ctx, t, store, owner, created.ID); len(jobs) != 0 {
		t.Fatalf("zone-less date got jobs %+v", jobs)
	}

	// An old date-only task can gain approximate reminders when the browser
	// supplies a zone on edit, and lose them when it is removed again.
	if _, err := store.Patch(ctx, owner, created.ID, PatchInput{ReminderZone: ptr("UTC")}); err != nil {
		t.Fatalf("add zone: %v", err)
	}
	if jobs := approxJobsForTask(ctx, t, store, owner, created.ID); len(jobs) != 2 {
		t.Fatalf("jobs after adding zone = %+v, want two", jobs)
	}
	if _, err := store.Patch(ctx, owner, created.ID, PatchInput{ReminderZone: ptr("")}); err != nil {
		t.Fatalf("remove zone: %v", err)
	}
	for _, job := range approxJobsForTask(ctx, t, store, owner, created.ID) {
		if job.DeliveryState != reminders.StateSuppressed {
			t.Errorf("job after removing zone = %+v", job)
		}
	}
}

func TestPostgresApproxReminderSkipsPastDates(t *testing.T) {
	store, _, owner := openIntegrationStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	appID := seedApplication(ctx, t, store.DB, owner)
	past := time.Now().AddDate(0, 0, -2).Format("2006-01-02")
	created, err := store.Create(ctx, owner, Input{
		ApplicationID: appID, Title: "Old", DuePrecision: PrecisionDate,
		DueDate: past, ReminderZone: "Asia/Kolkata",
	})
	if err != nil {
		t.Fatalf("create past date task: %v", err)
	}
	if jobs := approxJobsForTask(ctx, t, store, owner, created.ID); len(jobs) != 0 {
		t.Fatalf("past date got jobs %+v", jobs)
	}
}

func dayToIcs(day string) string { return strings.ReplaceAll(day, "-", "") }
