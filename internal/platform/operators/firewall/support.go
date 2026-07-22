package firewall

import (
	"context"
	"net/http"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/platform/agentclient"
)

// UnixClient is the control-plane-side Operator. It talks to the privileged
// agent over its Unix socket, authenticating with the shared token.
type UnixClient struct {
	client *agentclient.Client
}

func NewUnixClient(socketPath, tokenPath string) *UnixClient {
	return &UnixClient{client: agentclient.New(socketPath, tokenPath, "firewall", "node agent rejected the firewall operation", 30*time.Second)}
}

func (c *UnixClient) Discover(ctx context.Context) (Status, error) {
	var status Status
	err := c.client.JSON(ctx, http.MethodGet, "/v1/firewall/status", nil, &status)
	return status, err
}

func (c *UnixClient) Apply(ctx context.Context, change Change) (Status, error) {
	var status Status
	err := c.client.JSON(ctx, http.MethodPost, "/v1/firewall/apply", change, &status)
	return status, err
}
