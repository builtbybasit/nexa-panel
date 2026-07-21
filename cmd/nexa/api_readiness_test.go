package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nexa-panel/nexa-panel/internal/platform/persistence"
)

func TestAPIReadinessRequiresAuthenticatedAgent(t *testing.T) {
	// Unix socket paths are limited to roughly 100 bytes on macOS, while
	// testing.T.TempDir can be substantially longer.
	directory, err := os.MkdirTemp("/tmp", "nexa-ready-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	database, err := persistence.Open(filepath.Join(directory, "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := persistence.RunMigrations(context.Background(), database); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })

	check := apiReadiness(database, func(context.Context) error { return nil })
	if err := check(context.Background()); err != nil {
		t.Fatalf("healthy dependencies reported not ready: %v", err)
	}
	missingToken := filepath.Join(directory, "missing.token")
	check = apiReadiness(database, authenticatedAgentReadiness(filepath.Join(directory, "agent.sock"), missingToken))
	if err := check(context.Background()); err == nil || !strings.Contains(err.Error(), "privileged agent") {
		t.Fatalf("missing credential readiness error = %v", err)
	}
}
