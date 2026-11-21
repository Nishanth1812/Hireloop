package tasks

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
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

func TestPostgresLegacyAmbiguousInstantCanBeUpdated(t *testing.T) {
	store, db, owner := openIntegrationStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	appID := seedApplication(ctx, t, db, owner)
	var id string
	err := db.QueryRowContext(ctx, `INSERT INTO tasks (user_id, application_id, title, status, due_precision, due_at, time_zone)
		VALUES ($1, $2, 'Legacy task', 'open', 'instant', '2026-11-01T05:30:00.123456Z', 'America/New_York') RETURNING id`, owner, appID).Scan(&id)
	if err != nil {
		t.Fatalf("seed legacy task: %v", err)
	}
	status := StatusDone
	updated, err := store.Patch(ctx, owner, id, PatchInput{Status: &status})
	if err != nil || updated.Status != StatusDone || updated.DueAt != "2026-11-01T05:30:00.123456Z" {
		t.Fatalf("status update: %+v, %v", updated, err)
	}
	title, precision, local, zone := "Updated", PrecisionInstant, "2026-11-01T01:30", "America/New_York"
	updated, err = store.Patch(ctx, owner, id, PatchInput{Title: &title, DuePrecision: &precision, DueLocal: &local, TimeZone: &zone})
	if err != nil || updated.Title != title || updated.DueAt != "2026-11-01T05:30:00.123456Z" {
		t.Fatalf("unchanged legacy deadline: %+v, %v", updated, err)
	}
	local = "2026-11-01T01:45"
	if _, err := store.Patch(ctx, owner, id, PatchInput{DueLocal: &local}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("new ambiguous deadline: got %v, want ErrInvalid", err)
	}
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
	if err != nil || len(open.Items) != 0 || open.Total != 0 {
		t.Fatalf("open actions after completion = %+v, err = %v", open, err)
	}
	all, err := store.List(ctx, owner, Filter{ShowAll: true})
	if err != nil || len(all.Items) != 2 || all.Total != 2 {
		t.Fatalf("all tasks after completion = %+v, err = %v", all, err)
	}
	for _, task := range all.Items {
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
	if err != nil || len(listB.Items) != 0 || listB.Total != 0 {
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
	if len(list.Items) != len(want) {
		t.Fatalf("list length = %d, want %d", len(list.Items), len(want))
	}
	for i := range want {
		if list.Items[i].ID != want[i] {
			t.Fatalf("order[%d] = %s (%s), want %s", i, list.Items[i].ID, list.Items[i].Title, want[i])
		}
	}
}

// walkPages follows next_cursor to the end, refusing to loop forever if a
// broken cursor ever repeated a position.
func walkPages(t *testing.T, store PostgresStore, ctx context.Context, owner string, filter Filter) ([]Task, ListResult) {
	t.Helper()
	seen := map[string]bool{}
	all := []Task{}
	last := ListResult{}
	var err error
	for page := 0; page < 100; page++ {
		last, err = store.List(ctx, owner, filter)
		if err != nil {
			t.Fatalf("list page %d: %v", page, err)
		}
		if len(last.Items) > last.PageSize {
			t.Fatalf("page %d returned %d rows, more than page_size %d", page, len(last.Items), last.PageSize)
		}
		for _, task := range last.Items {
			if seen[task.ID] {
				t.Fatalf("page %d repeated task %s", page, task.ID)
			}
			seen[task.ID] = true
			all = append(all, task)
		}
		if last.NextCursor == "" {
			return all, last
		}
		cursor, err := filter.decodeCursor(last.NextCursor)
		if err != nil {
			t.Fatalf("decode cursor: %v", err)
		}
		filter.After = &cursor
	}
	t.Fatal("pagination did not terminate")
	return nil, ListResult{}
}

func TestPostgresTaskListIsBoundedAndContinues(t *testing.T) {
	store, db, owner := openIntegrationStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	appID := seedApplication(ctx, t, db, owner)

	want := []string{}
	for i := 0; i < 5; i++ {
		task, err := store.Create(ctx, owner, Input{
			ApplicationID: appID, Title: fmt.Sprintf("Task %d", i),
			DuePrecision: PrecisionDate, DueDate: fmt.Sprintf("2026-12-%02d", i+1),
		})
		if err != nil {
			t.Fatalf("create task %d: %v", i, err)
		}
		want = append(want, task.ID)
	}
	filter := Filter{PageSize: 2}
	items, last := walkPages(t, store, ctx, owner, filter)
	if last.Total != 5 || len(items) != 5 {
		t.Fatalf("walked %d of %d tasks", len(items), last.Total)
	}
	for i := range want {
		if items[i].ID != want[i] {
			t.Fatalf("walked order[%d] = %s, want %s", i, items[i].ID, want[i])
		}
	}
	// A cursor past the last row is an empty page with no continuation.
	filter.After = &Cursor{Day: "2026-12-05", At: noInstantAt,
		CreatedAt: time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC), ID: "00000000-0000-4000-8000-000000000000"}
	past, err := store.List(ctx, owner, filter)
	if err != nil || len(past.Items) != 0 || past.NextCursor != "" || past.Total != 5 {
		t.Fatalf("past-last page = %+v, err = %v", past, err)
	}
}

func TestPostgresTaskListTiesAreDeterministicAcrossPages(t *testing.T) {
	store, db, owner := openIntegrationStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	appID := seedApplication(ctx, t, db, owner)

	// Identical deadline day and created_at leave id as the only tie-break.
	// Inserting under the random test owner never touches another account.
	fixed := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	want := []string{}
	for i := 0; i < 5; i++ {
		var id string
		err := db.QueryRowContext(ctx, `INSERT INTO tasks
			(user_id, application_id, title, due_precision, due_date, created_at)
			VALUES ($1, $2, $3, 'date', '2026-12-01', $4) RETURNING id`,
			owner, appID, fmt.Sprintf("Tie %d", i), fixed).Scan(&id)
		if err != nil {
			t.Fatalf("seed tied task: %v", err)
		}
		want = append(want, id)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(want)))
	items, last := walkPages(t, store, ctx, owner, Filter{PageSize: 2})
	if last.Total != 5 || len(items) != 5 {
		t.Fatalf("walked %d of %d tied tasks", len(items), last.Total)
	}
	for i := range want {
		if items[i].ID != want[i] {
			t.Fatalf("tie order[%d] = %s, want %s", i, items[i].ID, want[i])
		}
	}
}

