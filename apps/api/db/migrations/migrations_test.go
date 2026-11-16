package migrations

import (
	"context"
	"database/sql"
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
