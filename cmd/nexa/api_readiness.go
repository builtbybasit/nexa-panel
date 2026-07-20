package main

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/platform/agentclient"
	"github.com/uptrace/bun"
)

// apiReadiness checks the two local dependencies required for safe request
// handling: durable control-plane state and an authenticated privileged-agent
// request. Opening the socket alone is insufficient because a missing or stale
// credential makes every agent-backed feature unavailable.
// It deliberately performs no mutations and relies on the caller's deadline.
func apiReadiness(database *bun.DB, agentCheck func(context.Context) error) func(context.Context) error {
	return func(ctx context.Context) error {
		if err := database.PingContext(ctx); err != nil {
			return fmt.Errorf("control-plane database: %w", err)
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
