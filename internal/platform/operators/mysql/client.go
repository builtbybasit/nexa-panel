package mysql

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
	return &UnixClient{client: agentclient.New(socketPath, tokenPath, "MySQL-family", "node agent rejected MySQL-family operation", 30*time.Minute)}
}

func (c *UnixClient) Discover(ctx context.Context) (*Engine, error) {
	var result struct {
		Engine *Engine `json:"engine"`
	}
	err := c.call(ctx, http.MethodGet, "/v1/mysql-family/engine", nil, &result)
	return result.Engine, err
}

func (c *UnixClient) Sizes(ctx context.Context) (map[string]int64, error) {
	var result struct {
		Sizes map[string]int64 `json:"sizes"`
	}
	err := c.call(ctx, http.MethodGet, "/v1/mysql-family/sizes", nil, &result)
	return result.Sizes, err
}

func (c *UnixClient) Plan(ctx context.Context, change Change) (Plan, error) {
	var result Plan
	err := c.call(ctx, http.MethodPost, "/v1/mysql-family/plan", change, &result)
	return result, err
}

func (c *UnixClient) Apply(ctx context.Context, execution Execution) (Observation, error) {
	var result Observation
	err := c.call(ctx, http.MethodPost, "/v1/mysql-family/apply", execution, &result)
	return result, err
}

func (c *UnixClient) call(ctx context.Context, method, path string, input, output any) error {
	return c.client.JSON(ctx, method, path, input, output)
}
