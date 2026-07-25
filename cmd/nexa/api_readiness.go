package main

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/platform/agentclient"
	"github.com/nexa-panel/nexa-panel/internal/platform/persistence"
	"github.com/uptrace/bun"
)

// apiReadiness checks the local dependencies required for safe request
// handling: a reachable control-panel database, a schema this binary's
// migrations have actually been applied to, and an authenticated
// privileged-agent request. Opening the socket alone is insufficient because a
// missing or stale credential makes every agent-backed feature unavailable, and
// a reachable database at the wrong schema revision serves errors from every
// module — which is also exactly what a half-completed self-update leaves
// behind, so the update's activation health gate depends on this check.
// It deliberately performs no mutations and relies on the caller's deadline.
func apiReadiness(database *bun.DB, agentCheck func(context.Context) error) func(context.Context) error {
	return func(ctx context.Context) error {
		if err := database.PingContext(ctx); err != nil {
			return fmt.Errorf("control-panel database: %w", err)
		}
		if err := persistence.AssertMigrationsCurrent(ctx, database); err != nil {
			return fmt.Errorf("control-panel schema: %w", err)
		}
		if err := agentCheck(ctx); err != nil {
			return fmt.Errorf("privileged agent: %w", err)
		}
		return nil
	}
}

func authenticatedAgentReadiness(agentSocket, agentToken string) func(context.Context) error {
	agent := agentclient.New(
		agentSocket,
		agentToken,
		"readiness",
		"privileged agent readiness check failed",
		5*time.Second,
		agentclient.WithResponseLimit(16*1024),
	)
	return func(ctx context.Context) error {
		var health struct {
			Status string `json:"status"`
		}
		if err := agent.JSON(ctx, http.MethodGet, "/v1/health", nil, &health); err != nil {
			return err
		}
		if health.Status != "ok" {
			return fmt.Errorf("unexpected health status %q", health.Status)
		}
		return nil
	}
}
