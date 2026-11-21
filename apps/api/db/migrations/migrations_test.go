package migrations

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestEmbeddedInitialMigration(t *testing.T) {
	source, err := NewSource()
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	version, err := source.First()
	if err != nil || version != 1 {
		t.Fatalf("first migration = %d, %v", version, err)
	}
	reader, _, err := source.ReadUp(version)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	data, err := io.ReadAll(reader)
	if err != nil || !strings.Contains(string(data), "CREATE TABLE public.applications") || !strings.Contains(string(data), "ENABLE ROW LEVEL SECURITY") {
		t.Fatalf("missing schema or RLS: %v", err)
	}
}

// TestMigrationsAreContiguous makes sure a new migration can never be added
// out of order or with a duplicated version, which golang-migrate would either
// skip or apply unpredictably.
func TestMigrationsAreContiguous(t *testing.T) {
	source, err := NewSource()
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	version, err := source.First()
	if err != nil {
		t.Fatal(err)
	}
	for want := uint(1); ; want++ {
		if version != want {
			t.Fatalf("migration version = %d, want %d", version, want)
		}
		if _, _, err := source.ReadUp(version); err != nil {
			t.Fatalf("read migration %d: %v", version, err)
		}
		next, err := source.Next(version)
		if errors.Is(err, os.ErrNotExist) {
			return
		}
		if err != nil {
			t.Fatalf("next migration after %d: %v", version, err)
		}
		version = next
	}
}

// TestManualFieldsMigrationIsAdditive pins the Phase 2 field migration: the new
// columns are additive, preserve existing rows, and stay date-only for
// applied_on so no time is invented.
func TestManualFieldsMigrationIsAdditive(t *testing.T) {
	source, err := NewSource()
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	reader, _, err := source.ReadUp(5)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	// Check the statements, not the comments: the rollback notes legitimately
	// mention DROP COLUMN.
	statements := make([]string, 0)
	for _, line := range strings.Split(body, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" && !strings.HasPrefix(trimmed, "--") {
			statements = append(statements, trimmed)
		}
	}
	applied := strings.Join(statements, "\n")
	for _, column := range []string{"url", "channel", "job_id", "applied_on", "notes"} {
		if !strings.Contains(applied, "ADD COLUMN "+column) {
			t.Errorf("migration 005 does not add column %q", column)
		}
	}
	if !strings.Contains(applied, "ADD COLUMN applied_on date") {
		t.Error("applied_on must be date-only, never a timestamp")
	}
	if strings.Contains(applied, "NOT NULL") {
		t.Error("new columns must stay nullable so existing rows remain valid")
	}
	if strings.Contains(applied, "DROP COLUMN") || strings.Contains(applied, "DELETE FROM") {
		t.Error("migration 005 must not remove or rewrite existing data")
	}
	if !strings.Contains(body, "Rollback:") {
		t.Error("migration 005 must document how to reverse it")
	}
}

// TestTasksMigrationIsAdditive pins the Phase 3 schema rule: migration 006
// creates a new owner-scoped table and never rewrites an existing record. Its
// date-only and instant columns stay distinct, and rollback is documented.
func TestTasksMigrationIsAdditive(t *testing.T) {
	source, err := NewSource()
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	reader, _, err := source.ReadUp(6)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	statements := make([]string, 0)
	for _, line := range strings.Split(body, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" && !strings.HasPrefix(trimmed, "--") {
			statements = append(statements, trimmed)
		}
	}
	applied := strings.Join(statements, "\n")
	for _, fragment := range []string{
		"CREATE TABLE public.tasks",
		"user_id text NOT NULL",
		"due_date date",
		"due_at timestamptz",
		"time_zone text",
		"ENABLE ROW LEVEL SECURITY",
	} {
		if !strings.Contains(applied, fragment) {
			t.Errorf("migration 006 is missing %q", fragment)
		}
	}
	if strings.Contains(applied, "ALTER TABLE public.applications") ||
		strings.Contains(applied, "DROP ") || strings.Contains(applied, "DELETE FROM") {
		t.Error("migration 006 must not change or remove existing data")
	}
	if !strings.Contains(body, "Rollback:") {
		t.Error("migration 006 must document how to reverse it")
	}
}

// migrationStatements returns the migration body with comment lines
// removed, so policy prose about DROP/DELETE cannot trip the additive-schema
// checks the way the rollback notes legitimately do.
func migrationStatements(t *testing.T, version uint) string {
	t.Helper()
	source, err := NewSource()
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	reader, _, err := source.ReadUp(version)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	statements := make([]string, 0)
	for _, line := range strings.Split(string(data), "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" && !strings.HasPrefix(trimmed, "--") {
			statements = append(statements, trimmed)
		}
	}
	return strings.Join(statements, "\n")
}

