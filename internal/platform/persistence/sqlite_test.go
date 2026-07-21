package persistence

import (
	"context"
	"path/filepath"
	"testing"
)

func TestOpenCreatesNestedDirectoriesAndUsableDatabase(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "nested", "control.db"))
	if err != nil {
		t.Fatalf("Open returned an error: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })

	if err := database.PingContext(context.Background()); err != nil {
		t.Fatalf("database is not usable after Open: %v", err)
	}

	// Re-opening the same path must succeed (idempotent, no schema assumptions).
	again, err := Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatalf("second Open returned an error: %v", err)
	}
	_ = again.Close()
}
