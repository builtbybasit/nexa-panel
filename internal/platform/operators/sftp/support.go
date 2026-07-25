package sftp

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/platform/agentclient"
	"github.com/nexa-panel/nexa-panel/internal/platform/fsutil"
)

// UnixClient is the control-plane-side Operator. It talks to the privileged
// agent over its Unix socket, authenticating with the shared token.
type UnixClient struct {
	client *agentclient.Client
}

func NewUnixClient(socketPath, tokenPath string) *UnixClient {
	return &UnixClient{client: agentclient.New(socketPath, tokenPath, "sftp", "node agent rejected the SFTP operation", 30*time.Second)}
}

func (c *UnixClient) Apply(ctx context.Context, request Request) (Observation, error) {
	var observation Observation
	err := c.client.JSON(ctx, http.MethodPost, "/v1/sftp/apply", request, &observation)
	return observation, err
}

// atomicWrite publishes a file by writing a sibling temp file and renaming it,
// so a reader (or sshd re-reading the drop-in) never sees a half-written config.
func atomicWrite(path string, content []byte, mode os.FileMode) error {
	return fsutil.Write(path, content, mode)
}

func commandError(output []byte, err error) error {
	message := strings.TrimSpace(string(output))
	if len(message) > 500 {
		message = message[:500]
	}
	if message == "" {
		return err
	}
	return fmt.Errorf("%w: %s", err, message)
}
