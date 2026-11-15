package main

import (
	"database/sql"
	"errors"
	"testing"
)

func TestPrepareDatabaseStopsOnMigrationFailure(t *testing.T) {
	want := errors.New("migration failed")
	called := false
	db, err := prepareDatabase("postgres://example.invalid/test", func(*sql.DB) error { called = true; return want })
	if db != nil {
		t.Fatal("database returned after failed migration")
	}
	if !called || !errors.Is(err, want) {
		t.Fatalf("called=%v err=%v", called, err)
	}
}
