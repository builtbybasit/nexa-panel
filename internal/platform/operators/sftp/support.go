package sftp

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/platform/agentclient"
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
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".nexa-sftp-*")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer os.Remove(name)
	if _, err := temporary.Write(content); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Chmod(mode); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
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
