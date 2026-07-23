package selfupdate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"syscall"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/platform/agentclient"
)

// agentTransport is the whole of this operator's dependency on the agent
// socket. It is an interface so the severed-response resume below can be
// exercised without a real socket and a real service restart.
type agentTransport interface {
	JSON(ctx context.Context, method, path string, input, output any) error
}

// UnixClient is the control-plane-side Operator implementation. It talks to the
// privileged agent over its unix socket, authenticating with the shared token.
type UnixClient struct {
	client agentTransport
	// resumeTimeout bounds how long a severed apply waits for the restarted
	// agent to publish a committed outcome. It matches the activation helper's
	// own ceiling: past that the helper has either journalled a result or died,
	// and no outcome is coming.
	resumeTimeout time.Duration
	resumePoll    time.Duration
}

// NewUnixClient dials the agent socket. The long timeout accommodates a full
// release download over a slow link.
func NewUnixClient(socketPath, tokenPath string) *UnixClient {
	return &UnixClient{
		client:        agentclient.New(socketPath, tokenPath, "self-update", "node agent rejected the self-update", 15*time.Minute),
		resumeTimeout: 10 * time.Minute,
		resumePoll:    2 * time.Second,
	}
}

func (c *UnixClient) Latest(ctx context.Context) (Availability, error) {
	var result Availability
	err := c.client.JSON(ctx, http.MethodGet, "/v1/self-update/latest", nil, &result)
	return result, err
}

// Transaction reads the node's durable update journal.
func (c *UnixClient) Transaction(ctx context.Context) (TransactionStatus, error) {
	var status TransactionStatus
	err := c.client.JSON(ctx, http.MethodGet, "/v1/self-update/transaction", nil, &status)
	return status, err
}

// Apply asks the agent to install a release. The outcome is whatever the
// durable journal says it is, not whatever this response says: activating an
// update restarts nexa-agent, which severs the very request that is reporting
// on it. Treating that severed connection as a failure told operators an update
// had failed on a node that had already updated correctly — and both obvious
// reactions to that, retrying and starting recovery, are wrong.
//
// So the journal is read before the call and, if the response is severed,
// polled until a *different* transaction reaches a terminal phase. Requiring a
// new transaction id is what keeps a stale success from being reported as this
// apply's, and what keeps the wait honest when the agent never comes back.
func (c *UnixClient) Apply(ctx context.Context, change Change) (Result, error) {
	before, beforeErr := c.Transaction(ctx)
	var result Result
	err := c.client.JSON(ctx, http.MethodPost, "/v1/self-update/apply", change, &result)
	if err == nil || !isSeveredResponse(err) {
		return result, err
	}
	if beforeErr != nil {
		// Without a baseline there is no way to tell this apply's outcome from
		// an older one, so the severed response stands as the reported failure.
		// An agent too old to serve the journal route lands here.
		return Result{}, fmt.Errorf("%w; the node's update journal could not be read to confirm the outcome: %v", err, beforeErr)
	}
	return c.awaitCommittedTransaction(ctx, before, err)
}

// awaitCommittedTransaction waits out the agent restart and reports what the
// journal committed. Every read failure in the loop is expected — the socket is
// gone for most of it — so only the deadline is terminal.
func (c *UnixClient) awaitCommittedTransaction(ctx context.Context, before TransactionStatus, severed error) (Result, error) {
	waitCtx, cancel := context.WithTimeout(ctx, c.resumeTimeout)
	defer cancel()
	ticker := time.NewTicker(c.resumePoll)
	defer ticker.Stop()
	for {
		if status, err := c.Transaction(waitCtx); err == nil && status.Terminal && status.ID != before.ID {
			if status.Phase == PhaseSucceeded {
				return status.Result, nil
			}
			failure := status.Failure
			if failure == "" {
				failure = "the node reported a failed update without a reason"
			}
			return status.Result, errors.New(failure)
		}
		select {
		case <-waitCtx.Done():
			return Result{}, fmt.Errorf("the node published no update outcome after its agent restarted: %w", severed)
		case <-ticker.C:
		}
	}
}

// isSeveredResponse reports whether err is the connection dying mid-exchange,
// which is what a restart of the agent looks like to its client. A request that
// never reached the agent, and one the agent answered with an error of its own,
// are both ordinary failures and must keep being reported as such.
func isSeveredResponse(err error) bool {
	var answered *agentclient.ResponseError
	if errors.As(err, &answered) {
		return false
	}
	return errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.EPIPE) ||
		errors.Is(err, net.ErrClosed)
}