func TestPostgresTaskListOwnerIsolationAcrossPages(t *testing.T) {
	store, db, ownerA := openIntegrationStore(t)
	ownerB := randomOwner(t)
	cleanupOwner(t, db, ownerB)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	appA := seedApplication(ctx, t, db, ownerA)
	appB := seedApplication(ctx, t, db, ownerB)

	var bID string
	if err := db.QueryRowContext(ctx, `INSERT INTO tasks
		(user_id, application_id, title, due_precision, due_date)
		VALUES ($1, $2, 'B secret', 'date', '2026-01-01') RETURNING id`, ownerB, appB).Scan(&bID); err != nil {
		t.Fatalf("seed B task: %v", err)
	}
	for i := 0; i < 5; i++ {
		if _, err := store.Create(ctx, ownerA, Input{
			ApplicationID: appA, Title: fmt.Sprintf("A %d", i),
			DuePrecision: PrecisionDate, DueDate: fmt.Sprintf("2026-12-%02d", i+1),
		}); err != nil {
			t.Fatalf("seed A task: %v", err)
		}
	}
	items, last := walkPages(t, store, ctx, ownerA, Filter{PageSize: 2})
	if len(items) != 5 || last.Total != 5 {
		t.Fatalf("A walked %d of %d", len(items), last.Total)
	}
	for _, task := range items {
		if task.ID == bID {
			t.Fatal("A's pages leaked B's task")
		}
	}
	onlyB, err := store.List(ctx, ownerB, Filter{ShowAll: true, PageSize: 2})
	if err != nil || len(onlyB.Items) != 1 || onlyB.Total != 1 || onlyB.NextCursor != "" {
		t.Fatalf("B list = %+v, err = %v", onlyB, err)
	}
	if onlyB.Items[0].ID != bID {
		t.Fatal("B's list did not return B's own task")
	}
}

func TestPostgresTaskCreateRejectsAmbiguousLocalTimes(t *testing.T) {
	store, db, owner := openIntegrationStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	appID := seedApplication(ctx, t, db, owner)

	for _, tc := range []struct{ name, local, zone string }{
		{"one-hour overlap", "2026-11-01T01:30", "America/New_York"},
		{"half-hour overlap", "2026-04-05T01:45", "Australia/Lord_Howe"},
		{"one-hour gap", "2026-03-08T02:30", "America/New_York"},
		{"half-hour gap", "2026-10-04T02:15", "Australia/Lord_Howe"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := store.Create(ctx, owner, Input{
				ApplicationID: appID, Title: "Interview", DuePrecision: PrecisionInstant,
				DueLocal: tc.local, TimeZone: tc.zone,
			})
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("%s %s: err = %v, want ErrInvalid", tc.local, tc.zone, err)
			}
		})
	}
	// A valid unambiguous instant in a non-hour-shift zone still round-trips.
	valid, err := store.Create(ctx, owner, Input{
		ApplicationID: appID, Title: "Interview", DuePrecision: PrecisionInstant,
		DueLocal: "2026-11-12T18:00", TimeZone: "Asia/Kolkata",
	})
	if err != nil || valid.DueAt != "2026-11-12T12:30:00Z" || valid.DueLocal != "2026-11-12T18:00" {
		t.Fatalf("valid instant = %+v, err = %v", valid, err)
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

func TestPostgresTaskIcsExportRoundTrip(t *testing.T) {
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
	dateExport, err := ExportIcs(dateTask)
	if err != nil {
		t.Fatalf("ExportIcs date task: %v", err)
	}
	if !strings.Contains(dateExport, "DUE;VALUE=DATE:20261112") || strings.Contains(dateExport, "TZID") {
		t.Fatalf("date export = %q, want DUE;VALUE=DATE:20261112 with no zone", dateExport)
	}

	instantTask, err := store.Create(ctx, owner, Input{
		ApplicationID: appID, Title: "Interview", DuePrecision: PrecisionInstant,
		DueLocal: "2026-11-12T18:00", TimeZone: "Asia/Kolkata",
	})
	if err != nil {
		t.Fatalf("create instant task: %v", err)
	}
	got, err := store.Get(ctx, owner, instantTask.ID)
	if err != nil {
		t.Fatalf("get instant task: %v", err)
	}
	export, err := ExportIcs(got)
	if err != nil {
		t.Fatalf("ExportIcs stored instant task: %v", err)
	}
	if !strings.Contains(export, "DUE:20261112T123000Z") {
		t.Fatalf("export = %q, want the stored instant in UTC", export)
	}
	if !strings.Contains(export, "SUMMARY:Interview") {
		t.Fatalf("export = %q", export)
	}

	// Another owner cannot read the task, so its export is not reachable.
	other := randomOwner(t)
	cleanupOwner(t, db, other)
	if _, err := store.Get(ctx, other, instantTask.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other owner get = %v, want ErrNotFound", err)
	}
}
