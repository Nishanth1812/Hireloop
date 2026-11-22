package main

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"hireloop/api/db/migrations"
	"hireloop/api/internal/accounts"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestParseArgsRequiresOneJournalPathAndKnownAction(t *testing.T) {
	for _, args := range [][]string{
		{},
		{"export"},
		{"reconcile", "journal.json", "extra"},
		{"delete", "journal.json"},
		{"export", "  "},
	} {
		if _, _, err := parseArgs(args); err == nil {
			t.Errorf("parseArgs(%q) accepted invalid command", args)
		}
	}

	mode, path, err := parseArgs([]string{"export", "live-journal.json"})
	if err != nil || mode != "export" || path != "live-journal.json" {
		t.Fatalf("export args = %q, %q, %v", mode, path, err)
	}
	mode, path, err = parseArgs([]string{"reconcile", "live-journal.json"})
	if err != nil || mode != "reconcile" || path != "live-journal.json" {
		t.Fatalf("reconcile args = %q, %q, %v", mode, path, err)
	}
}

func TestJournalCommandExportsAndReconcilesWithProductionStore(t *testing.T) {
	databaseURL := os.Getenv("HIRELOOP_ACCOUNTS_TEST_DATABASE_URL")
	if databaseURL == "" {
		databaseURL = os.Getenv("HIRELOOP_TEST_DATABASE_URL")
	}
	if databaseURL == "" {
		t.Skip("set HIRELOOP_TEST_DATABASE_URL to a disposable database to exercise the journal command")
	}
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatal("open disposable database")
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatal("connect to disposable database")
	}
	if _, err := db.ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS auth;
		CREATE TABLE IF NOT EXISTS auth.users (id uuid PRIMARY KEY, email text NOT NULL UNIQUE)`); err != nil {
		t.Fatalf("prepare synthetic auth schema: %v", err)
	}
	if err := migrations.Apply(db); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}

	subject := "journal_command_" + time.Now().UTC().Format("20060102150405.000000000")
	if _, err := db.ExecContext(ctx,
		`INSERT INTO applications (user_id, company, role, status) VALUES ($1, 'Synthetic', 'Engineer', 'saved')`, subject); err != nil {
		t.Fatalf("seed restored application: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO account_deletions (clerk_subject, state) VALUES ($1, 'pending')`, subject); err != nil {
		t.Fatalf("seed live deletion journal: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DELETE FROM applications WHERE user_id = $1`, subject)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM account_deletions WHERE clerk_subject = $1`, subject)
	})

	t.Setenv("DATABASE_URL", databaseURL)
	journalPath := t.TempDir() + string(os.PathSeparator) + "deletion-journal.json"
	if err := run([]string{"export", journalPath}); err != nil {
		t.Fatalf("export journal: %v", err)
	}
	data, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatalf("read exported journal: %v", err)
	}
	entries, err := accounts.UnmarshalJournal(data)
	found := false
	for _, entry := range entries {
		if entry.ClerkSubject == subject && entry.State == accounts.StatusPending {
			found = true
		}
	}
	if err != nil || !found {
		t.Fatalf("exported journal entries = %+v, %v", entries, err)
	}

	if err := run([]string{"reconcile", journalPath}); err != nil {
		t.Fatalf("reconcile journal: %v", err)
	}
	var rows int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM applications WHERE user_id = $1`, subject).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("restored application rows = %d, %v", rows, err)
	}
}
