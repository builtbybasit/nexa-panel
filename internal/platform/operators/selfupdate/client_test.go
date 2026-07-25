package selfupdate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/platform/agentclient"
)

// restartingAgent models the one thing that makes an update's outcome hard to
// report: activating a release restarts nexa-agent, so the apply request is
// severed by the very act it is reporting on, and the committed outcome only
// ever appears in the journal the restarted agent serves.
type restartingAgent struct {
	// journal is the sequence of statuses successive reads observe; the last
	// entry is repeated once exhausted.
	journal []TransactionStatus
	reads   int
	// applyErr is what the severed apply returns; nil answers normally.
	applyErr    error
	applyResult Result
	// transactionErr fails every journal read, standing in for an agent that
	// never comes back.
	transactionErr error
	applies        int
}

func (a *restartingAgent) JSON(_ context.Context, method, path string, _, output any) error {
	switch {
	case method == http.MethodPost && path == "/v1/self-update/apply":
		a.applies++
		if a.applyErr != nil {
			return a.applyErr
		}
		*output.(*Result) = a.applyResult
		return nil
	case method == http.MethodGet && path == "/v1/self-update/transaction":
		if a.transactionErr != nil {
			return a.transactionErr
		}
		index := a.reads
		if index >= len(a.journal) {
			index = len(a.journal) - 1
		}
		a.reads++
		if index < 0 {
			return errors.New("no journal configured")
		}
		*output.(*TransactionStatus) = a.journal[index]
		return nil
	}
	return fmt.Errorf("unexpected %s %s", method, path)
}

func testUnixClient(agent agentTransport) *UnixClient {
	return &UnixClient{client: agent, resumeTimeout: 2 * time.Second, resumePoll: time.Millisecond}
}

// severedResponse is what the agent restarting looks like to its client: the
// request was written, and the connection died before the response arrived.
func severedResponse() error {
	return fmt.Errorf("call self-update agent: Post \"http://unix/v1/self-update/apply\": %w", io.EOF)
}

func TestApplyReportsTheCommittedOutcomeWhenActivationRestartsTheAgent(t *testing.T) {
	committed := Result{PreviousVersion: "0.4.0", TargetVersion: "0.5.3", Swapped: true, Activated: true, PackagingSynced: true}
	agent := &restartingAgent{
		applyErr: severedResponse(),
		journal: []TransactionStatus{
			// Read before the apply: the node's previous, unrelated update.
			{Present: true, ID: "older", Phase: PhaseSucceeded, Terminal: true},
			// The agent is mid-restart; the new transaction is still activating.
			{Present: true, ID: "current", TargetVersion: "0.5.3", Phase: "activating"},
			{Present: true, ID: "current", TargetVersion: "0.5.3", Phase: PhaseSucceeded, Terminal: true, Result: committed},
		},
	}

	result, err := testUnixClient(agent).Apply(context.Background(), Change{Version: "0.5.3"})
	if err != nil {
		t.Fatalf("a successful update reported a failure: %v", err)
	}
	if result != committed {
		t.Fatalf("result = %+v, want the journalled %+v", result, committed)
	}
}

func TestApplyReportsAFailureTheRestartedAgentCommitted(t *testing.T) {
	agent := &restartingAgent{
		applyErr: severedResponse(),
		journal: []TransactionStatus{
			{},
			{Present: true, ID: "current", Phase: PhaseFailed, Terminal: true, Failure: "activate updated services: nginx -t failed", Result: Result{RolledBack: true}},
		},
	}

	result, err := testUnixClient(agent).Apply(context.Background(), Change{Version: "0.5.3"})

	if err == nil || !strings.Contains(err.Error(), "nginx -t failed") {
		t.Fatalf("error = %v, want the journalled activation failure", err)
	}
	if !result.RolledBack {
		t.Fatalf("result = %+v, want the journalled rollback", result)
	}
}

