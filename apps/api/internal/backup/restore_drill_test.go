package backup

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"hireloop/api/db/migrations"
	"hireloop/api/internal/accounts"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// Restore drill for the Phase 5 account-deletion gates (R1-07, R2-05).
//
// This test is opt-in and MUST only be pointed at two freshly created,
// disposable databases on a disposable PostgreSQL cluster. It never connects to
// a database by default and it uses only synthetic owners and synthetic emails.
//
//   HIRELOOP_BACKUP_SOURCE_URL   empty source database to seed, back up and delete in
//   HIRELOOP_BACKUP_RESTORE_URL  separate empty database to restore into
//   HIRELOOP_PG_DUMP             optional path to pg_dump (default: pg_dump on PATH)
//   HIRELOOP_PG_RESTORE          optional path to pg_restore (default: pg_restore on PATH)
//
// What it proves:
//  1. A plain logical restore of a pre-deletion backup resurrects a deleted
//     owner's rows in every data table. This hazard is asserted explicitly.
//  2. The production deletion path erases all ten data tables and leaves a
//     durable tombstone. Exporting that journal outside the backup, then
//     importing and reconciling it after a restore, keeps the owner erased and
//     leaves every other owner's restored rows intact.
//
// Honest scope: this exercises a logical pg_dump/pg_restore only. It does not
// claim PITR, offsite backup, or a physical-restore test. Credentials come only
// from the two opt-in environment variables; no .env file is read.

type drillOwner struct {
	subject   string
	authID    string
	appID     string
	taskID    string
	jobID     string
	emailID   string
	owners    []string
	syntheticEmail string
}

// drillCounts covers every Hireloop-owned data table, including the provider
// delivery tables and the webhook event ledger that earlier drills omitted.
type drillCounts struct {
	applications, events, tasks, jobs, settings, identities, authUsers int
	providerEmails, payloads, webhookEvents                           int
}

func drillURLs(t *testing.T) (string, string) {
	t.Helper()
	source := os.Getenv("HIRELOOP_BACKUP_SOURCE_URL")
	restore := os.Getenv("HIRELOOP_BACKUP_RESTORE_URL")
	if source == "" || restore == "" {
		t.Skip("set HIRELOOP_BACKUP_SOURCE_URL and HIRELOOP_BACKUP_RESTORE_URL to disposable databases to run the restore drill")
	}
	if source == restore {
		t.Fatal("source and restore URLs must be different databases")
	}
	return source, restore
}

func drillBinary(t *testing.T, envVar, fallback string) string {
	t.Helper()
	if value := os.Getenv(envVar); value != "" {
		return value
	}
	path, err := exec.LookPath(fallback)
	if err != nil {
		t.Skipf("%s not available: %v", fallback, err)
	}
	return path
}

func randomHex(t *testing.T, bytes int) string {
	t.Helper()
	buf := make([]byte, bytes)
	if _, err := rand.Read(buf); err != nil {
		t.Fatalf("random bytes: %v", err)
	}
	return hex.EncodeToString(buf)
}

