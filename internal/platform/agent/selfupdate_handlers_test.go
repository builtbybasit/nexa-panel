package agent

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	selfupdateoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/selfupdate"
)

type recordingSelfUpdate struct {
	applyCalls int
	// applyStarted is closed by the first apply and applyRelease blocks it, so a
	// test can hold a request open across a shutdown.
	applyStarted chan struct{}
	applyRelease chan struct{}
	status       selfupdateoperator.TransactionStatus
	statusErr    error
}

func (*recordingSelfUpdate) Latest(context.Context) (selfupdateoperator.Availability, error) {
	return selfupdateoperator.Availability{}, nil
}

func (operator *recordingSelfUpdate) Apply(context.Context, selfupdateoperator.Change) (selfupdateoperator.Result, error) {
	operator.applyCalls++
	if operator.applyStarted != nil {
		close(operator.applyStarted)
		<-operator.applyRelease
	}
	return selfupdateoperator.Result{}, nil
}

func (operator *recordingSelfUpdate) Transaction(context.Context) (selfupdateoperator.TransactionStatus, error) {
	return operator.status, operator.statusErr
}

func (*recordingSelfUpdate) Rollback(context.Context) (selfupdateoperator.Result, error) {
	return selfupdateoperator.Result{}, nil
}

func TestSelfUpdateTransactionRouteReportsTheJournal(t *testing.T) {
	operator := &recordingSelfUpdate{status: selfupdateoperator.TransactionStatus{
		Present: true, ID: "abc", TargetVersion: "0.5.3", Phase: selfupdateoperator.PhaseSucceeded, Terminal: true,
	}}
	server := New("unused", "0.1.0", "token", nil, WithSelfUpdateOperator(operator))
	response := httptest.NewRecorder()

	server.selfUpdateTransactionHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/self-update/transaction", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", response.Code, response.Body.String())
	}
	var status selfupdateoperator.TransactionStatus
	if err := json.Unmarshal(response.Body.Bytes(), &status); err != nil {
		t.Fatalf("decode journal: %v", err)
	}
	if !status.Terminal || status.ID != "abc" || status.Phase != selfupdateoperator.PhaseSucceeded {
		t.Fatalf("status = %+v", status)
	}
}

func TestSelfUpdateRPCRejectsHostPaths(t *testing.T) {
	operator := &recordingSelfUpdate{}
	server := New("unused", "0.1.0", "token", nil, WithSelfUpdateOperator(operator))
	request := httptest.NewRequest(http.MethodPost, "/v1/self-update/apply", strings.NewReader(`{"version":"0.2.0","binaryPath":"/tmp/attacker"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	server.selfUpdateApplyHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", response.Code, response.Body.String())
	}
	if operator.applyCalls != 0 {
		t.Fatal("a caller-controlled host path reached the root operator")
	}
}

// Applying an update restarts nexa-agent while the apply request it is serving
// is still open. Systemd's SIGTERM therefore arrives with a request that cannot
// be drained, and exiting non-zero because of it marked the unit failed on
// every successful update — the update itself having worked.
func TestServeExitsCleanlyWhenAStopSignalArrivesDuringAnApply(t *testing.T) {
	directory, err := os.MkdirTemp(socketTempRoot, "nexa-agent-stop-")
	if err != nil {
		t.Fatalf("create socket directory: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	socket := filepath.Join(directory, "agent.sock")

	operator := &recordingSelfUpdate{applyStarted: make(chan struct{}), applyRelease: make(chan struct{})}
	defer close(operator.applyRelease)
	server := New(socket, "0.1.0", "token", nil, WithSelfUpdateOperator(operator))
	server.shutdownGrace = 100 * time.Millisecond

	ctx, stop := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- server.Serve(ctx) }()

	client := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}}
	go func() {
		request, _ := http.NewRequest(http.MethodPost, "http://unix/v1/self-update/apply", strings.NewReader(`{"version":"0.5.3"}`))
		request.Header.Set("Authorization", "Bearer token")
		request.Header.Set("Content-Type", "application/json")
		for {
			response, err := client.Do(request)
			if err == nil {
				_ = response.Body.Close()
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(10 * time.Millisecond):
			}
			request, _ = http.NewRequest(http.MethodPost, "http://unix/v1/self-update/apply", strings.NewReader(`{"version":"0.5.3"}`))
			request.Header.Set("Authorization", "Bearer token")
			request.Header.Set("Content-Type", "application/json")
		}
	}()

	select {
	case <-operator.applyStarted:
	case <-time.After(10 * time.Second):
		stop()
		t.Fatal("the apply request never reached the operator")
	}
	stop()

	select {
	case err := <-served:
		if err != nil {
			t.Fatalf("a stop signal during an apply exited non-zero: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Serve did not return after the stop signal")
	}
}
