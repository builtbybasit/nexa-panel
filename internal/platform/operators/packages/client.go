package packages

import (
	"context"
	"net/http"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/platform/agentclient"
)

// UnixClient is the control-panel-side Operator implementation. It talks to the
// privileged agent over its Unix socket, authenticating with the shared token.
type UnixClient struct {
	client *agentclient.Client
}

// NewUnixClient dials the agent socket. The long timeout accommodates apt runs.
func NewUnixClient(socketPath, tokenPath string) *UnixClient {
	return &UnixClient{client: agentclient.New(socketPath, tokenPath, "applications", "node agent rejected application operation", 30*time.Minute)}
}

func (c *UnixClient) Catalog(ctx context.Context) ([]CatalogEntry, error) {
	var result struct {
		Entries []CatalogEntry `json:"entries"`
	}
	err := c.call(ctx, http.MethodGet, "/v1/packages/available", nil, &result)
	return result.Entries, err
}

func (c *UnixClient) Discover(ctx context.Context) ([]InstalledPackage, error) {
	var result struct {
		Items []InstalledPackage `json:"items"`
	}
	err := c.call(ctx, http.MethodGet, "/v1/packages/installed", nil, &result)
	return result.Items, err
}

func (c *UnixClient) Plan(ctx context.Context, change Change) (Plan, error) {
	var result Plan
	err := c.call(ctx, http.MethodPost, "/v1/packages/plan", change, &result)
	return result, err
}

func (c *UnixClient) Apply(ctx context.Context, plan Plan) (Observation, error) {
	var result Observation
	err := c.call(ctx, http.MethodPost, "/v1/packages/apply", plan, &result)
	return result, err
}

func (c *UnixClient) call(ctx context.Context, method, path string, input, output any) error {
	return c.client.JSON(ctx, method, path, input, output)
}
