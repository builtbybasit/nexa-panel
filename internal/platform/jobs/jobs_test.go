package jobs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/platform/audit"
	"github.com/nexa-panel/nexa-panel/internal/platform/authorization"
	"github.com/nexa-panel/nexa-panel/internal/platform/controlplane"
	"github.com/nexa-panel/nexa-panel/internal/platform/identity"
	"github.com/nexa-panel/nexa-panel/internal/platform/module"
	"github.com/nexa-panel/nexa-panel/internal/platform/persistence"
	"github.com/nexa-panel/nexa-panel/internal/platform/secrets"
)

func TestDurableWorkerCompletesJobAndStoresProgress(t *testing.T) {
	module, auditLog := newTestModule(t)
	if err := module.RegisterHandler("test.success", func(ctx context.Context, request json.RawMessage, report func(int, string) error) (any, error) {
		if err := report(25, "First step."); err != nil {
			return nil, err
		}
		if err := report(75, "Second step."); err != nil {
			return nil, err
		}
		return map[string]bool{"ok": true}, nil
	}); err != nil {
		t.Fatalf("register handler: %v", err)
	}

	actor := "user-1"
	job, err := module.Submit(context.Background(), "test.success", map[string]string{"input": "safe"}, &actor)
	if err != nil {
		t.Fatalf("Submit returned an error: %v", err)
	}
	module.Start(context.Background())
	completed := waitForState(t, module, job.ID, StateSucceeded)
	if completed.Progress != 100 || string(completed.Result) != `{"ok":true}` {
		t.Fatalf("unexpected completed job: %+v", completed)
	}
	events, err := module.EventsAfter(context.Background(), job.ID, 0)
	if err != nil {
		t.Fatalf("EventsAfter returned an error: %v", err)
	}
	if len(events) != 5 {
		t.Fatalf("event count = %d, want 5: %+v", len(events), events)
	}
	if events[0].State != StateQueued || events[len(events)-1].State != StateSucceeded {
		t.Fatalf("unexpected event states: %+v", events)
	}
	auditEvents, err := auditLog.List(context.Background(), 20)
	if err != nil {
		t.Fatalf("list audit events: %v", err)
	}
	if len(auditEvents) != 2 {
		t.Fatalf("audit event count = %d, want 2", len(auditEvents))
	}
}

func TestDurableWorkerStoresFailure(t *testing.T) {
	module, _ := newTestModule(t)
	if err := module.RegisterHandler("test.failure", func(context.Context, json.RawMessage, func(int, string) error) (any, error) {
		return nil, errors.New("planned failure")
	}); err != nil {
		t.Fatalf("register handler: %v", err)
	}
	job, err := module.Submit(context.Background(), "test.failure", struct{}{}, nil)
	if err != nil {
		t.Fatalf("Submit returned an error: %v", err)
	}
	module.Start(context.Background())
	failed := waitForState(t, module, job.ID, StateFailed)
	if failed.Failure != "planned failure" {
		t.Fatalf("failure = %q", failed.Failure)
	}
}

