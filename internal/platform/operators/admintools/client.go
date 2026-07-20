package admintools

import (
	"context"
	"net/http"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/platform/agentclient"
)

type UnixClient struct {
	client *agentclient.Client
}

func NewUnixClient(socketPath, tokenPath string) *UnixClient {
	return &UnixClient{client: agentclient.New(socketPath, tokenPath, "admin tool", "node agent rejected admin tool operation", 10*time.Minute)}
}

func (c *UnixClient) Discover(ctx context.Context) ([]Tool, error) {
	var result struct {
		Items []Tool `json:"items"`
	}
	err := c.call(ctx, http.MethodGet, "/v1/admin-tools", nil, &result)
	return result.Items, err
}

func (c *UnixClient) Plan(ctx context.Context, change Change) (Plan, error) {
	var result Plan
	err := c.call(ctx, http.MethodPost, "/v1/admin-tools/plan", change, &result)
	return result, err
}

func (c *UnixClient) Apply(ctx context.Context, execution Execution) (Observation, error) {
	var result Observation
	err := c.call(ctx, http.MethodPost, "/v1/admin-tools/apply", execution, &result)
	return result, err
}

func (c *UnixClient) call(ctx context.Context, method, path string, input, output any) error {
	return c.client.JSON(ctx, method, path, input, output)
}
