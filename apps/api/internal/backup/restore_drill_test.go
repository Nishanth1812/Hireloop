package backup

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"hireloop/api/db/migrations"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// Restore drill for the Phase 4 deletion/backup security gates (R1-07, R2-05).
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
//     owner's rows. That is the gap: backup/restore alone is not a deletion
//     procedure. The test asserts this hazard explicitly rather than hiding it.
//  2. A deletion journal kept OUTSIDE the backup, replayed after restore,
//     prevents the deleted owner from becoming accessible again while leaving
//     every other owner's restored rows intact.
//
// Honest scope: the application has no account-deletion endpoint and the
// owner-scoped tables have no cascade from auth.users, so "deletion" here is the
// operator-run SQL below, not a product feature. This test proves the restore
// procedure; it does not claim account deletion exists.

type drillOwner struct {
	owner    string
	authID   string
	appID    string
	taskID   string
	jobID    string
	syntheticEmail string
}

type drillCounts struct {
	applications int
	events       int
	tasks        int
	jobs         int
	settings     int
	identities   int
	authUsers    int
}

type journalEntry struct {
	Owner      string `json:"owner"`
	AuthUserID string `json:"auth_user_id"`
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
	db.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("connect to database: %v", err)
	}
	return db
}

// prepareSource builds the legacy auth.users shape the real migrations expect,
// then applies the repository's real migrations. Only synthetic rows are added.
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
	o := drillOwner{
		owner:          fmt.Sprintf("drill_%s_%s", label, randomHex(t, 6)),
		authID:         newUUID(t),
		syntheticEmail: fmt.Sprintf("%s-%s@example.test", label, randomHex(t, 6)),
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO auth.users (id, email) VALUES ($1, $2)`, o.authID, o.syntheticEmail); err != nil {
		t.Fatalf("seed synthetic auth user: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO public.user_identities (clerk_subject, supabase_user_id) VALUES ($1, $2)`, o.owner, o.authID); err != nil {
		t.Fatalf("seed identity mapping: %v", err)
	}
	if err := db.QueryRowContext(ctx,
		`INSERT INTO public.applications (user_id, company, role, status) VALUES ($1, 'Drill Corp', 'Engineer', 'saved') RETURNING id`,
		o.owner).Scan(&o.appID); err != nil {
		t.Fatalf("seed application: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO public.application_events (user_id, application_id, event_type, to_status)
		 VALUES ($1, $2, 'created', 'saved')`, o.owner, o.appID); err != nil {
		t.Fatalf("seed application event: %v", err)
	}
	dueAt := time.Now().UTC().Add(48 * time.Hour)
	if err := db.QueryRowContext(ctx, `
		INSERT INTO public.tasks (user_id, application_id, title, status, due_precision, due_at, time_zone)
		VALUES ($1, $2, 'Drill task', 'open', 'instant', $3, 'UTC') RETURNING id`,
		o.owner, o.appID, dueAt).Scan(&o.taskID); err != nil {
		t.Fatalf("seed task: %v", err)
	}
	if err := db.QueryRowContext(ctx, `
		INSERT INTO public.reminder_jobs (user_id, task_id, due_at, delivery_state)
		VALUES ($1, $2, $3, 'scheduled') RETURNING id`,
		o.owner, o.taskID, dueAt).Scan(&o.jobID); err != nil {
		t.Fatalf("seed reminder job: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO public.user_notification_settings (user_id, reminders_enabled) VALUES ($1, true)`, o.owner); err != nil {
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
		{`SELECT count(*) FROM public.applications WHERE user_id = $1`, o.owner, &c.applications},
		{`SELECT count(*) FROM public.application_events WHERE user_id = $1`, o.owner, &c.events},
		{`SELECT count(*) FROM public.tasks WHERE user_id = $1`, o.owner, &c.tasks},
		{`SELECT count(*) FROM public.reminder_jobs WHERE user_id = $1`, o.owner, &c.jobs},
		{`SELECT count(*) FROM public.user_notification_settings WHERE user_id = $1`, o.owner, &c.settings},
		{`SELECT count(*) FROM public.user_identities WHERE clerk_subject = $1`, o.owner, &c.identities},
		{`SELECT count(*) FROM auth.users WHERE id = $1`, o.authID, &c.authUsers},
	}
	for _, q := range queries {
		if err := db.QueryRowContext(ctx, q.query, q.id).Scan(q.dest); err != nil {
			t.Fatalf("count owner rows: %v", err)
		}
	}
	return c
}

