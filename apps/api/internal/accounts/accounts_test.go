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
	"hireloop/api/internal/reminders"

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
	providerEmails, payloads, webhookEvents                            int
}

func countRows(t *testing.T, ctx context.Context, db *sql.DB, f fixture) rowCounts {
	t.Helper()
	var c rowCounts
	queries := []struct {
		query string
		args  []any
		dest  *int
	}{
		{`SELECT count(*) FROM applications WHERE user_id = ANY($1::text[])`, []any{f.owners}, &c.applications},
		{`SELECT count(*) FROM application_events WHERE user_id = ANY($1::text[])`, []any{f.owners}, &c.events},
		{`SELECT count(*) FROM tasks WHERE user_id = ANY($1::text[])`, []any{f.owners}, &c.tasks},
		{`SELECT count(*) FROM reminder_jobs WHERE user_id = ANY($1::text[])`, []any{f.owners}, &c.jobs},
		{`SELECT count(*) FROM user_notification_settings WHERE user_id = ANY($1::text[])`, []any{f.owners}, &c.settings},
		{`SELECT count(*) FROM user_identities WHERE clerk_subject = $1`, []any{f.subject}, &c.identities},
		{`SELECT count(*) FROM auth.users WHERE id = $1`, []any{f.supabaseID}, &c.authUsers},
		{`SELECT count(*) FROM reminder_provider_emails WHERE user_id = ANY($1::text[])`, []any{f.owners}, &c.providerEmails},
		{`SELECT count(*) FROM reminder_delivery_payloads WHERE job_id = $1`, []any{f.jobID}, &c.payloads},
		{`SELECT count(*) FROM reminder_email_events WHERE job_id = $1 OR provider_email_id = $2`, []any{f.jobID, f.emailID}, &c.webhookEvents},
	}
	for _, q := range queries {
		if err := db.QueryRowContext(ctx, q.query, q.args...).Scan(q.dest); err != nil {
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
	if strings.Contains(body, `"supabase_user_id"`) || strings.Contains(body, `"user_id"`) ||
		strings.Contains(body, target.supabaseID) {
		t.Error("export contains internal owner identifiers")
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
		BEGIN RAISE EXCEPTION 'injected failure'; END $$ LANGUAGE plpgsql`); err != nil {
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

func TestLateProviderWebhookIsDiscardedAfterDeletion(t *testing.T) {
	db := testDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	f := seedOwner(t, ctx, db, "late_webhook", false)
	if _, err := (&Store{DB: db}).Delete(ctx, f.subject); err != nil {
		t.Fatalf("delete owner: %v", err)
	}

	applied, err := (&reminders.Store{DB: db}).RecordWebhookEvent(ctx,
		"evt_late_after_"+f.emailID, f.emailID, "email.delivered",
		reminders.Transition{State: reminders.StateDelivered, Note: "late callback"})
	if err != nil || applied {
		t.Fatalf("late webhook = %v, %v; want discarded", applied, err)
	}
	var events, hashes int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM reminder_email_events WHERE provider_event_id = $1`, "evt_late_after_"+f.emailID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM account_deletion_provider_hashes WHERE clerk_subject = $1`, f.subject).Scan(&hashes); err != nil {
		t.Fatal(err)
	}
	if events != 0 || hashes != 1 {
		t.Fatalf("late events = %d, provider-id fingerprints = %d; want 0, 1", events, hashes)
	}
}

func TestWebhookResolvedBeforeDeletionWaitsAndIsDiscarded(t *testing.T) {
	db := testDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	f := seedOwner(t, ctx, db, "webhook_delete_race", false)
	name := fmt.Sprintf("phase5_race_%d", time.Now().UTC().UnixNano())
	if _, err := db.ExecContext(ctx, fmt.Sprintf(`CREATE FUNCTION public.%s() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN IF OLD.user_id = '%s' THEN PERFORM pg_sleep(2); END IF; RETURN OLD; END $$`, name, f.subject)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, fmt.Sprintf(`CREATE TRIGGER %s BEFORE DELETE ON reminder_jobs
		FOR EACH ROW EXECUTE FUNCTION public.%s()`, name, name)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON reminder_jobs`, name))
		_, _ = db.ExecContext(context.Background(), fmt.Sprintf(`DROP FUNCTION IF EXISTS public.%s()`, name))
	})

	deleteDone := make(chan error, 1)
	go func() {
		_, err := (&Store{DB: db}).Delete(ctx, f.subject)
		deleteDone <- err
	}()
	waitForPGWaitEvent(t, ctx, db, "PgSleep")
	webhookDone := make(chan error, 1)
	go func() {
		applied, err := (&reminders.Store{DB: db}).RecordWebhookEvent(ctx,
			"evt_race_after_"+f.emailID, f.emailID, "email.delivered",
			reminders.Transition{State: reminders.StateDelivered, Note: "callback raced with deletion"})
		if err == nil && applied {
			err = errors.New("webhook unexpectedly applied")
		}
		webhookDone <- err
	}()
	waitForPGWaitEvent(t, ctx, db, "advisory")
	if err := <-deleteDone; err != nil {
		t.Fatalf("delete account: %v", err)
	}
	if err := <-webhookDone; err != nil {
		t.Fatalf("webhook after deletion won the owner lock: %v", err)
	}
	if n := rowCount(t, ctx, db, `SELECT count(*) FROM reminder_email_events WHERE provider_event_id = $1`, "evt_race_after_"+f.emailID); n != 0 {
		t.Fatalf("racing callback ledger rows = %d, want 0", n)
	}
}

