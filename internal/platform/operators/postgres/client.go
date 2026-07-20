package postgres

import (
	"context"
	"net/http"
	"net/url"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/platform/agentclient"
)

type UnixClient struct {
	client *agentclient.Client
}

func NewUnixClient(socketPath, tokenPath string) *UnixClient {
	return &UnixClient{client: agentclient.New(socketPath, tokenPath, "PostgreSQL", "node agent rejected PostgreSQL operation", 30*time.Minute)}
}

func (c *UnixClient) Discover(ctx context.Context) ([]Instance, error) {
	var result struct {
		Items []Instance `json:"items"`
	}
	err := c.call(ctx, http.MethodGet, "/v1/postgresql/instances", nil, &result)
	return result.Items, err
}

func (c *UnixClient) Sizes(ctx context.Context, instanceID string) (map[string]int64, error) {
	var result struct {
		Sizes map[string]int64 `json:"sizes"`
	}
	err := c.call(ctx, http.MethodGet, "/v1/postgresql/sizes?instanceId="+url.QueryEscape(instanceID), nil, &result)
	return result.Sizes, err
}

func (c *UnixClient) Plan(ctx context.Context, change Change) (Plan, error) {
	var plan Plan
	err := c.call(ctx, http.MethodPost, "/v1/postgresql/plan", change, &plan)
	return plan, err
}

func (c *UnixClient) Apply(ctx context.Context, execution Execution) (Observation, error) {
	var observation Observation
	err := c.call(ctx, http.MethodPost, "/v1/postgresql/apply", execution, &observation)
	return observation, err
}

func (c *UnixClient) call(ctx context.Context, method, path string, input, output any) error {
	return c.client.JSON(ctx, method, path, input, output)
}
