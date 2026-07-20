package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/platform/jobs"
	"github.com/nexa-panel/nexa-panel/internal/platform/persistence"
)

// runBackup implements `nexa backup trigger --plan <id>`: the command a plan's
// systemd timer runs. It opens the same control-plane state database the API
// serves from and enqueues a `backup.run` job; the running API worker claims it
// on its next poll and executes it. This keeps scheduled and manual "Back up
// now" runs on one execution path with no extra network or auth surface.
func runBackup(args []string, logger *slog.Logger) error {
	if len(args) == 0 || args[0] != "trigger" {
		return errors.New("usage: nexa backup trigger --plan <id> [--state PATH]")
	}
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
