package main

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/platform/jobs"
	"github.com/nexa-panel/nexa-panel/internal/platform/persistence"
)

func TestBackupTriggerOnlyEnqueuesAndDeduplicatesTimerRetry(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "control.db")
	database, err := persistence.Open(statePath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	if _, err := jobs.NewEnqueuer(ctx, database); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `CREATE TABLE backup_plans (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		site_ids TEXT NOT NULL,
		database_ids TEXT NOT NULL,
		enabled INTEGER NOT NULL
	)`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(
		ctx,
		"INSERT INTO backup_plans (id, name, site_ids, database_ids, enabled) VALUES (?, ?, ?, ?, 1)",
		"bkplan_1", "Nightly", `["site_a"]`, `[]`,
	); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	expired := now.Add(-time.Minute)
	if _, err := database.ExecContext(
		ctx, `INSERT INTO jobs (
		kind, title, state, progress, request_json, recovery_policy, scope_site_ids,
		lease_token, lease_expires_at, created_at, updated_at, started_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"host.mutation", "Existing work", jobs.StateRunning, 50, `{}`, jobs.RecoveryFail, `[]`,
		"expired", expired, now, now, now,
	); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	arguments := []string{"trigger", "--plan", "bkplan_1", "--state", statePath}
	if err := runBackup(arguments, logger); err != nil {
		t.Fatal(err)
	}
	if err := runBackup(arguments, logger); err != nil {
		t.Fatal(err)
	}
	var scheduled int
	if err := database.QueryRowContext(ctx, "SELECT COUNT(*) FROM jobs WHERE kind = 'backup.run'").Scan(&scheduled); err != nil {
		t.Fatal(err)
	}
	if scheduled != 1 {
		t.Fatalf("scheduled backup jobs = %d, want 1", scheduled)
	}
	var existingState string
	if err := database.QueryRowContext(ctx, "SELECT state FROM jobs WHERE kind = 'host.mutation'").Scan(&existingState); err != nil {
		t.Fatal(err)
	}
	if jobs.State(existingState) != jobs.StateRunning {
		t.Fatalf("enqueue-only trigger changed existing job to %q", existingState)
	}
}