func rawMigration(t *testing.T, version uint) string {
	t.Helper()
	source, err := NewSource()
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	reader, _, err := source.ReadUp(version)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// TestReminderJobsMigrationIsAdditive pins the Phase 4 durable-reminder core
// schema: one new owner-scoped table with an explicit delivery state machine
// and one job per task, both documented as reversible, and no change to any
// existing table or row.
func TestReminderJobsMigrationIsAdditive(t *testing.T) {
	applied := migrationStatements(t, 7)
	for _, fragment := range []string{
		"CREATE TABLE public.reminder_jobs",
		"user_id text",
		"task_id uuid",
		"due_at timestamptz",
		"delivery_state text",
		"'scheduled'", "'leased'", "'accepted'", "'delivered'", "'uncertain'",
		"'failed'", "'exhausted'", "'suppressed'",
		"CREATE UNIQUE INDEX reminder_jobs_task_idx",
		"ENABLE ROW LEVEL SECURITY",
	} {
		if !strings.Contains(applied, fragment) {
			t.Errorf("migration 007 is missing %q", fragment)
		}
	}
	if strings.Contains(applied, "ALTER TABLE public.applications") ||
		strings.Contains(applied, "ALTER TABLE public.tasks") ||
		strings.Contains(applied, "DROP ") || strings.Contains(applied, "DELETE FROM") {
		t.Error("migration 007 must not change or remove existing data")
	}
	if !strings.Contains(rawMigration(t, 7), "Rollback:") {
		t.Error("migration 007 must document how to reverse it")
	}
}

// TestUserNotificationSettingsMigrationIsAdditive pins the Phase 4 notification
// control schema: one new owner-scoped table, defaulting reminders on,
// forward-only and reversible by dropping the table.
func TestUserNotificationSettingsMigrationIsAdditive(t *testing.T) {
	applied := migrationStatements(t, 8)
	for _, fragment := range []string{
		"CREATE TABLE public.user_notification_settings",
		"user_id text PRIMARY KEY",
		"reminders_enabled boolean NOT NULL DEFAULT true",
		"ENABLE ROW LEVEL SECURITY",
	} {
		if !strings.Contains(applied, fragment) {
			t.Errorf("migration 008 is missing %q", fragment)
		}
	}
	if strings.Contains(applied, "ALTER TABLE public.applications") ||
		strings.Contains(applied, "ALTER TABLE public.tasks") ||
		strings.Contains(applied, "DROP ") || strings.Contains(applied, "DELETE FROM") {
		t.Error("migration 008 must not change or remove existing data")
	}
	if !strings.Contains(rawMigration(t, 8), "Rollback:") {
		t.Error("migration 008 must document how to reverse it")
	}
}

// TestResendCorrelationMigrationIsAdditive pins the Resend-wiring schema:
// versioned delivery evidence, the provider email mapping, and the event
// ledger. The delivery-state check is extended, not dropped, and nothing
// drops or deletes rows.
func TestResendCorrelationMigrationIsAdditive(t *testing.T) {
	applied := migrationStatements(t, 9)
	for _, fragment := range []string{
		"delivery_version int NOT NULL DEFAULT 0",
		"CHECK (delivery_version >= 0)",
		"ADD COLUMN provider_email_id text",
		"ADD COLUMN provider_version int",
		"'permanent_failed'",
		"CREATE TABLE public.reminder_email_events",
		"provider_event_id text PRIMARY KEY",
		"ENABLE ROW LEVEL SECURITY",
	} {
		if !strings.Contains(applied, fragment) {
			t.Errorf("migration 009 is missing %q", fragment)
		}
	}
	if !strings.Contains(applied, "'permanent_failed'") {
		t.Error("migration 009 must add the permanent_failed delivery state")
	}
	if strings.Contains(applied, "DROP TABLE") || strings.Contains(applied, "DELETE FROM") {
		t.Error("migration 009 must not remove or rewrite existing data")
	}
	if !strings.Contains(rawMigration(t, 9), "Rollback:") {
		t.Error("migration 009 must document how to reverse it")
	}
}

// TestReminderRetentionCleanupMigrationIsAdditive pins the Phase 4 retention
// cleanup schema: a unique index on provider-emails (job_id, delivery_version)
// and a cleanup function. No data is dropped or rewritten by the migration
// itself (the function contains DELETEs for scheduled cleanup, which is its
// purpose).
func TestReminderRetentionCleanupMigrationIsAdditive(t *testing.T) {
	applied := migrationStatements(t, 11)
	for _, fragment := range []string{
		"CREATE UNIQUE INDEX reminder_provider_emails_job_version_idx",
		"CREATE OR REPLACE FUNCTION public.cleanup_reminder_retention",
		"RETURNS TABLE(deleted_payloads int, deleted_pending_events int)",
	} {
		if !strings.Contains(applied, fragment) {
			t.Errorf("migration 011 is missing %q", fragment)
		}
	}
	// The migration creates a cleanup function that contains DELETE statements
	// for its operational purpose. The migration itself must not contain
	// top-level DROP TABLE or DELETE FROM that would execute immediately.
	raw := rawMigration(t, 11)
	for _, forbidden := range []string{
		"DROP TABLE",
		"DROP COLUMN",
	} {
		if strings.Contains(raw, forbidden) {
			t.Errorf("migration 011 must not contain top-level %q", forbidden)
		}
	}
	// Top-level DELETE FROM (outside function body) would be a data rewrite.
	// We allow DELETE inside the function body (which starts with $$).
	if idx := strings.Index(raw, "DELETE FROM"); idx >= 0 && idx < strings.Index(raw, "$$") {
		t.Error("migration 011 must not contain top-level DELETE FROM")
	}
	if !strings.Contains(raw, "Rollback:") {
		t.Error("migration 011 must document how to reverse it")
	}
}

func TestApplyIsIdempotentAgainstConfiguredDatabase(t *testing.T) {
	databaseURL := os.Getenv("HIRELOOP_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set HIRELOOP_TEST_DATABASE_URL to exercise migrations against the configured database")
	}
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatal("open configured test database")
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatal("connect to configured test database")
	}
	if err := Apply(db); err != nil {
		t.Fatalf("first migration application: %v", err)
	}
	if err := Apply(db); err != nil {
		t.Fatalf("repeat migration application: %v", err)
	}
}
