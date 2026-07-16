package persistence

import (
	"context"
	"path/filepath"
	"testing"
)

func TestOpenAndMigrateAreIdempotent(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "nested", "control.db"))
	if err != nil {
		t.Fatalf("Open returned an error: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })

	migrations := []string{`CREATE TABLE example (id TEXT PRIMARY KEY)`}
	if err := Migrate(context.Background(), database, "example", migrations); err != nil {
		t.Fatalf("first Migrate returned an error: %v", err)
	}
	if err := Migrate(context.Background(), database, "example", migrations); err != nil {
		t.Fatalf("second Migrate returned an error: %v", err)
	}

	var count int
	if err := database.NewSelect().Table("schema_migrations").ColumnExpr("count(*)").Where("module = ?", "example").Scan(context.Background(), &count); err != nil {
		t.Fatalf("query migration count: %v", err)
	}
	if count != 1 {
		t.Fatalf("migration count = %d, want 1", count)
	}
}
