package migrations

import (
	"io"
	"strings"
	"testing"
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
