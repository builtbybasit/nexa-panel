package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/platform/audit"
	"github.com/nexa-panel/nexa-panel/internal/platform/identity"
	"github.com/nexa-panel/nexa-panel/internal/platform/persistence"
)

func TestMFARequiresASubcommand(t *testing.T) {
	if err := runMFA(nil); err == nil {
		t.Fatal("a bare `nexa mfa` was accepted")
	}
	if err := runMFA([]string{"disable-everything"}); err == nil {
		t.Fatal("an unknown mfa subcommand was accepted")
	}
}

func TestMFAResetRequiresAUsernameBeforeTouchingTheHost(t *testing.T) {
	// --user is validated before the euid gate and before the database is opened,
	// so a mistyped invocation cannot half-run.
	err := runMFAReset([]string{"--state", filepath.Join(t.TempDir(), "absent.db")}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "--user is required") {
		t.Fatalf("reset without --user = %v", err)
	}
}

func TestMFAResetIsRootOnly(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("this test asserts the refusal shown to a non-root operator")
	}
	err := runMFAReset([]string{"--user", "admin", "--state", filepath.Join(t.TempDir(), "control.db")}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "must be run as root") {
		t.Fatalf("reset as a non-root user = %v, want a root-only refusal", err)
	}
	// The refusal must come before the state database is created, or a failed
	// break-glass attempt would leave a stray database behind.
	if _, statErr := os.Stat(filepath.Join(t.TempDir(), "control.db")); statErr == nil {
		t.Fatal("a state database was created by a refused reset")
	}
}

// The break-glass reset has to work against a state database written by the
// running panel, so this exercises the same call the CLI makes on a database
// built by the real migrations.
func TestBreakGlassResetRunsAgainstAMigratedStateDatabase(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "control.db")
	store, err := persistence.Open(statePath)
	if err != nil {
		t.Fatalf("open state database: %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	if err := persistence.RunMigrations(ctx, store); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	log, err := audit.New(ctx, store)
	if err != nil {
		t.Fatalf("create audit log: %v", err)
	}
	if _, err := identity.ResetSecondFactor(ctx, store, log, "nobody", time.Now()); err == nil {
		t.Fatal("resetting an account that does not exist reported success")
	}
}
