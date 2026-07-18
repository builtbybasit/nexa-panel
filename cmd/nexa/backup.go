package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"

	"github.com/nexa-panel/nexa-panel/internal/platform/audit"
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
	auditLog, err := audit.New(ctx, store)
	if err != nil {
		return fmt.Errorf("initialize audit log: %w", err)
	}
	queue, err := jobs.New(ctx, store, auditLog, logger)
	if err != nil {
		return fmt.Errorf("initialize job queue: %w", err)
	}
	// Submit validates the kind against registered handlers. This process only
	// enqueues; the running API worker owns the real handler and executes the
	// job. Register a stub so the kind is accepted here.
	queue.RegisterHandler("backup.run", func(context.Context, json.RawMessage, func(int, string) error) (any, error) {
		return nil, nil
	})
	job, err := queue.SubmitTitled(ctx, "backup.run", "Scheduled backup", map[string]string{"planId": *planID}, nil)
	if err != nil {
		return fmt.Errorf("enqueue backup: %w", err)
	}
	logger.Info("scheduled backup queued", "job", job.ID, "plan", *planID)
	return nil
}
