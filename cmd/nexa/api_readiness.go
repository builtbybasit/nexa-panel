package main

import (
	"context"
	"fmt"
	"net"

	"github.com/uptrace/bun"
)

// apiReadiness checks the two local dependencies required for safe request
// handling: durable control-plane state and the privileged agent transport.
// It deliberately performs no mutations and relies on the caller's deadline.
func apiReadiness(database *bun.DB, agentSocket string) func(context.Context) error {
	return func(ctx context.Context) error {
		if err := database.PingContext(ctx); err != nil {
			return fmt.Errorf("control-plane database: %w", err)
		}
		connection, err := (&net.Dialer{}).DialContext(ctx, "unix", agentSocket)
		if err != nil {
			return fmt.Errorf("privileged agent: %w", err)
		}
		return connection.Close()
	}
}
