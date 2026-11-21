package tasks

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"hireloop/api/db/migrations"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// Opt-in SQL-backed tests: set HIRELOOP_TEST_DATABASE_URL to a database used
// only for testing. Every row is written under a fresh random owner and deleted
// in cleanup; no other owner's data is read, written or reset.

func integrationDatabaseURL(t *testing.T) string {
	t.Helper()
	return os.Getenv("HIRELOOP_TEST_DATABASE_URL")
}

func randomOwner(t *testing.T) string {
	t.Helper()
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("generate owner id: %v", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func openIntegrationStore(t *testing.T) (PostgresStore, *sql.DB, string) {
	t.Helper()
	databaseURL := integrationDatabaseURL(t)
	if databaseURL == "" {
		t.Skip("set HIRELOOP_TEST_DATABASE_URL to run SQL-backed tests")
	}
	db, err := sql.Open("pgx", databaseURL)
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
	cleanupOwner(t, db, owner)
	return PostgresStore{DB: db}, db, owner
}

// cleanupOwner registers removal of every row a random test owner can create,
// including a second owner used by isolation tests. It never touches another
// owner's data.
func cleanupOwner(t *testing.T, db *sql.DB, owner string) {
	t.Helper()
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		if _, err := db.ExecContext(cleanupCtx, `DELETE FROM tasks WHERE user_id = $1`, owner); err != nil {
			t.Errorf("cleanup tasks for test owner: %v", err)
		}
		if _, err := db.ExecContext(cleanupCtx, `DELETE FROM applications WHERE user_id = $1`, owner); err != nil {
			t.Errorf("cleanup applications for test owner: %v", err)
		}
	})
}

func seedApplication(ctx context.Context, t *testing.T, db *sql.DB, owner string) string {
	t.Helper()
	var id string
	err := db.QueryRowContext(ctx,
		`INSERT INTO applications (user_id, company, role, status) VALUES ($1, 'Acme', 'Engineer', 'saved') RETURNING id`,
		owner).Scan(&id)
	if err != nil {
		t.Fatalf("seed application: %v", err)
	}
	return id
}

func TestPostgresTaskDeadlinePrecisionRoundTrip(t *testing.T) {
	store, db, owner := openIntegrationStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	appID := seedApplication(ctx, t, db, owner)

	dateTask, err := store.Create(ctx, owner, Input{
		ApplicationID: appID, Title: "Apply by date", DuePrecision: PrecisionDate, DueDate: "2026-11-12",
	})
	if err != nil {
		t.Fatalf("create date-only task: %v", err)
	}
	if dateTask.DuePrecision != PrecisionDate || dateTask.DueDate != "2026-11-12" ||
		dateTask.DueAt != "" || dateTask.DueLocal != "" || dateTask.TimeZone != "" {
		t.Fatalf("date-only task = %+v, want no time or zone", dateTask)
	}
	if dateTask.Status != StatusOpen || dateTask.ApplicationID != appID {
		t.Fatalf("new task should be open and linked: %+v", dateTask)
	}

	instantTask, err := store.Create(ctx, owner, Input{
		ApplicationID: appID, Title: "Interview", DuePrecision: PrecisionInstant,
		DueLocal: "2026-11-12T18:00", TimeZone: "Asia/Kolkata",
	})
	if err != nil {
		t.Fatalf("create instant task: %v", err)
	}
	if instantTask.DuePrecision != PrecisionInstant || instantTask.DueAt != "2026-11-12T12:30:00Z" ||
		instantTask.DueLocal != "2026-11-12T18:00" || instantTask.TimeZone != "Asia/Kolkata" ||
		instantTask.DueDate != "" {
		t.Fatalf("instant task = %+v", instantTask)
	}

	// Completion and cancellation persist through a fresh read and leave the
	// open actions view.
	done, err := store.Patch(ctx, owner, instantTask.ID, PatchInput{Status: ptr(StatusDone)})
	if err != nil || done.Status != StatusDone {
		t.Fatalf("complete task: %+v, err = %v", done, err)
	}
	cancelled, err := store.Patch(ctx, owner, dateTask.ID, PatchInput{Status: ptr(StatusCancelled)})
	if err != nil || cancelled.Status != StatusCancelled {
		t.Fatalf("cancel task: %+v, err = %v", cancelled, err)
	}
	open, err := store.List(ctx, owner, Filter{})
	if err != nil || len(open) != 0 {
		t.Fatalf("open actions after completion = %+v, err = %v", open, err)
	}
	all, err := store.List(ctx, owner, Filter{ShowAll: true})
	if err != nil || len(all) != 2 {
		t.Fatalf("all tasks after completion = %+v, err = %v", all, err)
	}
	for _, task := range all {
		if task.Status == StatusOpen {
			t.Fatalf("finished task still open after reload: %+v", task)
		}
		got, err := store.Get(ctx, owner, task.ID)
		if err != nil || got.Status != task.Status {
			t.Fatalf("reload %s = %+v, err = %v", task.ID, got, err)
		}
	}
}

