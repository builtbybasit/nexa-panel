package agent

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/platform/agentauth"
	nodeoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/nodes"
	postgresoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/postgres"
	siteoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/sites"
)

func TestAgentRejectsMissingCredential(t *testing.T) {
	operator, _ := nodeoperator.NewFileOperator(filepath.Join(t.TempDir(), "probe.conf"))
	server := New("", "test", "expected-token", operator, slog.New(slog.NewTextHandler(io.Discard, nil)))
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

func TestAgentSignsSitePlansAndRejectsTampering(t *testing.T) {
	operator, _ := nodeoperator.NewFileOperator(filepath.Join(t.TempDir(), "probe.conf"))
	server := New("", "test", "signing-token", operator, slog.New(slog.NewTextHandler(io.Discard, nil)))
	plan := siteoperator.Plan{ID: "plan-1", Kind: siteoperator.PlanKind, Site: siteoperator.Site{ID: "site-1"}, PlannedAt: time.Now(), ExpiresAt: time.Now().Add(time.Minute)}
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
	operator, _ := nodeoperator.NewFileOperator(filepath.Join(t.TempDir(), "probe.conf"))
	server := New("", "test", "signing-token", operator, slog.New(slog.NewTextHandler(io.Discard, nil)))
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

func TestAgentSignsPlansAndRejectsTampering(t *testing.T) {
	operator, _ := nodeoperator.NewFileOperator(filepath.Join(t.TempDir(), "probe.conf"))
	server := New("", "test", "signing-token", operator, slog.New(slog.NewTextHandler(io.Discard, nil)))
	plan, err := operator.Plan(context.Background(), nodeoperator.Change{Present: true, Content: "managed=true\n"})
	if err != nil {
		t.Fatalf("Plan returned an error: %v", err)
	}
	plan.Signature = server.signPlan(plan)
	if !server.verifyPlan(plan) {
		t.Fatal("agent-issued plan should verify")
	}
	plan.Desired.Content = "tampered=true\n"
	if server.verifyPlan(plan) {
		t.Fatal("tampered plan should not verify")
	}
}

func TestUnixClientPlanApplyObserveRollback(t *testing.T) {
	directory := t.TempDir()
	socketDirectory, err := os.MkdirTemp("/private/tmp", "nexa-agent-")
	if err != nil {
		t.Fatalf("create socket directory: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(socketDirectory) })
	socketPath := filepath.Join(socketDirectory, "agent.sock")
	tokenPath := filepath.Join(directory, "agent.token")
	probePath := filepath.Join(directory, "etc", "probe.conf")
	token, err := agentauth.OpenOrCreate(tokenPath)
	if err != nil {
		t.Fatalf("create token: %v", err)
	}
	operator, _ := nodeoperator.NewFileOperator(probePath)
	server := New(socketPath, "test", token, operator, slog.New(slog.NewTextHandler(io.Discard, nil)))
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

	client := nodeoperator.NewUnixClient(socketPath, tokenPath)
	plan, err := client.Plan(context.Background(), nodeoperator.Change{Present: true, Content: "managed=true\n"})
	if err != nil {
		t.Fatalf("client Plan returned an error: %v", err)
	}
	applied, err := client.Apply(context.Background(), plan)
	if err != nil || !applied.Exists {
		t.Fatalf("client Apply = %+v, %v", applied, err)
	}
	observed, err := client.Observe(context.Background())
	if err != nil || observed.Digest != plan.Desired.Digest {
		t.Fatalf("client Observe = %+v, %v", observed, err)
	}
	rolledBack, err := client.Rollback(context.Background(), plan)
	if err != nil || rolledBack.Exists {
		t.Fatalf("client Rollback = %+v, %v", rolledBack, err)
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