func waitForPGWaitEvent(t *testing.T, ctx context.Context, db *sql.DB, waitEvent string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var waiting bool
		if err := db.QueryRowContext(ctx, `SELECT EXISTS (
			SELECT 1 FROM pg_stat_activity
			WHERE datname = current_database() AND wait_event = $1 AND pid <> pg_backend_pid()
		)`, waitEvent).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for PostgreSQL wait event %q", waitEvent)
}

func rowCount(t *testing.T, ctx context.Context, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var count int
	if err := db.QueryRowContext(ctx, query, args...).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestPendingDeletionRetryMovesAttemptedSubjectsBehindUntouchedOnes(t *testing.T) {
	db := testDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store := &Store{DB: db}
	suffix := fmt.Sprintf("%d", time.Now().UTC().UnixNano())
	subjects := []string{"clerk_retry_a_" + suffix, "clerk_retry_b_" + suffix, "clerk_retry_c_" + suffix}
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DELETE FROM account_deletions WHERE clerk_subject = ANY($1::text[])`, subjects)
	})
	base := time.Now().UTC().Add(-time.Hour)
	for i, subject := range subjects {
		at := base.Add(time.Duration(i) * time.Minute)
		if _, err := db.ExecContext(ctx, `INSERT INTO account_deletions (clerk_subject, state, created_at, updated_at)
			VALUES ($1, 'pending', $2, $2)`, subject, at); err != nil {
			t.Fatal(err)
		}
	}
	first, err := store.Pending(ctx, 2)
	if err != nil || len(first) != 2 || first[0].ClerkSubject != subjects[0] || first[1].ClerkSubject != subjects[1] {
		t.Fatalf("first pending batch = %+v, %v", first, err)
	}
	claimed, err := store.MarkAttempt(ctx, first[0].ClerkSubject)
	if err != nil || !claimed {
		t.Fatalf("claim first pending subject = %v, %v", claimed, err)
	}
	next, err := store.Pending(ctx, 2)
	if err != nil || len(next) != 2 || next[0].ClerkSubject != subjects[1] || next[1].ClerkSubject != subjects[2] {
		t.Fatalf("next pending batch after a failed attempt = %+v, %v", next, err)
	}
}

func TestConcurrentPendingDeletionAttemptClaimIsExclusive(t *testing.T) {
	db := testDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store := &Store{DB: db}
	subject := fmt.Sprintf("clerk_retry_claim_%d", time.Now().UTC().UnixNano())
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DELETE FROM account_deletions WHERE clerk_subject = $1`, subject)
	})
	if _, err := db.ExecContext(ctx, `INSERT INTO account_deletions (clerk_subject, state, created_at, updated_at)
		VALUES ($1, 'pending', now() - interval '2 minutes', now() - interval '2 minutes')`, subject); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan bool, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			claimed, err := store.MarkAttempt(ctx, subject)
			if err != nil {
				t.Errorf("claim pending deletion: %v", err)
				return
			}
			results <- claimed
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	claims := 0
	for claimed := range results {
		if claimed {
			claims++
		}
	}
	if claims != 1 {
		t.Fatalf("simultaneous pending retry claims = %d, want exactly one", claims)
	}
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
		t.Fatalf("importing the journal twice must be idempotent: %v; entry = %+v", err, imported)
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

