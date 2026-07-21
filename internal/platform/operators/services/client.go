package services

import (
	"context"
	"net/http"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/platform/agentclient"
)

// UnixClient is the control-plane-side Operator implementation. It talks to the
// privileged agent over its Unix socket, authenticating with the shared token.
type UnixClient struct {
	client *agentclient.Client
}

// NewUnixClient dials the agent socket. Service actions are quick, but a restart
// of a heavy unit can take a little while, so the timeout is generous.
func NewUnixClient(socketPath, tokenPath string) *UnixClient {
	return &UnixClient{client: agentclient.New(socketPath, tokenPath, "services", "node agent rejected service operation", 5*time.Minute)}
}

func (c *UnixClient) Discover(ctx context.Context) ([]Service, error) {
	var result struct {
		Services []Service `json:"services"`
	}
	err := c.client.JSON(ctx, http.MethodGet, "/v1/services", nil, &result)
	return result.Services, err
}

func (c *UnixClient) Plan(ctx context.Context, change Change) (Plan, error) {
	var result Plan
	err := c.client.JSON(ctx, http.MethodPost, "/v1/services/plan", change, &result)
	return result, err
}

func (c *UnixClient) Apply(ctx context.Context, plan Plan) (Observation, error) {
	var result Observation
	err := c.client.JSON(ctx, http.MethodPost, "/v1/services/apply", plan, &result)
	return result, err
}