func TestApplyStillFailsWhenNoNewTransactionEverCommits(t *testing.T) {
	agent := &restartingAgent{
		applyErr: severedResponse(),
		// The journal never moves past the transaction that was already there,
		// so nothing this apply did was ever committed.
		journal: []TransactionStatus{{Present: true, ID: "older", Phase: PhaseSucceeded, Terminal: true}},
	}

	if _, err := testUnixClient(agent).Apply(context.Background(), Change{Version: "0.5.3"}); err == nil {
		t.Fatal("a severed apply with no committed transaction was reported as success")
	}
	// Resuming reads the journal; it must never re-issue the apply itself. An
	// update that has already swapped the binary is not a request to repeat.
	if agent.applies != 1 {
		t.Fatalf("applies = %d, want the one request; the severed apply was retried", agent.applies)
	}
}

// The activation that severs the apply also restarts nexa-api, and that restart
// cancels the job context this Apply runs under before the journal ever reaches a
// terminal phase. The client must surface that cancellation as context.Canceled,
// not as a "no update outcome" failure: only context.Canceled tells the jobs
// worker a shutdown is in progress, so it leaves the row running and RecoveryRetry
// re-reads the committed outcome after the control panel comes back. Masking it
// recorded a false failure for a node that had already updated correctly.
func TestApplyPropagatesTheCancellationWhenActivationRestartsTheControlPanel(t *testing.T) {
	agent := &restartingAgent{
		applyErr: severedResponse(),
		// The journal is still activating and never commits before the API is
		// bounced, so resuming can only end when the parent context is cancelled.
		journal: []TransactionStatus{
			{Present: true, ID: "older", Phase: PhaseSucceeded, Terminal: true},
			{Present: true, ID: "current", TargetVersion: "0.5.3", Phase: "activating"},
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(5 * time.Millisecond)
		cancel()
	}()

	_, err := testUnixClient(agent).Apply(ctx, Change{Version: "0.5.3"})

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled so the jobs worker leaves the row running for retry", err)
	}
}

func TestApplyDoesNotResumeWhenTheAgentAnsweredWithAnError(t *testing.T) {
	agent := &restartingAgent{
		applyErr: &agentclient.ResponseError{StatusCode: http.StatusConflict, Code: "self_update_failed", Message: "this node already runs version 0.5.3"},
		journal:  []TransactionStatus{{Present: true, ID: "older", Phase: PhaseSucceeded, Terminal: true}},
	}

	_, err := testUnixClient(agent).Apply(context.Background(), Change{Version: "0.5.3"})

	if err == nil || !strings.Contains(err.Error(), "already runs version") {
		t.Fatalf("error = %v, want the agent's own rejection", err)
	}
}

// A node whose agent predates the transaction route cannot be asked what
// happened, so the severed response has to stay a reported failure rather than
// become a silent success.
func TestApplyReportsTheSeveredResponseWhenTheJournalCannotBeRead(t *testing.T) {
	agent := &restartingAgent{
		applyErr:       severedResponse(),
		transactionErr: &agentclient.ResponseError{StatusCode: http.StatusNotFound, Message: "not found"},
	}

	if _, err := testUnixClient(agent).Apply(context.Background(), Change{Version: "0.5.3"}); err == nil {
		t.Fatal("an unreadable journal turned a severed apply into a success")
	}
}

func TestApplyReturnsTheResponseWhenTheAgentSurvives(t *testing.T) {
	expected := Result{PreviousVersion: "0.4.0", TargetVersion: "0.5.3", Activated: true}
	agent := &restartingAgent{applyResult: expected, journal: []TransactionStatus{{}}}

	result, err := testUnixClient(agent).Apply(context.Background(), Change{Version: "0.5.3"})

	if err != nil || result != expected {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
}

// The journal projection is what the client reads over the socket, so it has to
// survive a JSON round trip with its terminal phase and result intact.
func TestTransactionStatusSurvivesTheSocketEncoding(t *testing.T) {
	encoded, err := json.Marshal(TransactionStatus{Present: true, ID: "abc", Phase: PhaseSucceeded, Terminal: true, Result: Result{Activated: true}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded TransactionStatus
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !decoded.Present || !decoded.Terminal || decoded.Phase != PhaseSucceeded || !decoded.Result.Activated {
		t.Fatalf("decoded = %+v", decoded)
	}
}