// deleteOwner is the operator-run erasure the gate has to survive. There is no
// application deletion path today; owner-scoped tables have no cascade from
// auth.users, so every table must be removed explicitly.
func deleteOwner(t *testing.T, ctx context.Context, db *sql.DB, o drillOwner) {
	t.Helper()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin deletion: %v", err)
	}
	defer tx.Rollback()
	statements := []struct {
		query string
		id    any
	}{
		{`DELETE FROM public.reminder_jobs WHERE user_id = $1`, o.owner},
		{`DELETE FROM public.application_events WHERE user_id = $1`, o.owner},
		{`DELETE FROM public.tasks WHERE user_id = $1`, o.owner},
		{`DELETE FROM public.applications WHERE user_id = $1`, o.owner},
		{`DELETE FROM public.user_notification_settings WHERE user_id = $1`, o.owner},
		{`DELETE FROM public.user_identities WHERE clerk_subject = $1`, o.owner},
		{`DELETE FROM auth.users WHERE id = $1`, o.authID},
	}
	for _, stmt := range statements {
		if _, err := tx.ExecContext(ctx, stmt.query, stmt.id); err != nil {
			t.Fatalf("delete owner row: %v", err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit deletion: %v", err)
	}
}

func requireZero(t *testing.T, phase string, c drillCounts) {
	t.Helper()
	nonZero := []string{}
	if c.applications != 0 { nonZero = append(nonZero, "applications") }
	if c.events != 0 { nonZero = append(nonZero, "application_events") }
	if c.tasks != 0 { nonZero = append(nonZero, "tasks") }
	if c.jobs != 0 { nonZero = append(nonZero, "reminder_jobs") }
	if c.settings != 0 { nonZero = append(nonZero, "notification_settings") }
	if c.identities != 0 { nonZero = append(nonZero, "user_identities") }
	if c.authUsers != 0 { nonZero = append(nonZero, "auth.users") }
	if len(nonZero) > 0 {
		t.Fatalf("%s: deleted owner still has rows in %v (%+v)", phase, nonZero, c)
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

// TestRestore drill is the executable R2-05/R1-07 proof. It asserts the hazard
// (naive restore resurrects a deleted owner) and the mitigation (an external
// deletion journal replayed after restore keeps that owner erased).
func TestRestoreDrillDeletedAccountDoesNotReturn(t *testing.T) {
	sourceURL, restoreURL := drillURLs(t)
	pgDump := drillBinary(t, "HIRELOOP_PG_DUMP", "pg_dump")
	pgRestore := drillBinary(t, "HIRELOOP_PG_RESTORE", "pg_restore")

	source := openDrillDB(t, sourceURL)
	prepareSource(t, source)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	deleted := seedOwner(t, ctx, source, "deleted")
	control := seedOwner(t, ctx, source, "control")
	// Keep both owners tidy in this disposable database even if an assertion fails.
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cleanupCancel()
		deleteOwner(t, cleanupCtx, source, deleted)
		deleteOwner(t, cleanupCtx, source, control)
	})

	wantControl := countOwner(t, ctx, source, control)
	if wantControl.applications == 0 || wantControl.events == 0 || wantControl.jobs == 0 {
		t.Fatalf("control owner was not seeded: %+v", wantControl)
	}

	tmpDir := t.TempDir()
	dumpFile := filepath.Join(tmpDir, "hireloop-drill.dump")
	journalFile := filepath.Join(tmpDir, "deletion-journal.json")

	runCommand(t, pgDump, "-Fc", "--no-owner", "--no-privileges", "-d", sourceURL, "-f", dumpFile)

	// Operator erasure of one account, followed by a deletion journal entry that
	// lives outside the database backup.
	deleteOwner(t, ctx, source, deleted)
	requireZero(t, "source after deletion", countOwner(t, ctx, source, deleted))
	journal := []journalEntry{{Owner: deleted.owner, AuthUserID: deleted.authID}}
	journalBytes, err := json.Marshal(journal)
	if err != nil {
		t.Fatalf("marshal deletion journal: %v", err)
	}
	if err := os.WriteFile(journalFile, journalBytes, 0o600); err != nil {
		t.Fatalf("write deletion journal: %v", err)
	}

	// Restore the pre-deletion backup into the separate empty database.
	target := openDrillDB(t, restoreURL)
	runCommand(t, pgRestore, "--no-owner", "--no-privileges", "-d", restoreURL, dumpFile)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cleanupCancel()
		deleteOwner(t, cleanupCtx, target, deleted)
		deleteOwner(t, cleanupCtx, target, control)
	})

	// Hazard: a plain restore brings the deleted account back. Assert it so the
	// gate reports the gap instead of silently passing on a clean-looking backup.
	afterNaiveRestore := countOwner(t, ctx, target, deleted)
	if afterNaiveRestore == (drillCounts{}) {
		t.Fatalf("R2-05 gap check: naive restore did NOT resurrect the deleted owner (%+v); investigate the backup before relying on this drill", afterNaiveRestore)
	}
	t.Logf("GAP PROVEN: naive restore resurrected the deleted owner (%+v)", afterNaiveRestore)
	requireEqual(t, "control owner after naive restore", countOwner(t, ctx, target, control), wantControl)

	// Mitigation: replay the external journal after every restore, then verify
	// the deleted owner is gone and the control owner is unchanged.
	stored, err := os.ReadFile(journalFile)
	if err != nil {
		t.Fatalf("read deletion journal: %v", err)
	}
	var replay []journalEntry
	if err := json.Unmarshal(stored, &replay); err != nil {
		t.Fatalf("decode deletion journal: %v", err)
	}
	for _, entry := range replay {
		deleteOwner(t, ctx, target, drillOwner{owner: entry.Owner, authID: entry.AuthUserID})
	}
	requireZero(t, "after journal replay", countOwner(t, ctx, target, deleted))
	requireEqual(t, "control owner after journal replay", countOwner(t, ctx, target, control), wantControl)
	t.Log("GATE MET: journal replayed after restore; deleted owner absent, control owner intact")
}
