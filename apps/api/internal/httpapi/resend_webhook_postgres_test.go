package httpapi

import (
	"context"
	"crypto/rand"
	"database/sql"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"hireloop/api/db/migrations"
	"hireloop/api/internal/reminders"
	"hireloop/api/internal/resend"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// Opt-in real-Postgres receiver test: set HIRELOOP_TEST_DATABASE_URL to run
// it; otherwise it skips. The route is exercised end-to-end against the real
// store: a signed event applies to the job it correlates to, and a replay of
// the same signed event is a no-op (persistent dedup, robust across
// reschedules).

func integrationDB(t *testing.T) *sql.DB {
	t.Helper()
	url := os.Getenv("HIRELOOP_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set HIRELOOP_TEST_DATABASE_URL to run SQL-backed webhook tests")
	}
	db, err := sql.Open("pgx", url)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("connect to database: %v", err)
	}
	if err := migrations.Apply(db); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	return db
}

func randomOwner(t *testing.T) string {
	t.Helper()
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("generate owner id: %v", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func TestWebhookRouteAppliesEventsAgainstRealStore(t *testing.T) {
	db := integrationDB(t)
	owner := randomOwner(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		for _, stmt := range []string{
			`DELETE FROM reminder_email_events WHERE job_id IN (SELECT id FROM reminder_jobs WHERE user_id = $1)`,
			`DELETE FROM reminder_email_events WHERE provider_email_id IN (SELECT provider_email_id FROM reminder_jobs WHERE user_id = $1) AND job_id IS NULL`,
			`DELETE FROM reminder_jobs WHERE user_id = $1`,
			`DELETE FROM tasks WHERE user_id = $1`,
			`DELETE FROM applications WHERE user_id = $1`,
		} {
			if _, err := db.ExecContext(cleanupCtx, stmt, owner); err != nil {
				t.Errorf("cleanup: %v", err)
			}
		}
	})

	store := &reminders.Store{DB: db}
	var appID string
	if err := db.QueryRowContext(ctx,
		`INSERT INTO applications (user_id, company, role, status) VALUES ($1, 'Acme', 'Engineer', 'saved') RETURNING id`, owner).Scan(&appID); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-time.Minute)
	var taskID string
	if err := db.QueryRowContext(ctx,
		`INSERT INTO tasks (user_id, application_id, title, status, due_precision, due_at, time_zone)
		 VALUES ($1, $2, 'Interview', 'open', 'instant', $3, 'UTC') RETURNING id`, owner, appID, past).Scan(&taskID); err != nil {
		t.Fatal(err)
	}
	if err := store.Schedule(ctx, db, owner, taskID, past); err != nil {
		t.Fatal(err)
	}
	var jobID string
	if err := db.QueryRowContext(ctx, `SELECT id FROM reminder_jobs WHERE task_id = $1`, taskID).Scan(&jobID); err != nil {
		t.Fatal(err)
	}
	var version int
	if err := db.QueryRowContext(ctx, `SELECT delivery_version FROM reminder_jobs WHERE id = $1`, jobID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 1 {
		t.Fatalf("first schedule version = %d, want 1", version)
	}
	if err := store.SetProviderEmailID(ctx, jobID, version, "email-route-1"); err != nil {
		t.Fatal(err)
	}

	verifier, err := resend.NewWebhookVerifier(resendTestSecret)
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHandlerWithTasks(nil, nil, nil, nil, store, verifier)

	eventID := "msg_route_real_1"
	body := []byte(`{"type":"email.delivered","created_at":"` + time.Now().UTC().Format(time.RFC3339Nano) + `","data":{"email_id":"email-route-1"}}`)
	rec := postWebhook(t, handler, eventID, fmt.Sprint(time.Now().Unix()), resendTestSecret, body)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delivered event code = %d, want 204", rec.Code)
	}
	var state string
	if err := db.QueryRowContext(ctx, `SELECT delivery_state FROM reminder_jobs WHERE id = $1`, jobID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "delivered" {
		t.Fatalf("job state = %q, want delivered", state)
	}

	// A replay of the same signed event is a no-op: the event is already in
	// the persistent ledger.
	rec = postWebhook(t, handler, eventID, fmt.Sprint(time.Now().Unix()), resendTestSecret, body)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("replay code = %d, want 204", rec.Code)
	}
	var ledger int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM reminder_email_events WHERE provider_event_id = $1`, eventID).Scan(&ledger); err != nil {
		t.Fatal(err)
	}
	if ledger != 1 {
		t.Fatalf("ledger rows = %d, want 1", ledger)
	}
}
