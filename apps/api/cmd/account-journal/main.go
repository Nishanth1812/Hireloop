// Command account-journal exports the deletion journal from a live database
// and imports/reconciles it after a database restore.
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"hireloop/api/db/migrations"
	"hireloop/api/internal/accounts"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		log.Fatal(err)
	}
}

func parseArgs(args []string) (action, path string, err error) {
	if len(args) != 2 || (args[0] != "export" && args[0] != "reconcile") || strings.TrimSpace(args[1]) == "" {
		return "", "", errors.New("usage: account-journal <export|reconcile> <journal.json>")
	}
	return args[0], strings.TrimSpace(args[1]), nil
}

func run(args []string) error {
	action, path, err := parseArgs(args)
	if err != nil {
		return err
	}
	databaseURL := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if databaseURL == "" {
		return errors.New("DATABASE_URL is required")
	}
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return errors.New("could not open configured database")
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		return errors.New("could not connect to configured database")
	}
	store := &accounts.Store{DB: db}

	switch action {
	case "export":
		entries, err := store.Journal(ctx)
		if err != nil {
			return errors.New("could not export deletion journal")
		}
		data, err := accounts.MarshalJournal(entries)
		if err != nil {
			return errors.New("could not encode deletion journal")
		}
		file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
		if err != nil {
			return errors.New("could not create journal file")
		}
		if err := file.Chmod(0o600); err != nil {
			_ = file.Close()
			return errors.New("could not restrict journal file permissions")
		}
		if _, err := file.Write(data); err != nil {
			_ = file.Close()
			return errors.New("could not write journal file")
		}
		if err := file.Close(); err != nil {
			return errors.New("could not close journal file")
		}
		fmt.Printf("Exported %d deletion record(s). Store the journal outside database backups.\n", len(entries))
	case "reconcile":
		data, err := os.ReadFile(path)
		if err != nil {
			return errors.New("could not read journal file")
		}
		entries, err := accounts.UnmarshalJournal(data)
		if err != nil {
			return errors.New("journal file is invalid")
		}
		if err := migrations.Apply(db); err != nil {
			return errors.New("could not apply migrations to restored database")
		}
		imported, err := store.ImportJournal(ctx, entries)
		if err != nil {
			return errors.New("could not import deletion journal")
		}
		reconciled, err := store.Reconcile(ctx)
		if err != nil {
			return errors.New("could not reconcile deleted accounts")
		}
		fmt.Printf("Imported %d journal record(s) and reconciled %d deletion(s).\n", imported, reconciled)
	}
	return nil
}