func TestNewRecoversInterruptedJobs(t *testing.T) {
	database, err := persistence.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	auditLog, err := audit.New(context.Background(), database)
	if err != nil {
		t.Fatalf("create audit module: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	first, err := NewWithConfig(context.Background(), database, auditLog, logger, Config{PollInterval: 10 * time.Millisecond})
	if err != nil {
		t.Fatalf("create first jobs module: %v", err)
	}
	now := time.Now().UTC()
	model := &jobModel{Kind: "platform.diagnostics", State: string(StateRunning), Progress: 50, RequestJSON: `{}`, CreatedAt: now, UpdatedAt: now, StartedAt: &now}
	if _, err := database.NewInsert().Model(model).Exec(context.Background()); err != nil {
		t.Fatalf("insert interrupted job: %v", err)
	}
	first.Close()

	recovered, err := NewWithConfig(context.Background(), database, auditLog, logger, Config{PollInterval: 10 * time.Millisecond})
	if err != nil {
		t.Fatalf("create recovered jobs module: %v", err)
	}
	t.Cleanup(recovered.Close)
	job, err := recovered.Get(context.Background(), model.ID)
	if err != nil {
		t.Fatalf("get recovered job: %v", err)
	}
	if job.State != StateQueued || job.StartedAt != nil {
		t.Fatalf("unexpected recovered job: %+v", job)
	}
	events, err := recovered.EventsAfter(context.Background(), model.ID, 0)
	if err != nil {
		t.Fatalf("get recovery events: %v", err)
	}
	if len(events) != 1 || events[0].Message != "Job recovered after control-plane restart." {
		t.Fatalf("unexpected recovery events: %+v", events)
	}
}

func TestAuthenticatedDiagnosticsHTTPAndEventStream(t *testing.T) {
	database, err := persistence.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	ctx := context.Background()
	auditLog, err := audit.New(ctx, database)
	if err != nil {
		t.Fatalf("create audit module: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	secretBox, err := secrets.New(bytes.Repeat([]byte{3}, 32))
	if err != nil {
		t.Fatalf("create test secret box: %v", err)
	}
	identityModule, err := identity.NewWithConfig(ctx, database, auditLog, secretBox, logger, identity.Config{
		SessionTTL: time.Hour, PasswordMemoryKiB: 64, PasswordIterations: 1, PasswordThreads: 1,
	})
	if err != nil {
		t.Fatalf("create identity module: %v", err)
	}
	jobsModule, err := NewWithConfig(ctx, database, auditLog, logger, Config{PollInterval: 10 * time.Millisecond})
	if err != nil {
		t.Fatalf("create jobs module: %v", err)
	}
	jobsModule.Start(context.Background())
	t.Cleanup(jobsModule.Close)
	server, err := controlplane.New("test", []module.Module{auditLog, identityModule, jobsModule}, logger,
		controlplane.WithAuthentication(identityModule), controlplane.WithAuthorization(authorization.New()))
	if err != nil {
		t.Fatalf("create control plane: %v", err)
	}

	bootstrapRequest := httptest.NewRequest(http.MethodPost, "/api/v1/auth/bootstrap",
		strings.NewReader(`{"username":"admin","password":"a-strong-password"}`))
	bootstrapResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(bootstrapResponse, bootstrapRequest)
	if bootstrapResponse.Code != http.StatusCreated {
		t.Fatalf("bootstrap = %d %s", bootstrapResponse.Code, bootstrapResponse.Body.String())
	}
	cookie := bootstrapResponse.Result().Cookies()[0]
	now := time.Now().UTC()
	if _, err := database.ExecContext(ctx, "UPDATE identity_users SET totp_confirmed_at = ?", now); err != nil {
		t.Fatalf("mark test MFA enrollment: %v", err)
	}
	if _, err := database.ExecContext(ctx, "UPDATE identity_sessions SET mfa_verified_at = ?", now); err != nil {
		t.Fatalf("mark test session verified: %v", err)
	}

	diagnosticsRequest := httptest.NewRequest(http.MethodPost, "/api/v1/jobs/diagnostics",
		strings.NewReader(`{"delayMilliseconds":10}`))
	diagnosticsRequest.AddCookie(cookie)
	diagnosticsResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(diagnosticsResponse, diagnosticsRequest)
	if diagnosticsResponse.Code != http.StatusAccepted {
		t.Fatalf("diagnostics = %d %s", diagnosticsResponse.Code, diagnosticsResponse.Body.String())
	}
	var submitted Job
	if err := json.Unmarshal(diagnosticsResponse.Body.Bytes(), &submitted); err != nil {
		t.Fatalf("decode submitted job: %v", err)
	}
	waitForState(t, jobsModule, submitted.ID, StateSucceeded)

	eventsRequest := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/jobs/%d/events", submitted.ID), nil)
	eventsRequest.AddCookie(cookie)
	eventsResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(eventsResponse, eventsRequest)
	if eventsResponse.Code != http.StatusOK || !strings.Contains(eventsResponse.Body.String(), "event: progress") {
		t.Fatalf("events = %d %s", eventsResponse.Code, eventsResponse.Body.String())
	}
}

func newTestModule(t *testing.T) (*Module, *audit.Module) {
	t.Helper()
	database, err := persistence.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	auditLog, err := audit.New(context.Background(), database)
	if err != nil {
		t.Fatalf("create audit module: %v", err)
	}
	module, err := NewWithConfig(context.Background(), database, auditLog,
		slog.New(slog.NewTextHandler(io.Discard, nil)), Config{PollInterval: 10 * time.Millisecond})
	if err != nil {
		t.Fatalf("create jobs module: %v", err)
	}
	t.Cleanup(module.Close)
	return module, auditLog
}

func waitForState(t *testing.T, module *Module, id int64, wanted State) Job {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		job, err := module.Get(context.Background(), id)
		if err != nil {
			t.Fatalf("Get returned an error: %v", err)
		}
		if job.State == wanted {
			return job
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("job %d did not reach state %s", id, wanted)
	return Job{}
}
