package certificates

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
		socketPath, tokenPath, "certificate", "node agent rejected certificate operation", 5*time.Minute,
		agentclient.WithResponseLimit(64*1024),
	)}
}

func (c *UnixClient) Plan(ctx context.Context, request Request) (Plan, error) {
	var plan Plan
	err := c.call(ctx, "/v1/certificates/plan", request, &plan)
	return plan, err
}

func (c *UnixClient) Execute(ctx context.Context, plan Plan) (Observation, error) {
	var result Observation
	err := c.call(ctx, "/v1/certificates/execute", plan, &result)
	return result, err
}

func (c *UnixClient) call(ctx context.Context, path string, input, output any) error {
	return c.client.JSON(ctx, http.MethodPost, path, input, output)
}
