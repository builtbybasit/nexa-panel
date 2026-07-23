package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"log/slog"
	"os"
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
	if err := persistence.RunMigrations(context.Background(), database); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	defer database.Close()
	ctx := context.Background()
	if _, err := jobs.NewEnqueuer(ctx, database); err != nil {
		t.Fatal(err)
	}
	// backup_plans is created by the central migrations; seed a valid row (and
	// the backup_accounts row its account_id foreign key requires).
	now := time.Now().UTC()
	if _, err := database.ExecContext(
		ctx,
		"INSERT INTO backup_accounts (id, name, type, path, config_json, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)",
		"acct_1", "Local", "local", "/backups", "{}", now, now,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(
		ctx,
		`INSERT INTO backup_plans (id, name, account_id, copies_limit, site_ids, database_ids, schedule, enabled, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, 1, ?, ?)`,
		"bkplan_1", "Nightly", "acct_1", 5, `["site_a"]`, `[]`, "daily", now, now,
	); err != nil {
		t.Fatal(err)
	}
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

func TestBackupSystemEnqueuesOneJobResolvingAccountByName(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "control.db")
	database, err := persistence.Open(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := persistence.RunMigrations(context.Background(), database); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	defer database.Close()
	ctx := context.Background()
	if _, err := jobs.NewEnqueuer(ctx, database); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, err := database.ExecContext(
		ctx,
		"INSERT INTO backup_accounts (id, name, type, path, config_json, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)",
		"acct_1", "Offsite", "s3", "bucket", "{}", now, now,
	); err != nil {
		t.Fatal(err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	// Resolve by name, then a second run in the same minute must deduplicate.
	arguments := []string{"system", "--account", "Offsite", "--state", statePath}
	if err := runBackup(arguments, logger); err != nil {
		t.Fatal(err)
	}
	if err := runBackup(arguments, logger); err != nil {
		t.Fatal(err)
	}
	var queued int
	if err := database.QueryRowContext(ctx, "SELECT COUNT(*) FROM jobs WHERE kind = 'backup.system'").Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if queued != 1 {
		t.Fatalf("backup.system jobs = %d, want 1", queued)
	}
}

func TestBackupSystemRestoreExtractsAndRefusesToClobber(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "nexa-panel-system.tar.gz")
	writeSystemFixture(t, archive, map[string]string{"control.db": "state", "master.key": "secret"})

	stateDest := filepath.Join(dir, "control.db")
	keyDest := filepath.Join(dir, "master.key")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	if err := runBackup([]string{"system-restore", "--archive", archive, "--state", stateDest, "--master-key", keyDest}, logger); err != nil {
		t.Fatalf("system-restore: %v", err)
	}
	for _, dest := range []string{stateDest, keyDest} {
		info, err := os.Stat(dest)
		if err != nil {
			t.Fatalf("restored file missing: %v", err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("%s perms = %o, want 600", dest, info.Mode().Perm())
		}
	}

	// A second restore over the now-populated control.db must be refused.
	if err := runBackup([]string{"system-restore", "--archive", archive, "--state", stateDest, "--master-key", keyDest}, logger); err == nil {
		t.Fatal("expected system-restore to refuse clobbering an existing control.db")
	}
	// ...unless --force is given.
	if err := runBackup([]string{"system-restore", "--archive", archive, "--state", stateDest, "--master-key", keyDest, "--force"}, logger); err != nil {
		t.Fatalf("forced system-restore: %v", err)
	}
}

func writeSystemFixture(t *testing.T, archivePath string, members map[string]string) {
	t.Helper()
	var buffer bytes.Buffer
	gzipWriter := gzip.NewWriter(&buffer)
	tarWriter := tar.NewWriter(gzipWriter)
	for name, content := range members {
		if err := tarWriter.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tarWriter.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(archivePath, buffer.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}
