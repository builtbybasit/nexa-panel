package agent

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/platform/agentauth"
	postgresoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/postgres"
	siteoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/sites"
)

func TestAgentRejectsMissingCredential(t *testing.T) {
	server := New("", "test", "expected-token", slog.New(slog.NewTextHandler(io.Discard, nil)))
	handler := server.authenticate(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("missing token status = %d", unauthorized.Code)
	}
	authorizedRequest := httptest.NewRequest(http.MethodGet, "/", nil)
	authorizedRequest.Header.Set("Authorization", "Bearer expected-token")
	authorized := httptest.NewRecorder()
	handler.ServeHTTP(authorized, authorizedRequest)
	if authorized.Code != http.StatusNoContent {
		t.Fatalf("valid token status = %d", authorized.Code)
	}
}

func TestServeRejectsUnsafeConfigurationBeforeOpeningSocket(t *testing.T) {
	if err := New("/private/tmp/unused-agent.sock", "test", "", nil).Serve(context.Background()); err == nil || !strings.Contains(err.Error(), "credential") {
		t.Fatalf("empty credential error = %v", err)
	}
}

func TestAgentSignsSitePlansAndRejectsTampering(t *testing.T) {
	server := New("", "test", "signing-token", slog.New(slog.NewTextHandler(io.Discard, nil)))
	plan := siteoperator.Plan{ID: "plan-1", Kind: siteoperator.PlanKind, Site: siteoperator.Site{ID: "site-1"}, PlannedAt: time.Now(), ExpiresAt: time.Now().Add(time.Minute)}
	if server.verifySitePlan(plan) {
		t.Fatal("an unsigned site plan must not verify")
	}
	plan.Signature = server.signSitePlan(plan)
	if !server.verifySitePlan(plan) {
		t.Fatal("agent-issued site plan should verify")
	}
	plan.Site.ID = "tampered"
	if server.verifySitePlan(plan) {
		t.Fatal("tampered site plan should not verify")
	}
}

func TestAgentSignsPostgresPlansAndRejectsTampering(t *testing.T) {
	server := New("", "test", "signing-token", slog.New(slog.NewTextHandler(io.Discard, nil)))
	plan := postgresoperator.Plan{ID: "plan-1", Kind: postgresoperator.PlanKind, Change: postgresoperator.Change{Action: postgresoperator.ActionCreateDatabase, Database: "app_db"}, PlannedAt: time.Now(), ExpiresAt: time.Now().Add(time.Minute)}
	plan.Signature = server.signPostgresPlan(plan)
	if !server.verifyPostgresPlan(plan) {
		t.Fatal("agent-issued PostgreSQL plan should verify")
	}
	plan.Change.Database = "tampered"
	if server.verifyPostgresPlan(plan) {
		t.Fatal("tampered PostgreSQL plan should not verify")
	}
}

func TestAgentServesHealthOverUnixSocket(t *testing.T) {
	directory := t.TempDir()
	socketDirectory, err := os.MkdirTemp("/private/tmp", "nexa-agent-")
	if err != nil {
		t.Fatalf("create socket directory: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(socketDirectory) })
	socketPath := filepath.Join(socketDirectory, "agent.sock")
	tokenPath := filepath.Join(directory, "agent.token")
	token, err := agentauth.OpenOrCreate(tokenPath)
	if err != nil {
		t.Fatalf("create token: %v", err)
	}
	server := New(socketPath, "test", token, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx) }()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(socketPath); err == nil {
			break
		}
		select {
		case err := <-done:
			if err != nil && strings.Contains(err.Error(), "operation not permitted") {
				t.Skip("sandbox does not permit Unix socket binding")
			}
			t.Fatalf("agent stopped before creating socket: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("agent socket was not created")
		}
		time.Sleep(10 * time.Millisecond)
	}

	client := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var dialer net.Dialer
		return dialer.DialContext(ctx, "unix", socketPath)
	}}}
	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://unix/v1/health", nil)
	if err != nil {
		t.Fatalf("build health request: %v", err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("health request failed: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("health status = %d", response.StatusCode)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve returned an error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("agent did not stop")
	}
}
