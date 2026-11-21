// Command worker claims due reminder jobs for Hireloop and dispatches them
// through the delivery-adapter boundary.
//
// Real sending is opt-in: the worker runs without a dispatcher (jobs stay
// scheduled instead of being leased or exhausted) unless sending is
// explicitly enabled and fully configured: RESEND_ENABLED=true with
// RESEND_API_KEY and RESEND_FROM set.
//
// Retention cleanup runs automatically every hour (configurable via
// CleanupInterval in worker.Config) even when sending is disabled, so PII
// payloads and stale pending events are bounded in all deployments.
package main

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"hireloop/api/db/migrations"
	"hireloop/api/internal/clerk"
	"hireloop/api/internal/reminders"
	"hireloop/api/internal/resend"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func main() {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		log.Fatal("DATABASE_URL is required for the reminder worker")
	}
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	defer db.Close()
	if err := migrations.Apply(db); err != nil {
		log.Fatalf("database initialization or migration failed; check database configuration and migration state: %v", err)
	}

	store := &reminders.Store{DB: db}
	dispatcher := newResendDispatcher(store)
	worker := reminders.NewWorker(store, reminders.Config{Dispatcher: dispatcher})
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if dispatcher == nil {
		log.Printf("Hireloop reminder worker running with sending disabled: set RESEND_ENABLED=true with RESEND_API_KEY and RESEND_FROM to dispatch")
	} else {
		log.Printf("Hireloop reminder worker running; real sending is enabled")
	}
	if err := worker.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatal(err)
	}
	log.Printf("Hireloop reminder worker stopped")
}

// newResendDispatcher returns the Resend dispatcher when real sending is
// explicitly enabled and fully configured; nil otherwise. A nil dispatcher
// makes the worker idle (Worker.Ready is false): a missing provider must
// not lease or exhaust jobs.
func newResendDispatcher(store *reminders.Store) reminders.Dispatcher {
	if !envBool(os.Getenv("RESEND_ENABLED")) {
		return nil
	}
	apiKey := strings.TrimSpace(os.Getenv("RESEND_API_KEY"))
	from := strings.TrimSpace(os.Getenv("RESEND_FROM"))
	if apiKey == "" || from == "" {
		log.Printf("Resend sending is enabled but incompletely configured; leaving jobs scheduled")
		return nil
	}
	sender, err := resend.NewSender(apiKey, 0)
	if err != nil {
		log.Printf("Resend sending unavailable: %v", err)
		return nil
	}
	clerkClient, err := clerk.NewBackendClient(os.Getenv("CLERK_SECRET_KEY"))
	if err != nil {
		log.Printf("Resend sending unavailable: %v", err)
		return nil
	}
	return reminders.NewResendDispatcher(store, sender, from, reminders.NewOwnerEmailResolver(store.DB, clerkClient))
}

// envBool parses a boolean env value; empty or unparsable means false.
func envBool(value string) bool {
	parsed, err := strconv.ParseBool(strings.TrimSpace(value))
	return err == nil && parsed
}
