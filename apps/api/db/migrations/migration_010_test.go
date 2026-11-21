package migrations

import (
	"strings"
	"testing"
)

// TestResendDeliveryRecoveryMigrationIsAdditive pins migration 010: it adds
// recovery metadata and two new tables, and neither rewrites nor removes any
// existing row. Applied migrations 001-009 remain untouched.
func TestResendDeliveryRecoveryMigrationIsAdditive(t *testing.T) {
	applied := migrationStatements(t, 10)
	for _, fragment := range []string{
		"ALTER TABLE public.reminder_email_events",
		"ADD COLUMN target_state text",
		"ADD COLUMN resolution text NOT NULL DEFAULT 'pending'",
		"CREATE TABLE public.reminder_provider_emails",
		"provider_email_id text PRIMARY KEY",
		"job_id uuid NOT NULL REFERENCES public.reminder_jobs(id) ON DELETE CASCADE",
		"CREATE TABLE public.reminder_delivery_payloads",
		"recipient text NOT NULL",
		"text_body text NOT NULL",
		"PRIMARY KEY (job_id, delivery_version)",
		"ENABLE ROW LEVEL SECURITY",
	} {
		if !strings.Contains(applied, fragment) {
			t.Errorf("migration 010 is missing %q", fragment)
		}
	}
	for _, forbidden := range []string{
		"ALTER TABLE public.applications",
		"ALTER TABLE public.tasks",
		"DELETE FROM",
		"DROP TABLE",
		"DROP COLUMN",
	} {
		if strings.Contains(applied, forbidden) {
			t.Errorf("migration 010 must not contain %q", forbidden)
		}
	}
	if !strings.Contains(rawMigration(t, 10), "Rollback:") {
		t.Error("migration 010 must document how to reverse it")
	}
}