func newUUID(t *testing.T) string {
	t.Helper()
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("random uuid: %v", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func openDrillDB(t *testing.T, url string) *sql.DB {
	t.Helper()
	db, err := sql.Open("pgx", url)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("connect to database: %v", err)
	}
	return db
}

// prepareSource builds the synthetic auth schema the repository's migrations
// expect, then applies the real migrations. Only synthetic rows are added.
func prepareSource(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := db.ExecContext(ctx, `
		CREATE SCHEMA IF NOT EXISTS auth;
		CREATE TABLE IF NOT EXISTS auth.users (
			id uuid PRIMARY KEY,
			email text NOT NULL UNIQUE
		);`); err != nil {
		t.Fatalf("create synthetic auth schema: %v", err)
	}
	if err := migrations.Apply(db); err != nil {
		t.Fatalf("apply real migrations: %v", err)
	}
}

func seedOwner(t *testing.T, ctx context.Context, db *sql.DB, label string) drillOwner {
	t.Helper()
	suffix := fmt.Sprintf("%s_%s", label, randomHex(t, 6))
	o := drillOwner{
		subject:        "drill_" + suffix,
		authID:         newUUID(t),
		syntheticEmail: suffix + "@example.test",
	}
	o.owners = []string{o.subject, o.authID}
	if _, err := db.ExecContext(ctx, `INSERT INTO auth.users (id, email) VALUES ($1, $2)`, o.authID, o.syntheticEmail); err != nil {
		t.Fatalf("seed synthetic auth user: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO public.user_identities (clerk_subject, supabase_user_id) VALUES ($1, $2)`, o.subject, o.authID); err != nil {
		t.Fatalf("seed identity mapping: %v", err)
	}
	if err := db.QueryRowContext(ctx,
		`INSERT INTO public.applications (user_id, company, role, status) VALUES ($1, 'Drill Corp', 'Engineer', 'saved') RETURNING id`,
		o.subject).Scan(&o.appID); err != nil {
		t.Fatalf("seed application: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO public.application_events (user_id, application_id, event_type, to_status)
		 VALUES ($1, $2, 'created', 'saved')`, o.subject, o.appID); err != nil {
		t.Fatalf("seed application event: %v", err)
	}
	dueAt := time.Now().UTC().Add(48 * time.Hour)
	if err := db.QueryRowContext(ctx, `
		INSERT INTO public.tasks (user_id, application_id, title, status, due_precision, due_at, time_zone)
		VALUES ($1, $2, 'Drill task', 'open', 'instant', $3, 'UTC') RETURNING id`,
		o.subject, o.appID, dueAt).Scan(&o.taskID); err != nil {
		t.Fatalf("seed task: %v", err)
	}
	if err := db.QueryRowContext(ctx, `
		INSERT INTO public.reminder_jobs (user_id, task_id, due_at, delivery_state, reminder_timing, true_deadline)
		VALUES ($1, $2, $3, 'scheduled', '24h_before', $3) RETURNING id`,
		o.subject, o.taskID, dueAt).Scan(&o.jobID); err != nil {
		t.Fatalf("seed reminder job: %v", err)
	}
	o.emailID = "email_" + suffix
	if _, err := db.ExecContext(ctx, `
		INSERT INTO public.reminder_provider_emails (provider_email_id, job_id, user_id, delivery_version)
		VALUES ($1, $2, $3, 1)`, o.emailID, o.jobID, o.subject); err != nil {
		t.Fatalf("seed provider email: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO public.reminder_delivery_payloads (job_id, delivery_version, recipient, subject, text_body)
		VALUES ($1, 1, $2, 'Drill subject', 'Drill body')`, o.jobID, o.syntheticEmail); err != nil {
		t.Fatalf("seed delivery payload: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO public.reminder_email_events (provider_event_id, provider_email_id, job_id, event_type, resolution)
		VALUES ($1, $2, $3, 'delivered', 'applied')`, "evt_"+o.emailID, o.emailID, o.jobID); err != nil {
		t.Fatalf("seed webhook event: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO public.user_notification_settings (user_id, reminders_enabled) VALUES ($1, true)`, o.subject); err != nil {
		t.Fatalf("seed notification setting: %v", err)
	}
	return o
}

func countOwner(t *testing.T, ctx context.Context, db *sql.DB, o drillOwner) drillCounts {
	t.Helper()
	var c drillCounts
	queries := []struct {
		query string
		id    any
		dest  *int
	}{
		{`SELECT count(*) FROM public.applications WHERE user_id = ANY($1::text[])`, o.owners, &c.applications},
		{`SELECT count(*) FROM public.application_events WHERE user_id = ANY($1::text[])`, o.owners, &c.events},
		{`SELECT count(*) FROM public.tasks WHERE user_id = ANY($1::text[])`, o.owners, &c.tasks},
		{`SELECT count(*) FROM public.reminder_jobs WHERE user_id = ANY($1::text[])`, o.owners, &c.jobs},
		{`SELECT count(*) FROM public.user_notification_settings WHERE user_id = ANY($1::text[])`, o.owners, &c.settings},
		{`SELECT count(*) FROM public.user_identities WHERE clerk_subject = $1`, o.subject, &c.identities},
		{`SELECT count(*) FROM auth.users WHERE id = $1`, o.authID, &c.authUsers},
		{`SELECT count(*) FROM public.reminder_provider_emails WHERE user_id = ANY($1::text[])`, o.owners, &c.providerEmails},
		{`SELECT count(*) FROM public.reminder_delivery_payloads WHERE job_id IN (SELECT id FROM public.reminder_jobs WHERE user_id = ANY($1::text[]))`, o.owners, &c.payloads},
		{`SELECT count(*) FROM public.reminder_email_events WHERE job_id IN (SELECT id FROM public.reminder_jobs WHERE user_id = ANY($1::text[])) OR provider_email_id IN (SELECT provider_email_id FROM public.reminder_provider_emails WHERE user_id = ANY($1::text[]))`, o.owners, &c.webhookEvents},
	}
	for _, q := range queries {
		if err := db.QueryRowContext(ctx, q.query, q.id).Scan(q.dest); err != nil {
			t.Fatalf("count owner rows: %v", err)
		}
	}
	return c
}

// cleanupOwner removes only the synthetic rows of this drill owner.
func cleanupOwner(t *testing.T, ctx context.Context, db *sql.DB, o drillOwner) {
	t.Helper()
	statements := []struct {
		query string
		id    any
	}{
		{`DELETE FROM public.reminder_email_events WHERE job_id IN (SELECT id FROM public.reminder_jobs WHERE user_id = ANY($1::text[]))`, o.owners},
		{`DELETE FROM public.reminder_delivery_payloads WHERE job_id IN (SELECT id FROM public.reminder_jobs WHERE user_id = ANY($1::text[]))`, o.owners},
		{`DELETE FROM public.reminder_provider_emails WHERE user_id = ANY($1::text[])`, o.owners},
		{`DELETE FROM public.reminder_jobs WHERE user_id = ANY($1::text[])`, o.owners},
		{`DELETE FROM public.application_events WHERE user_id = ANY($1::text[])`, o.owners},
		{`DELETE FROM public.tasks WHERE user_id = ANY($1::text[])`, o.owners},
		{`DELETE FROM public.applications WHERE user_id = ANY($1::text[])`, o.owners},
		{`DELETE FROM public.user_notification_settings WHERE user_id = ANY($1::text[])`, o.owners},
		{`DELETE FROM public.user_identities WHERE clerk_subject = $1`, o.subject},
		{`DELETE FROM auth.users WHERE id = $1`, o.authID},
		{`DELETE FROM public.account_deletions WHERE clerk_subject = $1`, o.subject},
	}
	for _, stmt := range statements {
		if _, err := db.ExecContext(ctx, stmt.query, stmt.id); err != nil {
			t.Errorf("cleanup synthetic owner: %v", err)
		}
	}
}

func requireZero(t *testing.T, phase string, c drillCounts) {
	t.Helper()
	if c != (drillCounts{}) {
		t.Fatalf("%s: deleted owner still has rows in %+v", phase, c)
	}
}

func requireEqual(t *testing.T, phase string, got, want drillCounts) {
	t.Helper()
	if got != want {
		t.Fatalf("%s: restored control owner counts = %+v, want %+v", phase, got, want)
	}
}

func runCommand(t *testing.T, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v failed: %v\n%s", name, args, err, out)
	}
}

func TestRestoreDrillDeletedAccountDoesNotReturn(t *testing.T) {
	sourceURL, restoreURL := drillURLs(t)
	pgDump := drillBinary(t, "HIRELOOP_PG_DUMP", "pg_dump")
	pgRestore := drillBinary(t, "HIRELOOP_PG_RESTORE", "pg_restore")

	source := openDrillDB(t, sourceURL)
	prepareSource(t, source)

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	deleted := seedOwner(t, ctx, source, "deleted")
	control := seedOwner(t, ctx, source, "control")
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		cleanupOwner(t, cleanupCtx, source, deleted)
		cleanupOwner(t, cleanupCtx, source, control)
	})

	wantControl := countOwner(t, ctx, source, control)
	if wantControl.applications == 0 || wantControl.events == 0 || wantControl.jobs == 0 ||
		wantControl.providerEmails == 0 || wantControl.payloads == 0 || wantControl.webhookEvents == 0 {
		t.Fatalf("control owner was not seeded in all data tables: %+v", wantControl)
	}

	tmpDir := t.TempDir()
	dumpFile := filepath.Join(tmpDir, "hireloop-drill.dump")
	journalFile := filepath.Join(tmpDir, "deletion-journal.json")

	// Back up the pre-deletion state.
	runCommand(t, pgDump, "-Fc", "--no-owner", "--no-privileges", "-d", sourceURL, "-f", dumpFile)

	// Delete through the production account lifecycle. No hand-written SQL and
	// no separate JSON journal here: the tombstone in account_deletions is the
	// durable journal, exported separately from the backup below.
	sourceStore := &accounts.Store{DB: source}
	result, err := sourceStore.Delete(ctx, deleted.subject)
	if err != nil {
		t.Fatalf("delete through account lifecycle: %v", err)
	}
	if result.State != accounts.StatusPending {
		t.Fatalf("deletion state = %q, want pending (Clerk is not called in the drill)", result.State)
	}
	requireZero(t, "source after deletion", countOwner(t, ctx, source, deleted))
	requireEqual(t, "control after source deletion", countOwner(t, ctx, source, control), wantControl)

	journal, err := sourceStore.Journal(ctx)
	if err != nil {
		t.Fatalf("export live journal: %v", err)
	}
	journalBytes, err := accounts.MarshalJournal(journal)
	if err != nil {
		t.Fatalf("marshal journal: %v", err)
	}
	if err := os.WriteFile(journalFile, journalBytes, 0o600); err != nil {
		t.Fatalf("write journal: %v", err)
	}

	// Restore the pre-deletion backup into the separate empty database.
	target := openDrillDB(t, restoreURL)
	// --clean makes the drill repeatable against the same disposable target.
	runCommand(t, pgRestore, "--clean", "--if-exists", "--no-owner", "--no-privileges", "-d", restoreURL, dumpFile)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		cleanupOwner(t, cleanupCtx, target, deleted)
		cleanupOwner(t, cleanupCtx, target, control)
	})

	// Hazard: a plain restore brings the deleted account back in every table.
	afterNaiveRestore := countOwner(t, ctx, target, deleted)
	if afterNaiveRestore == (drillCounts{}) {
		t.Fatalf("R2-05 gap check: naive restore did NOT resurrect the deleted owner (%+v); investigate the backup", afterNaiveRestore)
	}
	t.Logf("GAP PROVEN: naive restore resurrected the deleted owner in all data tables (%+v)", afterNaiveRestore)
	requireEqual(t, "control owner after naive restore", countOwner(t, ctx, target, control), wantControl)

	// Mitigation: import the live journal (including deletions newer than the
	// backup) into the restored target, then reconcile through production code.
	stored, err := os.ReadFile(journalFile)
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}
	imported, err := accounts.UnmarshalJournal(stored)
	if err != nil {
		t.Fatalf("decode journal: %v", err)
	}
	targetStore := &accounts.Store{DB: target}
	if _, err := targetStore.ImportJournal(ctx, imported); err != nil {
		t.Fatalf("import journal after restore: %v", err)
	}
	if n, err := targetStore.Reconcile(ctx); err != nil || n == 0 {
		t.Fatalf("reconcile after restore = %d, %v", n, err)
	}
	requireZero(t, "after reconcile", countOwner(t, ctx, target, deleted))
	requireEqual(t, "control owner after reconcile", countOwner(t, ctx, target, control), wantControl)
	var tombstones int
	if err := target.QueryRowContext(ctx,
		`SELECT count(*) FROM public.account_deletions WHERE clerk_subject = $1`, deleted.subject).Scan(&tombstones); err != nil || tombstones != 1 {
		t.Fatalf("tombstone was not retained after reconcile: %d, %v", tombstones, err)
	}
	if _, err := targetStore.Reconcile(ctx); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	requireZero(t, "after second reconcile", countOwner(t, ctx, target, deleted))
	t.Log("GATE MET: live journal imported and reconciled; deleted owner absent from all ten tables, control owner intact, tombstone retained")
}
