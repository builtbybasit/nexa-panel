package sites

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
	return &UnixClient{client: agentclient.New(socketPath, tokenPath, "site", "node agent rejected the site operation", 2*time.Minute)}
}

func (c *UnixClient) Plan(ctx context.Context, site Site) (Plan, error) {
	var plan Plan
	err := c.call(ctx, http.MethodPost, "/v1/sites/plan", site, &plan)
	return plan, err
}

func (c *UnixClient) PlanTeardown(ctx context.Context, site Site) (Plan, error) {
	var plan Plan
	err := c.call(ctx, http.MethodPost, "/v1/sites/teardown-plan", site, &plan)
	return plan, err
}

func (c *UnixClient) Apply(ctx context.Context, plan Plan) (Observation, error) {
	var observation Observation
	err := c.call(ctx, http.MethodPost, "/v1/sites/apply", plan, &observation)
	return observation, err
}

func (c *UnixClient) Rollback(ctx context.Context, plan Plan) (Observation, error) {
	var observation Observation
	err := c.call(ctx, http.MethodPost, "/v1/sites/rollback", plan, &observation)
	return observation, err
}

func (c *UnixClient) call(ctx context.Context, method, path string, input, output any) error {
	return c.client.JSON(ctx, method, path, input, output)
}
