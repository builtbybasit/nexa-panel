package nodes

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
	return &UnixClient{client: agentclient.New(
		socketPath, tokenPath, "node", "node agent rejected the operation", 8*time.Second,
		agentclient.WithResponseLimit(64*1024),
	)}
}

func (c *UnixClient) Plan(ctx context.Context, change Change) (Plan, error) {
	var plan Plan
	err := c.call(ctx, http.MethodPost, "/v1/node/probe/plan", change, &plan)
	return plan, err
}

func (c *UnixClient) Apply(ctx context.Context, plan Plan) (Snapshot, error) {
	var observation Snapshot
	err := c.call(ctx, http.MethodPost, "/v1/node/probe/apply", plan, &observation)
	return observation, err
}

func (c *UnixClient) Observe(ctx context.Context) (Snapshot, error) {
	var observation Snapshot
	err := c.call(ctx, http.MethodGet, "/v1/node/probe/observe", nil, &observation)
	return observation, err
}

func (c *UnixClient) Rollback(ctx context.Context, plan Plan) (Snapshot, error) {
	var observation Snapshot
	err := c.call(ctx, http.MethodPost, "/v1/node/probe/rollback", plan, &observation)
	return observation, err
}

func (c *UnixClient) call(ctx context.Context, method, path string, input, output any) error {
	return c.client.JSON(ctx, method, path, input, output)
}
