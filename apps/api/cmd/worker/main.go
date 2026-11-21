// Command worker claims due reminder jobs for Hireloop and dispatches them
// through the delivery-adapter boundary. Until a mail provider is integrated,
// the null dispatcher reports failure so the honest delivery states stay
// 'failed'/'exhausted'; nothing implies delivery.
package main

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"os"
	"os/signal"
	"syscall"

	"hireloop/api/db/migrations"
	"hireloop/api/internal/reminders"

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

	worker := reminders.NewWorker(&reminders.Store{DB: db}, reminders.Config{Dispatcher: reminders.NullDispatcher{}})
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log.Printf("Hireloop reminder worker running")
	if err := worker.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatal(err)
	}
	log.Printf("Hireloop reminder worker stopped")
}
