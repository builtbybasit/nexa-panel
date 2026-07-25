package deploy

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/platform/agentclient"
	"github.com/nexa-panel/nexa-panel/internal/platform/fsutil"
)

// UnixClient is the control-panel-side Operator. It talks to the privileged
// agent over its Unix socket, authenticating with the shared token. The timeout
// is generous because the deploy endpoints reach the network — a repository
// probe against a slow forge should not be reported as a node failure.
type UnixClient struct {
	client *agentclient.Client
	// prepareClient is the same transport with the node-preparation timeout.
	// Preparing a node runs apt, which on a cold index and a slow mirror takes
	// far longer than any other call here; it gets its own client rather than
	// stretching everyone else's deadline to match (the php operator's
	// precedent, operators/php/client.go:20-22). The agent's own WriteTimeout
	// is 35 minutes, so this stays inside it.
	prepareClient *agentclient.Client
}

func NewUnixClient(socketPath, tokenPath string) *UnixClient {
	return &UnixClient{
		client:        agentclient.New(socketPath, tokenPath, "deploy", "node agent rejected the deploy operation", 5*time.Minute),
		prepareClient: agentclient.New(socketPath, tokenPath, "deploy", "node agent rejected the deploy operation", 30*time.Minute),
	}
}

func (c *UnixClient) ApplySSHAccess(ctx context.Context, request SSHAccessRequest) (SSHAccessObservation, error) {
	var observation SSHAccessObservation
	err := c.client.JSON(ctx, http.MethodPost, "/v1/deploy/ssh/apply", request, &observation)
	return observation, restoreSentinel(err)
}

// restoreSentinel turns the agent's flattened error code back into the sentinel
// the node raised. The socket carries a code and a message, not a Go error, so
// without this the control panel could only match on message text.
func restoreSentinel(err error) error {
	var responseError *agentclient.ResponseError
	if errors.As(err, &responseError) && responseError.Code == "sftp_jail_present" {
		return ErrSFTPJailPresent
	}
	return err
}

// atomicWrite publishes a file by writing a sibling temp file and renaming it,
// so a reader (sshd re-reading a drop-in, or an in-flight login reading
// authorized_keys) never sees a half-written file.
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