func TestImportJournalPreservesTimestampsAndRejectsInvalidSupabaseIDs(t *testing.T) {
	db := testDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store := &Store{DB: db}

	invalid := JournalEntry{
		ClerkSubject:   "clerk_bad_journal_" + time.Now().UTC().Format("20060102150405.000000000"),
		SupabaseUserID: "00000000-0000-0000-0000-000000000000'); DELETE FROM auth.users; --",
		State:          StatusPending,
		CreatedAt:      "2026-01-01T00:00:00Z",
		UpdatedAt:      "2026-01-01T00:00:00Z",
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(),
			`DELETE FROM account_deletions WHERE clerk_subject = $1`, invalid.ClerkSubject)
	})
	if _, err := store.ImportJournal(ctx, []JournalEntry{invalid}); err == nil {
		t.Fatal("journal with a non-UUID Supabase owner was accepted")
	}
	var rows int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM account_deletions WHERE clerk_subject = $1`, invalid.ClerkSubject).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("invalid journal created %d tombstones, err = %v", rows, err)
	}

	created := time.Date(2026, 2, 3, 4, 5, 6, 123456000, time.UTC)
	updated := created.Add(5 * time.Minute)
	completed := updated.Add(time.Minute)
	sourceSubject := "clerk_journal_source_" + time.Now().UTC().Format("20060102150405.000000000")
	if _, err := db.ExecContext(ctx, `
		INSERT INTO account_deletions (clerk_subject, state, created_at, updated_at, completed_at)
		VALUES ($1, 'completed', $2, $3, $4)`, sourceSubject, created, updated, completed); err != nil {
		t.Fatalf("seed source tombstone: %v", err)
	}
	providerHash := strings.Repeat("a", 64)
	if _, err := db.ExecContext(ctx, `INSERT INTO account_deletion_provider_hashes (provider_email_hash, clerk_subject) VALUES ($1, $2)`, providerHash, sourceSubject); err != nil {
		t.Fatalf("seed provider fingerprint: %v", err)
	}
	targetSubject := sourceSubject + "_restored"
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DELETE FROM account_deletions WHERE clerk_subject = ANY($1::text[])`, []string{sourceSubject, targetSubject})
	})

	entries, err := store.Journal(ctx)
	if err != nil {
		t.Fatalf("export source journal: %v", err)
	}
	var entry JournalEntry
	for _, candidate := range entries {
		if candidate.ClerkSubject == sourceSubject {
			entry = candidate
			break
		}
	}
	if entry.ClerkSubject == "" {
		t.Fatal("source tombstone is missing from journal")
	}
	if len(entry.ProviderEmailHashes) != 1 || entry.ProviderEmailHashes[0] != providerHash {
		t.Fatalf("journal provider fingerprints = %v, want %q", entry.ProviderEmailHashes, providerHash)
	}
	for value, want := range map[string]time.Time{entry.CreatedAt: created, entry.UpdatedAt: updated, entry.CompletedAt: completed} {
		parsed, err := time.Parse(time.RFC3339Nano, value)
		if err != nil || !parsed.Equal(want) {
			t.Fatalf("journal timestamp %q parsed as %v, %v; want %v", value, parsed, err, want)
		}
	}
	entry.ClerkSubject = targetSubject
	if _, err := db.ExecContext(ctx, `DELETE FROM account_deletion_provider_hashes WHERE clerk_subject = $1`, sourceSubject); err != nil {
		t.Fatalf("prepare independent restore target: %v", err)
	}
	if _, err := store.ImportJournal(ctx, []JournalEntry{entry}); err != nil {
		t.Fatalf("import source timestamps: %v", err)
	}
	var gotCreated, gotUpdated, gotCompleted time.Time
	if err := db.QueryRowContext(ctx, `
		SELECT created_at, updated_at, completed_at FROM account_deletions WHERE clerk_subject = $1`, targetSubject).
		Scan(&gotCreated, &gotUpdated, &gotCompleted); err != nil {
		t.Fatalf("read imported timestamps: %v", err)
	}
	if !gotCreated.Equal(created) || !gotUpdated.Equal(updated) || !gotCompleted.Equal(completed) {
		t.Fatalf("imported timestamps = %v, %v, %v; want %v, %v, %v", gotCreated, gotUpdated, gotCompleted, created, updated, completed)
	}
	var importedHashes int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM account_deletion_provider_hashes WHERE clerk_subject = $1 AND provider_email_hash = $2`, targetSubject, providerHash).Scan(&importedHashes); err != nil || importedHashes != 1 {
		t.Fatalf("imported provider fingerprints = %d, %v; want 1", importedHashes, err)
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
	if n, err := RetryPending(ctx, store, clerk); err == nil || n != 0 {
		t.Fatalf("failed first retry must report outstanding work: %d, %v", n, err)
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

func TestPendingDeletionRetryIsBounded(t *testing.T) {
	items := make([]PendingDeletion, PendingRetryBatchSize+3)
	for i := range items {
		items[i].ClerkSubject = fmt.Sprintf("user_retry_%d", i)
	}
	store := &fakePendingStore{items: items}
	clerk := &fakeClerk{err: errors.New("clerk unavailable")}
	if _, err := RetryPending(context.Background(), store, clerk); err == nil {
		t.Fatal("failed batch should report outstanding work")
	}
	if clerk.calls != PendingRetryBatchSize || store.attempted != PendingRetryBatchSize {
		t.Fatalf("retry calls = %d, recorded attempts = %d, want %d", clerk.calls, store.attempted, PendingRetryBatchSize)
	}
}

type fakePendingStore struct {
	mu        sync.Mutex
	items     []PendingDeletion
	completed int
	attempted int
}

func (f *fakePendingStore) Pending(_ context.Context, limit int) ([]PendingDeletion, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if limit < len(f.items) {
		return append([]PendingDeletion(nil), f.items[:limit]...), nil
	}
	return append([]PendingDeletion(nil), f.items...), nil
}

func (f *fakePendingStore) MarkAttempt(context.Context, string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.attempted++
	return true, nil
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