func TestPostgresTaskDeadlineEditsSwitchPrecision(t *testing.T) {
	store, db, owner := openIntegrationStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	appID := seedApplication(ctx, t, db, owner)

	task, err := store.Create(ctx, owner, Input{
		ApplicationID: appID, Title: "Interview", DuePrecision: PrecisionInstant,
		DueLocal: "2026-11-12T18:00", TimeZone: "Asia/Kolkata",
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	// Instant -> date must not drag the old wall time or zone along.
	asDate, err := store.Patch(ctx, owner, task.ID, PatchInput{
		DuePrecision: ptr(PrecisionDate), DueDate: ptr("2026-12-01"),
	})
	if err != nil || asDate.DuePrecision != PrecisionDate || asDate.DueDate != "2026-12-01" ||
		asDate.DueAt != "" || asDate.DueLocal != "" || asDate.TimeZone != "" {
		t.Fatalf("switch to date = %+v, err = %v", asDate, err)
	}
	// Date -> no deadline clears every deadline column.
	none, err := store.Patch(ctx, owner, task.ID, PatchInput{DuePrecision: ptr(PrecisionNone)})
	if err != nil || none.DuePrecision != PrecisionNone || none.DueDate != "" || none.DueAt != "" {
		t.Fatalf("clear deadline = %+v, err = %v", none, err)
	}
	// Re-adding an instant works and keeps its original zone.
	again, err := store.Patch(ctx, owner, task.ID, PatchInput{
		DuePrecision: ptr(PrecisionInstant), DueLocal: ptr("2026-12-02T09:15"), TimeZone: ptr("America/New_York"),
	})
	if err != nil || again.DueAt != "2026-12-02T14:15:00Z" || again.DueLocal != "2026-12-02T09:15" ||
		again.TimeZone != "America/New_York" {
		t.Fatalf("switch back to instant = %+v, err = %v", again, err)
	}
}

func TestPostgresTasksAreOwnerScoped(t *testing.T) {
	store, db, ownerA := openIntegrationStore(t)
	ownerB := randomOwner(t)
	cleanupOwner(t, db, ownerB)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	appA := seedApplication(ctx, t, db, ownerA)
	appB := seedApplication(ctx, t, db, ownerB)

	taskA, err := store.Create(ctx, ownerA, Input{ApplicationID: appA, Title: "A task"})
	if err != nil {
		t.Fatalf("create A task: %v", err)
	}
	// B cannot read, change, or attach a task to A's application or task.
	if got, err := store.Get(ctx, ownerB, taskA.ID); !errors.Is(err, ErrNotFound) || got.ID != "" {
		t.Fatalf("B get A task = %+v, err = %v", got, err)
	}
	if got, err := store.Patch(ctx, ownerB, taskA.ID, PatchInput{Status: ptr(StatusDone)}); !errors.Is(err, ErrNotFound) || got.ID != "" {
		t.Fatalf("B patch A task = %+v, err = %v", got, err)
	}
	if _, err := store.Create(ctx, ownerB, Input{ApplicationID: appA, Title: "B guessing A's app"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("B linking A's application: err = %v, want not found", err)
	}
	listB, err := store.List(ctx, ownerB, Filter{ShowAll: true})
	if err != nil || len(listB) != 0 {
		t.Fatalf("B list = %+v, err = %v", listB, err)
	}
	// A's row is unchanged by every attempt above.
	got, err := store.Get(ctx, ownerA, taskA.ID)
	if err != nil || got.Status != StatusOpen || got.Title != "A task" {
		t.Fatalf("A task after B's attempts = %+v, err = %v", got, err)
	}
	// B can still use its own application.
	if _, err := store.Create(ctx, ownerB, Input{ApplicationID: appB, Title: "B task"}); err != nil {
		t.Fatalf("B create own task: %v", err)
	}
}

func TestPostgresTaskListOrdersSoonestDeadlineFirst(t *testing.T) {
	store, db, owner := openIntegrationStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	appID := seedApplication(ctx, t, db, owner)

	noDeadline, err := store.Create(ctx, owner, Input{ApplicationID: appID, Title: "No deadline"})
	if err != nil {
		t.Fatal(err)
	}
	late, err := store.Create(ctx, owner, Input{ApplicationID: appID, Title: "Late", DuePrecision: PrecisionDate, DueDate: "2026-12-20"})
	if err != nil {
		t.Fatal(err)
	}
	soon, err := store.Create(ctx, owner, Input{
		ApplicationID: appID, Title: "Soon", DuePrecision: PrecisionInstant,
		DueLocal: "2026-11-12T09:00", TimeZone: "UTC",
	})
	if err != nil {
		t.Fatal(err)
	}
	list, err := store.List(ctx, owner, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{soon.ID, late.ID, noDeadline.ID}
	if len(list) != len(want) {
		t.Fatalf("list length = %d, want %d", len(list), len(want))
	}
	for i := range want {
		if list[i].ID != want[i] {
			t.Fatalf("order[%d] = %s (%s), want %s", i, list[i].ID, list[i].Title, want[i])
		}
	}
}

func TestPostgresTaskShapeConstraintRejectsDateWithZone(t *testing.T) {
	_, db, owner := openIntegrationStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	appID := seedApplication(ctx, t, db, owner)

	// The API rejects this, but the database constraint is the backstop for
	// any write that bypasses it: a date-only deadline cannot carry a zone.
	_, err := db.ExecContext(ctx,
		`INSERT INTO tasks (user_id, application_id, title, due_precision, due_date, time_zone)
		 VALUES ($1, $2, 'Bad shape', 'date', '2026-11-12', 'Asia/Kolkata')`, owner, appID)
	if err == nil {
		t.Fatal("database accepted a date-only deadline with a timezone")
	}
}
