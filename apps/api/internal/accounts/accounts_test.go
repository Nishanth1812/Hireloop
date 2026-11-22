package accounts

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
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

// The SQL integration tests are opt-in and MUST only point at a freshly
// created disposable database. They never connect by default and use only
// synthetic owners and synthetic email addresses.
func testDB(t *testing.T) *sql.DB {
	t.Helper()
	url := os.Getenv("HIRELOOP_ACCOUNTS_TEST_DATABASE_URL")
	if url == "" {
		url = os.Getenv("HIRELOOP_TEST_DATABASE_URL")
	}
	if url == "" {
		t.Skip("set HIRELOOP_ACCOUNTS_TEST_DATABASE_URL to a disposable database to run account SQL tests")
	}
	db, err := sql.Open("pgx", url)
	if err != nil {
		t.Fatalf("open disposable database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("connect to disposable database: %v", err)
	}
	if _, err := db.ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS auth;
		CREATE TABLE IF NOT EXISTS auth.users (id uuid PRIMARY KEY, email text NOT NULL UNIQUE);`); err != nil {
		t.Fatalf("prepare synthetic auth schema: %v", err)
	}
	if err := migrations.Apply(db); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	return db
}

type fixture struct {
	subject    string
	supabaseID string
	appID      string
	taskID     string
	jobID      string
	emailID    string
	owners     []string
}

func seedOwner(t *testing.T, ctx context.Context, db *sql.DB, label string, many bool) fixture {
	t.Helper()
	suffix := fmt.Sprintf("%s_%d", label, time.Now().UTC().UnixNano())
	f := fixture{
		subject:    "clerk_" + suffix,
		supabaseID: newUUID(t),
	}
	f.owners = []string{f.subject, f.supabaseID}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO auth.users (id, email) VALUES ($1, $2)`, f.supabaseID, suffix+"@example.test"); err != nil {
		t.Fatalf("seed auth user: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO user_identities (clerk_subject, supabase_user_id) VALUES ($1, $2)`, f.subject, f.supabaseID); err != nil {
		t.Fatalf("seed identity: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		// Direct SQL cleanup bypasses the tombstone guard; these are synthetic rows.
		if _, err := db.ExecContext(cleanupCtx, `DELETE FROM auth.users WHERE id = $1`, f.supabaseID); err != nil {
			t.Errorf("cleanup auth user: %v", err)
		}
		if _, err := db.ExecContext(cleanupCtx, `DELETE FROM account_deletions WHERE clerk_subject = $1`, f.subject); err != nil {
			t.Errorf("cleanup tombstone: %v", err)
		}
	})

	// Mix both ownership key variants so deletion must erase rows keyed by
	// Clerk subject and by bound Supabase UUID.
	var owner string
	insertApp := func(i int) string {
		if i%2 == 0 {
			owner = f.subject
		} else {
			owner = f.supabaseID
		}
		var id string
		if err := db.QueryRowContext(ctx,
			`INSERT INTO applications (user_id, company, role, status) VALUES ($1, $2, $3, 'saved') RETURNING id`,
			owner, fmt.Sprintf("Company %d", i), "Engineer").Scan(&id); err != nil {
			t.Fatalf("seed application: %v", err)
		}
		if _, err := db.ExecContext(ctx,
			`INSERT INTO application_events (user_id, application_id, event_type, to_status) VALUES ($1, $2, 'created', 'saved')`,
			owner, id); err != nil {
			t.Fatalf("seed application event: %v", err)
		}
		return id
	}

	count := 1
	if many {
		count = 60
	}
	var firstTaskID, firstJobID, firstEmailID string
	for i := 0; i < count; i++ {
		id := insertApp(i)
		if i == 0 {
			f.appID = id
		}
		if i >= 2 {
			continue
		}
		taskOwner := owner
		dueAt := time.Now().UTC().Add(48 * time.Hour)
		var taskID string
		var jobID string
		if i == 0 {
			if err := db.QueryRowContext(ctx, `
				INSERT INTO tasks (user_id, application_id, title, status, due_precision, due_at, time_zone)
				VALUES ($1, $2, $3, 'open', 'instant', $4, 'Asia/Kolkata') RETURNING id`,
				taskOwner, id, "Instant task", dueAt).Scan(&taskID); err != nil {
				t.Fatalf("seed instant task: %v", err)
			}
			if err := db.QueryRowContext(ctx, `
				INSERT INTO reminder_jobs (user_id, task_id, due_at, delivery_state, reminder_timing, true_deadline)
				VALUES ($1, $2, $3, 'scheduled', '24h_before', $3) RETURNING id`,
				taskOwner, taskID, dueAt).Scan(&jobID); err != nil {
				t.Fatalf("seed instant job: %v", err)
			}
		} else {
			day := dueAt.Format("2006-01-02")
			if err := db.QueryRowContext(ctx, `
				INSERT INTO tasks (user_id, application_id, title, status, due_precision, due_date, reminder_zone)
				VALUES ($1, $2, $3, 'open', 'date', $4, 'Asia/Kolkata') RETURNING id`,
				taskOwner, id, "Date task", day).Scan(&taskID); err != nil {
				t.Fatalf("seed date task: %v", err)
			}
			if err := db.QueryRowContext(ctx, `
				INSERT INTO reminder_jobs (user_id, task_id, due_at, delivery_state, reminder_timing, due_day, reminder_zone)
				VALUES ($1, $2, $3, 'scheduled', 'day_before_approx', $4, 'Asia/Kolkata') RETURNING id`,
				taskOwner, taskID, dueAt, day).Scan(&jobID); err != nil {
				t.Fatalf("seed approximate job: %v", err)
			}
		}
		if firstTaskID == "" {
			firstTaskID, f.taskID = taskID, taskID
		}
		if firstJobID == "" {
			firstJobID, f.jobID = jobID, jobID
		}
		emailID := fmt.Sprintf("email_%s_%d", suffix, i)
		if _, err := db.ExecContext(ctx, `
			INSERT INTO reminder_provider_emails (provider_email_id, job_id, user_id, delivery_version)
			VALUES ($1, $2, $3, 1)`, emailID, jobID, taskOwner); err != nil {
			t.Fatalf("seed provider email: %v", err)
		}
		if firstEmailID == "" {
			firstEmailID, f.emailID = emailID, emailID
		}
		if _, err := db.ExecContext(ctx, `
			INSERT INTO reminder_delivery_payloads (job_id, delivery_version, recipient, subject, text_body)
			VALUES ($1, 1, $2, 'subject', 'body')`, jobID, suffix+"@example.test"); err != nil {
			t.Fatalf("seed payload: %v", err)
		}
		if _, err := db.ExecContext(ctx, `
			INSERT INTO reminder_email_events (provider_event_id, provider_email_id, job_id, event_type, resolution)
			VALUES ($1, $2, $3, 'delivered', 'applied')`, "evt_"+emailID, emailID, jobID); err != nil {
			t.Fatalf("seed webhook event: %v", err)
		}
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO user_notification_settings (user_id, reminders_enabled) VALUES ($1, false)`, f.subject); err != nil {
		t.Fatalf("seed settings: %v", err)
	}
	return f
}

type rowCounts struct {
	applications, events, tasks, jobs, settings, identities, authUsers int
	providerEmails, payloads, webhookEvents                           int
}

func countRows(t *testing.T, ctx context.Context, db *sql.DB, f fixture) rowCounts {
	t.Helper()
	var c rowCounts
	queries := []struct {
		query string
		arg   any
		dest  *int
	}{
		{`SELECT count(*) FROM applications WHERE user_id = ANY($1::text[])`, f.owners, &c.applications},
		{`SELECT count(*) FROM application_events WHERE user_id = ANY($1::text[])`, f.owners, &c.events},
		{`SELECT count(*) FROM tasks WHERE user_id = ANY($1::text[])`, f.owners, &c.tasks},
		{`SELECT count(*) FROM reminder_jobs WHERE user_id = ANY($1::text[])`, f.owners, &c.jobs},
		{`SELECT count(*) FROM user_notification_settings WHERE user_id = ANY($1::text[])`, f.owners, &c.settings},
		{`SELECT count(*) FROM user_identities WHERE clerk_subject = $1`, f.subject, &c.identities},
		{`SELECT count(*) FROM auth.users WHERE id = $1`, f.supabaseID, &c.authUsers},
		{`SELECT count(*) FROM reminder_provider_emails WHERE user_id = ANY($1::text[])`, f.owners, &c.providerEmails},
		{`SELECT count(*) FROM reminder_delivery_payloads WHERE job_id IN (SELECT id FROM reminder_jobs WHERE user_id = ANY($1::text[]))`, f.owners, &c.payloads},
		{`SELECT count(*) FROM reminder_email_events WHERE job_id IN (SELECT id FROM reminder_jobs WHERE user_id = ANY($1::text[])) OR provider_email_id IN (SELECT provider_email_id FROM reminder_provider_emails WHERE user_id = ANY($1::text[]))`, f.owners, &c.webhookEvents},
	}
	for _, q := range queries {
		if err := db.QueryRowContext(ctx, q.query, q.arg).Scan(q.dest); err != nil {
			t.Fatalf("count rows: %v", err)
		}
	}
	return c
}

func requireZero(t *testing.T, phase string, c rowCounts) {
	t.Helper()
	if c != (rowCounts{}) {
		t.Fatalf("%s: deleted owner still has rows: %+v", phase, c)
	}
}

func newUUID(t *testing.T) string {
	t.Helper()
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func stubEmail(email string) func(context.Context, string) (string, error) {
	return func(context.Context, string) (string, error) { return email, nil }
}

func TestExportSnapshotIncludesAllOwnerRows(t *testing.T) {
	db := testDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	target := seedOwner(t, ctx, db, "export_target", true)
	control := seedOwner(t, ctx, db, "export_control", true)

	store := &Store{DB: db, VerifiedEmail: stubEmail("verified@example.test")}
	export, err := store.Export(ctx, target.subject)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if export.SchemaVersion != ExportSchemaVersion {
		t.Fatalf("schema version = %d", export.SchemaVersion)
	}
	if export.Identity.ClerkSubject != target.subject || export.Identity.PrimaryEmail != "verified@example.test" {
		t.Fatalf("identity = %+v", export.Identity)
	}
	if len(export.Applications) != 60 {
		t.Fatalf("applications = %d, want 60 (no pagination truncation)", len(export.Applications))
	}
	events := 0
	for _, app := range export.Applications {
		events += len(app.Events)
	}
	if events != 60 {
		t.Fatalf("application events = %d, want 60", events)
	}
	if len(export.Tasks) != 2 {
		t.Fatalf("tasks = %d, want 2", len(export.Tasks))
	}
	foundDate, foundInstant := false, false
	for _, task := range export.Tasks {
		if task.DuePrecision == "date" && task.DueDate != "" {
			foundDate = true
		}
		if task.DuePrecision == "instant" && task.DueAt != "" && task.TimeZone != "" {
			foundInstant = true
		}
	}
	if !foundInstant || !foundDate {
		t.Fatalf("deadline representations were not preserved: %+v", export.Tasks)
	}
	if len(export.NotificationSettings) != 1 || export.NotificationSettings[0].RemindersEnabled {
		t.Fatalf("notification settings = %+v", export.NotificationSettings)
	}
	raw, err := json.Marshal(export)
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for _, forbidden := range []string{"provider_email_id", "text_body", "recipient", "lease_token", "delivery_payloads"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("export leaks operational internals: %q", forbidden)
		}
	}
	if strings.Contains(body, control.subject) || strings.Contains(body, control.supabaseID) {
		t.Error("export contains another owner's identifiers")
	}
	if _, err := time.Parse(time.RFC3339, export.ExportedAt); err != nil {
		t.Errorf("exported_at is not an instant: %q", export.ExportedAt)
	}
}

func TestExportEmptyAccountIsValid(t *testing.T) {
	db := testDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store := &Store{DB: db, VerifiedEmail: stubEmail("empty@example.test")}
	export, err := store.Export(ctx, "clerk_empty_"+time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		t.Fatalf("export empty account: %v", err)
	}
	if export.Applications == nil || export.Tasks == nil || export.NotificationSettings == nil {
		t.Fatal("empty arrays must be present, not null")
	}
}

func TestDeleteAccountIsAtomicAndIdempotent(t *testing.T) {
	db := testDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	f := seedOwner(t, ctx, db, "atomic", false)

	// Inject a failure into the middle of the erasure: no data may be lost and
	// no tombstone may remain when the transaction rolls back.
	if _, err := db.ExecContext(ctx, `CREATE OR REPLACE FUNCTION pg_temp.fail_settings_delete() RETURNS trigger AS $$
		BEGIN RAISE EXCEPTION 'injected failure'; END $$ LANGUAGE plpgsql`);
		err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `CREATE CONSTRAINT TRIGGER fail_settings_delete
		AFTER DELETE ON user_notification_settings DEFERRABLE INITIALLY DEFERRED
		FOR EACH ROW EXECUTE FUNCTION pg_temp.fail_settings_delete()`); err != nil {
		t.Fatal(err)
	}
	store := &Store{DB: db}
	if _, err := store.Delete(ctx, f.subject); err == nil {
		t.Fatal("injected failure must abort deletion")
	}
	if got := countRows(t, ctx, db, f); got == (rowCounts{}) {
		t.Fatal("rolled-back deletion erased data")
	}
	var tombstones int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM account_deletions WHERE clerk_subject = $1`, f.subject).Scan(&tombstones); err != nil || tombstones != 0 {
		t.Fatalf("tombstone survived rollback: %d, %v", tombstones, err)
	}
	if _, err := db.ExecContext(ctx, `DROP TRIGGER fail_settings_delete ON user_notification_settings`); err != nil {
		t.Fatal(err)
	}

	first, err := store.Delete(ctx, f.subject)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	second, err := store.Delete(ctx, f.subject)
	if err != nil {
		t.Fatalf("repeat delete: %v", err)
	}
	if first.State != StatusPending || second.State != StatusPending {
		t.Fatalf("states = %q, %q", first.State, second.State)
	}
	requireZero(t, "after idempotent delete", countRows(t, ctx, db, f))
}

func TestDeleteAccountRemovesAllOwnerDataAndPreservesControlOwner(t *testing.T) {
	db := testDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	target := seedOwner(t, ctx, db, "delete_target", false)
	control := seedOwner(t, ctx, db, "delete_control", false)
	wantControl := countRows(t, ctx, db, control)

	store := &Store{DB: db}
	if _, err := store.Delete(ctx, target.subject); err != nil {
		t.Fatalf("delete: %v", err)
	}
	requireZero(t, "deleted owner", countRows(t, ctx, db, target))
	if got := countRows(t, ctx, db, control); got != wantControl {
		t.Fatalf("control owner changed: %+v, want %+v", got, wantControl)
	}
	if _, err := store.Export(ctx, target.subject); !errors.Is(err, ErrAccountDeleted) {
		t.Fatalf("export after deletion = %v, want ErrAccountDeleted", err)
	}
	if state, err := store.Status(ctx, target.subject); err != nil || state != StatusPending {
		t.Fatalf("status = %q, %v", state, err)
	}
}

func TestConcurrentOwnerWriteCannotOutliveDeletion(t *testing.T) {
	db := testDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	f := seedOwner(t, ctx, db, "race", false)
	store := &Store{DB: db}

	// A write that passed authentication before deletion began holds the
	// guard lock until it commits. Deletion must wait for it, then erase its
	// rows. A separate connection is required because pgx pools per *sql.DB.
	db.SetMaxOpenConns(4)
	writer, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Rollback()
	var racingID string
	if err := writer.QueryRowContext(ctx,
		`INSERT INTO applications (user_id, company, role, status) VALUES ($1, 'Racing', 'Engineer', 'saved') RETURNING id`,
		f.subject).Scan(&racingID); err != nil {
		t.Fatalf("racing write: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := store.Delete(ctx, f.subject)
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("deletion did not serialize against the in-flight write: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	if err := writer.Commit(); err != nil {
		t.Fatalf("commit racing write: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("deletion after write commit: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("deletion did not complete after the racing write committed")
	}
	var remaining int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM applications WHERE id = $1`, racingID).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("racing row outlived deletion: %d, %v", remaining, err)
	}

	// A write that starts after the tombstone is committed must be rejected.
	if _, err := db.ExecContext(ctx,
		`INSERT INTO applications (user_id, company, role, status) VALUES ($1, 'After', 'Engineer', 'saved')`, f.subject); err == nil {
		t.Fatal("write after deletion was accepted")
	}
}

func TestReconcileDeletionIsIdempotent(t *testing.T) {
	db := testDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	target := seedOwner(t, ctx, db, "reconcile", false)
	store := &Store{DB: db}
	if _, err := store.Delete(ctx, target.subject); err != nil {
		t.Fatalf("delete: %v", err)
	}

	journal, err := store.Journal(ctx)
	if err != nil {
		t.Fatalf("journal: %v", err)
	}
	found := false
	for _, entry := range journal {
		if entry.ClerkSubject == target.subject {
			found = true
		}
	}
	if !found {
		t.Fatal("journal is missing the tombstone")
	}
	raw, err := json.Marshal(journal)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "@example.test") {
		t.Fatal("journal contains email content")
	}
	var imported []JournalEntry
	if err := json.Unmarshal(raw, &imported); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ImportJournal(ctx, imported); err != nil {
		t.Fatalf("importing the journal twice must be idempotent: %v", err)
	}
	var tombstones int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM account_deletions WHERE clerk_subject = $1`, target.subject).Scan(&tombstones); err != nil || tombstones != 1 {
		t.Fatalf("tombstones after repeat import = %d, %v", tombstones, err)
	}

	// Simulate a restore of the pre-deletion backup: pg_restore bypasses the
	// write guard with session_replication_role = replica.
	if _, err := db.ExecContext(ctx, `SET session_replication_role = replica`); err != nil {
		t.Fatal(err)
	}
	restored := seedOwner(t, ctx, db, "restored", false)
	// Rebind the restored rows to the tombstoned owner ids by inserting a few
	// rows directly under both owner variants.
	for _, owner := range []string{target.subject, target.supabaseID} {
		if _, err := db.ExecContext(ctx,
			`INSERT INTO applications (user_id, company, role, status) VALUES ($1, 'Resurrected', 'Engineer', 'saved')`, owner); err != nil {
			t.Fatalf("simulate restored row: %v", err)
		}
	}
	if _, err := db.ExecContext(ctx, `SET session_replication_role = origin`); err != nil {
		t.Fatal(err)
	}
	_ = restored

	n, err := store.Reconcile(ctx)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if n == 0 {
		t.Fatal("reconcile processed no tombstones")
	}
	var leftover int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM applications WHERE user_id = ANY($1::text[])`, target.owners).Scan(&leftover); err != nil || leftover != 0 {
		t.Fatalf("restored rows survived reconcile: %d, %v", leftover, err)
	}
	if _, err := store.Reconcile(ctx); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM account_deletions WHERE clerk_subject = $1`, target.subject); err != nil {
		t.Fatal(err)
	}
}

// Offline proof of durable retry: a failed Clerk deletion keeps the record
// pending and a later success completes it, regardless of email dispatch.
func TestPendingDeletionRetriesAfterClerkRecovery(t *testing.T) {
	ctx := context.Background()
	store := &fakePendingStore{
		items: []PendingDeletion{{ClerkSubject: "user_retry"}},
	}
	clerk := &fakeClerk{err: errors.New("clerk unavailable")}
	if n, err := RetryPending(ctx, store, clerk); err != nil || n != 0 {
		t.Fatalf("first retry = %d, %v", n, err)
	}
	if store.completed != 0 {
		t.Fatal("failed Clerk deletion was marked complete")
	}
	clerk.err = nil
	if n, err := RetryPending(ctx, store, clerk); err != nil || n != 1 {
		t.Fatalf("second retry = %d, %v", n, err)
	}
	if store.completed != 1 || clerk.calls != 2 {
		t.Fatalf("completed = %d, clerk calls = %d", store.completed, clerk.calls)
	}
	if _, err := RetryPending(ctx, store, clerk); err != nil {
		t.Fatal(err)
	}
	if store.completed != 1 {
		t.Fatal("retry of a completed deletion changed state")
	}
}

type fakePendingStore struct {
	mu        sync.Mutex
	items     []PendingDeletion
	completed int
}

func (f *fakePendingStore) Pending(context.Context) ([]PendingDeletion, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]PendingDeletion(nil), f.items...), nil
}

func (f *fakePendingStore) Complete(context.Context, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.completed++
	f.items = nil
	return nil
}

type fakeClerk struct {
	err   error
	calls int
}

func (f *fakeClerk) BanAndDelete(context.Context, string) error {
	f.calls++
	return f.err
}
