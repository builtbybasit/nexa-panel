package selfupdate

import (
	"context"
	"net/http"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/platform/agentclient"
)

// UnixClient is the control-plane-side Operator implementation. It talks to the
// privileged agent over its unix socket, authenticating with the shared token.
type UnixClient struct {
	client *agentclient.Client
}

// NewUnixClient dials the agent socket. The long timeout accommodates a full
// release download over a slow link.
func NewUnixClient(socketPath, tokenPath string) *UnixClient {
	return &UnixClient{client: agentclient.New(socketPath, tokenPath, "self-update", "node agent rejected the self-update", 15*time.Minute)}
}

func (c *UnixClient) Latest(ctx context.Context) (Availability, error) {
	var result Availability
	err := c.client.JSON(ctx, http.MethodGet, "/v1/self-update/latest", nil, &result)
	return result, err
}

func (c *UnixClient) Apply(ctx context.Context, change Change) (Result, error) {
	var result Result
	err := c.client.JSON(ctx, http.MethodPost, "/v1/self-update/apply", change, &result)
	return result, err
}
