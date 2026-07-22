package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/platform/jobs"
	backupoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/backups"
	"github.com/nexa-panel/nexa-panel/internal/platform/persistence"
)

const backupUsage = "usage: nexa backup <trigger|system|system-restore> ..."

// runBackup dispatches the backup subcommands. `trigger` and `system` enqueue a
// durable job into the same control-plane state database the API serves from
// (the running API worker claims and executes it), which keeps scheduled, manual,
// and CLI runs on one execution path with no extra network or auth surface.
// `system-restore` is self-contained: it extracts a LOCAL archive onto a fresh
// node and never touches the (encrypted) control plane, which is exactly what is
// being restored.
func runBackup(args []string, logger *slog.Logger) error {
	if len(args) == 0 {
		return errors.New(backupUsage)
	}
	switch args[0] {
	case "trigger":
		return runBackupTrigger(args, logger)
	case "system":
		return runBackupSystem(args, logger)
	case "system-restore":
		return runBackupSystemRestore(args, logger)
	default:
		return errors.New(backupUsage)
	}
}

// runBackupTrigger implements `nexa backup trigger --plan <id>`: the command a
// plan's systemd timer runs.
func runBackupTrigger(args []string, logger *slog.Logger) error {
	flags := flag.NewFlagSet("backup trigger", flag.ContinueOnError)
	planID := flags.String("plan", "", "backup plan ID to run")
	statePath := flags.String("state", envOrDefault("NEXA_STATE_DATABASE", "/var/lib/nexa-panel/control.db"), "control-plane state database path")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if *planID == "" {
		return errors.New("--plan is required")
	}

	ctx := context.Background()
	store, err := persistence.Open(*statePath)
	if err != nil {
		return fmt.Errorf("open state database: %w", err)
	}
	defer store.Close()
	enqueuer, err := jobs.NewEnqueuer(ctx, store)
	if err != nil {
		return fmt.Errorf("initialize job enqueuer: %w", err)
	}
	var planName, encodedSiteIDs, encodedDatabaseIDs string
	err = store.QueryRowContext(
		ctx,
		"SELECT name, site_ids, database_ids FROM backup_plans WHERE id = ? AND enabled = 1", strings.TrimSpace(*planID),
	).Scan(&planName, &encodedSiteIDs, &encodedDatabaseIDs)
	if err != nil {
		return fmt.Errorf("load enabled backup plan: %w", err)
	}
	siteIDs := make([]string, 0)
	if err := json.Unmarshal([]byte(encodedSiteIDs), &siteIDs); err != nil {
		return fmt.Errorf("decode backup plan site scope: %w", err)
	}
	var databaseIDs []string
	if err := json.Unmarshal([]byte(encodedDatabaseIDs), &databaseIDs); err != nil {
		return fmt.Errorf("decode backup plan database scope: %w", err)
	}
	if len(databaseIDs) != 0 {
		siteIDs = nil
	}
	// Timer retries in the same minute collapse to one durable row. The running
	// API owns execution and applies the handler's fail-on-interruption policy.
	idempotencyKey := "scheduled-backup:" + strings.TrimSpace(*planID) + ":" + time.Now().UTC().Format("20060102T1504")
	job, err := enqueuer.EnqueueTitled(ctx, "backup.run", "Back up "+planName,
		map[string]string{"planId": strings.TrimSpace(*planID)}, jobs.EnqueueOptions{
			SubmitOptions:  jobs.SubmitOptions{SiteIDs: siteIDs, IdempotencyKey: idempotencyKey},
			RecoveryPolicy: jobs.RecoveryFail,
		})
	if err != nil {
		return fmt.Errorf("enqueue backup: %w", err)
	}
	logger.Info("scheduled backup queued", "job", job.ID, "plan", *planID)
	return nil
}

// runBackupSystem implements `nexa backup system --account <id|name>`: it enqueues
// a `backup.system` job so the running API worker snapshots control.db +
// master.key and ships them off-node. This protects the panel's own state (users,
// grants, encrypted secrets, and the key that decrypts them) so a disk loss is
// recoverable. It resolves the account by ID or by name against backup_accounts.
func runBackupSystem(args []string, logger *slog.Logger) error {
	flags := flag.NewFlagSet("backup system", flag.ContinueOnError)
	account := flags.String("account", "", "backup storage account ID or name to store the panel-state backup")
	statePath := flags.String("state", envOrDefault("NEXA_STATE_DATABASE", "/var/lib/nexa-panel/control.db"), "control-plane state database path")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if strings.TrimSpace(*account) == "" {
		return errors.New("--account is required")
	}

	ctx := context.Background()
	store, err := persistence.Open(*statePath)
	if err != nil {
		return fmt.Errorf("open state database: %w", err)
	}
	defer store.Close()
	enqueuer, err := jobs.NewEnqueuer(ctx, store)
	if err != nil {
		return fmt.Errorf("initialize job enqueuer: %w", err)
	}

	var accountID string
	query := strings.TrimSpace(*account)
	err = store.QueryRowContext(
		ctx,
		"SELECT id FROM backup_accounts WHERE id = ? OR name = ?", query, query,
	).Scan(&accountID)
	if err != nil {
		return fmt.Errorf("resolve backup account %q: %w", query, err)
	}

	idempotencyKey := "system-backup:" + accountID + ":" + time.Now().UTC().Format("20060102T1504")
	job, err := enqueuer.EnqueueTitled(ctx, "backup.system", "Back up panel state",
		map[string]string{"accountId": accountID}, jobs.EnqueueOptions{
			SubmitOptions:  jobs.SubmitOptions{IdempotencyKey: idempotencyKey},
			RecoveryPolicy: jobs.RecoveryFail,
		})
	if err != nil {
		return fmt.Errorf("enqueue panel-state backup: %w", err)
	}
	logger.Info("panel-state backup queued", "job", job.ID, "account", accountID)
	return nil
}

// runBackupSystemRestore implements `nexa backup system-restore`: it extracts a
// LOCAL panel-state archive onto a fresh node. It is deliberately self-contained
// (no control-plane access) because control.db is exactly what is being restored;
// fetching the archive from the remote is a documented one-line `rclone copy`
// step (see docs/runbooks/panel-state-restore.md). It refuses to clobber an
// existing non-empty control.db unless --force is given.
func runBackupSystemRestore(args []string, logger *slog.Logger) error {
	flags := flag.NewFlagSet("backup system-restore", flag.ContinueOnError)
	archive := flags.String("archive", "", "path to a local nexa-panel-system.tar.gz archive")
	statePath := flags.String("state", "/var/lib/nexa-panel/control.db", "destination path for control.db")
	masterKeyPath := flags.String("master-key", "/var/lib/nexa-panel/master.key", "destination path for master.key")
	force := flags.Bool("force", false, "overwrite an existing non-empty control.db")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if strings.TrimSpace(*archive) == "" {
		return errors.New("--archive is required")
	}
	if !*force {
		if info, err := os.Stat(*statePath); err == nil && info.Size() > 0 {
			return fmt.Errorf("refusing to overwrite existing control database at %s (pass --force to replace it)", *statePath)
		}
	}
	if err := backupoperator.ExtractSystemArchive(*archive, *statePath, *masterKeyPath); err != nil {
		return fmt.Errorf("restore panel state: %w", err)
	}
	logger.Info("panel state restored", "state", *statePath, "masterKey", *masterKeyPath)
	logger.Info("set ownership to nexa:nexa and restart services; see docs/runbooks/panel-state-restore.md")
	return nil
}
