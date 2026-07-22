package backups

import (
	"context"
	"net/http"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/platform/agentclient"
)

// UnixClient is the control-plane side of the operator: it speaks HTTP over the
// agent's Unix socket. It mirrors the pattern used by every other operator
// (see operators/postgres/client.go).
type UnixClient struct {
	client *agentclient.Client
}

func NewUnixClient(socketPath, tokenPath string) *UnixClient {
	return &UnixClient{client: agentclient.New(
		socketPath, tokenPath, "backups", "node agent rejected backup operation", 30*time.Minute,
	)}
}

func (c *UnixClient) TestAccount(ctx context.Context, account Account) (TestResult, error) {
	var result TestResult
	err := c.call(ctx, http.MethodPost, "/v1/backups/accounts/test", account, &result)
	return result, err
}

func (c *UnixClient) Run(ctx context.Context, request RunRequest) (Manifest, error) {
	var manifest Manifest
	err := c.call(ctx, http.MethodPost, "/v1/backups/run", request, &manifest)
	return manifest, err
}

func (c *UnixClient) RunSystem(ctx context.Context, request SystemRunRequest) (Manifest, error) {
	var manifest Manifest
	err := c.call(ctx, http.MethodPost, "/v1/backups/system/run", request, &manifest)
	return manifest, err
}

func (c *UnixClient) Restore(ctx context.Context, request RestoreRequest) error {
	return c.call(ctx, http.MethodPost, "/v1/backups/restore", request, nil)
}

func (c *UnixClient) DeleteCopy(ctx context.Context, request DeleteRequest) error {
	return c.call(ctx, http.MethodPost, "/v1/backups/copies/delete", request, nil)
}

func (c *UnixClient) InstallSchedule(ctx context.Context, spec ScheduleSpec) error {
	return c.call(ctx, http.MethodPost, "/v1/backups/schedules/install", spec, nil)
}

func (c *UnixClient) RemoveSchedule(ctx context.Context, planID string) error {
	return c.call(ctx, http.MethodPost, "/v1/backups/schedules/remove", map[string]string{"planId": planID}, nil)
}

func (c *UnixClient) call(ctx context.Context, method, path string, input, output any) error {
	return c.client.JSON(ctx, method, path, input, output)
}
