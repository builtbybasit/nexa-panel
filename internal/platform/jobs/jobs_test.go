package jobs

import (
	"bytes"
	"context"
	"database/sql"
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

func TestDurableWorkerRecoversFromHandlerPanic(t *testing.T) {
	module, _ := newTestModule(t)
	if err := module.RegisterHandler("test.panic", func(context.Context, json.RawMessage, func(int, string) error) (any, error) {
		panic("boom")
	}); err != nil {
		t.Fatalf("register panic handler: %v", err)
	}
	if err := module.RegisterHandler("test.after", func(context.Context, json.RawMessage, func(int, string) error) (any, error) {
		return map[string]bool{"ok": true}, nil
	}); err != nil {
		t.Fatalf("register after handler: %v", err)
	}

	panicJob, err := module.Submit(context.Background(), "test.panic", struct{}{}, nil)
	if err != nil {
		t.Fatalf("Submit panic job: %v", err)
	}
	module.Start(context.Background())

	failed := waitForState(t, module, panicJob.ID, StateFailed)
	if !strings.Contains(failed.Failure, "job handler panicked") || !strings.Contains(failed.Failure, "boom") {
		t.Fatalf("failure = %q, want it to mention the recovered panic", failed.Failure)
	}
	if got := module.PanicCount(); got != 1 {
		t.Fatalf("PanicCount() = %d, want 1", got)
	}

	// The worker goroutine must survive the panic and keep processing jobs.
	afterJob, err := module.Submit(context.Background(), "test.after", struct{}{}, nil)
	if err != nil {
		t.Fatalf("Submit after job: %v", err)
	}
	module.wake()
	waitForState(t, module, afterJob.ID, StateSucceeded)
}

func TestHeartbeatKeepsLongRunningJobLeaseActive(t *testing.T) {
	database, err := persistence.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := persistence.RunMigrations(context.Background(), database); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	ctx := context.Background()
	auditLog, err := audit.New(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	module, err := NewWithConfig(ctx, database, auditLog, slog.New(slog.NewTextHandler(io.Discard, nil)), Config{
		PollInterval:  5 * time.Millisecond,
		LeaseDuration: 150 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(module.Close)
	started := make(chan struct{})
	release := make(chan struct{})
	if err := module.RegisterHandler("test.long", func(ctx context.Context, _ json.RawMessage, _ func(int, string) error) (any, error) {
		close(started)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-release:
			return map[string]bool{"ok": true}, nil
		}
	}); err != nil {
		t.Fatal(err)
	}
	job, err := module.Submit(ctx, "test.long", struct{}{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	module.Start(ctx)
	<-started
	time.Sleep(350 * time.Millisecond)
	if err := module.recoverExpired(ctx); err != nil {
		t.Fatal(err)
	}
	active, err := module.Get(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if active.State != StateRunning {
		t.Fatalf("heartbeat did not preserve running lease: %+v", active)
	}
	close(release)
	waitForState(t, module, job.ID, StateSucceeded)
}

func TestExpiredRetryLeaseRequeuesInterruptedJob(t *testing.T) {
	database, err := persistence.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := persistence.RunMigrations(context.Background(), database); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
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
	expired := now.Add(-time.Minute)
	leaseToken := "expired-lease"
	model := &jobModel{
		Kind: "platform.diagnostics", State: string(StateRunning), Progress: 50,
		RequestJSON: `{}`, ScopeSiteIDs: `[]`, RecoveryPolicy: string(RecoveryRetry),
		LeaseToken: &leaseToken, LeaseExpiresAt: &expired, CreatedAt: now, UpdatedAt: now, StartedAt: &now,
	}
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
	if len(events) != 1 || events[0].Message != "Job lease expired; retrying idempotent work." {
		t.Fatalf("unexpected recovery events: %+v", events)
	}
}

func TestExpiredFailLeaseRequiresReconciliation(t *testing.T) {
	database, err := persistence.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := persistence.RunMigrations(context.Background(), database); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	ctx := context.Background()
	auditLog, err := audit.New(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	seed, err := NewWithConfig(ctx, database, auditLog, logger, Config{PollInterval: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	seed.Close()
	now := time.Now().UTC()
	expired := now.Add(-time.Minute)
	leaseToken := "expired-lease"
	model := &jobModel{
		Kind: "host.mutation", State: string(StateRunning), Progress: 60,
		RequestJSON: `{}`, ScopeSiteIDs: `[]`, RecoveryPolicy: string(RecoveryFail),
		LeaseToken: &leaseToken, LeaseExpiresAt: &expired, CreatedAt: now, UpdatedAt: now, StartedAt: &now,
	}
	if _, err := database.NewInsert().Model(model).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	recovered, err := NewWithConfig(ctx, database, auditLog, logger, Config{PollInterval: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(recovered.Close)
	job, err := recovered.Get(ctx, model.ID)
	if err != nil {
		t.Fatal(err)
	}
	if job.State != StateFailed || !strings.Contains(job.Failure, "reconcile") {
		t.Fatalf("expired non-idempotent job = %+v", job)
	}
}

func TestActiveLeaseIsNotRecoveredByAnotherWorker(t *testing.T) {
	database, err := persistence.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := persistence.RunMigrations(context.Background(), database); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	ctx := context.Background()
	auditLog, err := audit.New(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	seed, err := NewWithConfig(ctx, database, auditLog, logger, Config{PollInterval: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	seed.Close()
	now := time.Now().UTC()
	activeUntil := now.Add(time.Minute)
	leaseToken := "active-lease"
	model := &jobModel{
		Kind: "host.mutation", State: string(StateRunning), Progress: 40,
		RequestJSON: `{}`, ScopeSiteIDs: `[]`, RecoveryPolicy: string(RecoveryRetry),
		LeaseToken: &leaseToken, LeaseExpiresAt: &activeUntil, CreatedAt: now, UpdatedAt: now, StartedAt: &now,
	}
	if _, err := database.NewInsert().Model(model).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	observer, err := NewWithConfig(ctx, database, auditLog, logger, Config{PollInterval: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(observer.Close)
	job, err := observer.Get(ctx, model.ID)
	if err != nil {
		t.Fatal(err)
	}
	if job.State != StateRunning || job.Progress != 40 {
		t.Fatalf("active lease was changed: %+v", job)
	}
}

func TestEnqueuerDoesNotRecoverAndDeduplicates(t *testing.T) {
	database, err := persistence.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := persistence.RunMigrations(context.Background(), database); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	ctx := context.Background()
	enqueuer, err := NewEnqueuer(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	expired := now.Add(-time.Minute)
	leaseToken := "expired-lease"
	running := &jobModel{
		Kind: "host.mutation", State: string(StateRunning), RequestJSON: `{}`, ScopeSiteIDs: `[]`,
		RecoveryPolicy: string(RecoveryFail), LeaseToken: &leaseToken, LeaseExpiresAt: &expired,
		CreatedAt: now, UpdatedAt: now, StartedAt: &now,
	}
	if _, err := database.NewInsert().Model(running).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	first, err := enqueuer.EnqueueTitled(ctx, "backup.run", "Scheduled backup", map[string]string{"planId": "one"}, EnqueueOptions{
		SubmitOptions:  SubmitOptions{SiteIDs: []string{"site_a"}, IdempotencyKey: "timer:one"},
		RecoveryPolicy: RecoveryFail,
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := enqueuer.EnqueueTitled(ctx, "backup.run", "Scheduled backup", map[string]string{"planId": "one"}, EnqueueOptions{
		SubmitOptions:  SubmitOptions{SiteIDs: []string{"site_a"}, IdempotencyKey: "timer:one"},
		RecoveryPolicy: RecoveryFail,
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID {
		t.Fatalf("idempotent submissions created jobs %d and %d", first.ID, second.ID)
	}
	if _, err := enqueuer.EnqueueTitled(ctx, "backup.run", "Scheduled backup", map[string]string{"planId": "two"}, EnqueueOptions{
		SubmitOptions:  SubmitOptions{SiteIDs: []string{"site_a"}, IdempotencyKey: "timer:one"},
		RecoveryPolicy: RecoveryFail,
	}); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("conflicting key error = %v", err)
	}
	var state string
	if err := database.QueryRowContext(ctx, "SELECT state FROM jobs WHERE id = ?", running.ID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if State(state) != StateRunning {
		t.Fatalf("enqueue-only helper changed running job to %q", state)
	}
}

func TestDeveloperJobReadsRespectSiteScope(t *testing.T) {
	module, _ := newTestModule(t)
	ctx := context.Background()
	module.SetSiteAccessPolicy(fakeJobSiteAccess{grants: map[string][]string{"dev-1": {"site_a"}}})
	if err := module.RegisterHandler("test.scoped", func(context.Context, json.RawMessage, func(int, string) error) (any, error) {
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	other := "admin-1"
	own := "dev-1"
	granted, _ := module.SubmitTitledWithOptions(ctx, "test.scoped", "Granted", map[string]string{"value": "safe"}, SubmitOptions{ActorUserID: &other, SiteIDs: []string{"site_a"}})
	hidden, _ := module.SubmitTitledWithOptions(ctx, "test.scoped", "Hidden", map[string]string{"secret": "hidden"}, SubmitOptions{ActorUserID: &other, SiteIDs: []string{"site_b"}})
	_, _ = module.SubmitTitledWithOptions(ctx, "test.scoped", "Compound", struct{}{}, SubmitOptions{ActorUserID: &other, SiteIDs: []string{"site_a", "site_b"}})
	owned, _ := module.SubmitTitledWithOptions(ctx, "test.scoped", "Owned", struct{}{}, SubmitOptions{ActorUserID: &own})
	_, _ = module.SubmitTitledWithOptions(ctx, "test.scoped", "Unscoped other", struct{}{}, SubmitOptions{ActorUserID: &other})

	developer := identity.User{ID: own, Role: "developer"}
	items, err := module.ListForUser(ctx, developer, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].ID != owned.ID || items[1].ID != granted.ID {
		t.Fatalf("developer-visible jobs = %+v", items)
	}
	if _, err := module.GetForUser(ctx, developer, hidden.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("hidden job lookup error = %v", err)
	}
}

type fakeJobSiteAccess struct {
	grants map[string][]string
}

func (policy fakeJobSiteAccess) AccessibleSiteIDs(_ context.Context, user identity.User) (bool, []string, error) {
	if user.Role != "developer" {
		return true, nil, nil
	}
	return false, policy.grants[user.ID], nil
}

func TestAuthenticatedDiagnosticsHTTPAndEventStream(t *testing.T) {
	server, jobsModule, cookie, csrfToken := newAuthenticatedTestServer(t)

	diagnosticsRequest := httptest.NewRequest(http.MethodPost, "/api/v1/jobs/diagnostics",
		strings.NewReader(`{"delayMilliseconds":10}`))
	diagnosticsRequest.RemoteAddr = "127.0.0.1:12345"
	diagnosticsRequest.Header.Set("Origin", "http://"+diagnosticsRequest.Host)
	diagnosticsRequest.Header.Set("X-CSRF-Token", csrfToken)
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
	eventsRequest.RemoteAddr = "127.0.0.1:12345"
	eventsRequest.AddCookie(cookie)
	eventsResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(eventsResponse, eventsRequest)
	if eventsResponse.Code != http.StatusOK || !strings.Contains(eventsResponse.Body.String(), "event: progress") {
		t.Fatalf("events = %d %s", eventsResponse.Code, eventsResponse.Body.String())
	}
}

// TestJobAPIRedactsCredentialsInRequestAndResult pins the response-path
// redaction: a job request or result that carries credential material must
// never leave the API, on either the list or the get route.
func TestJobAPIRedactsCredentialsInRequestAndResult(t *testing.T) {
	server, jobsModule, cookie, _ := newAuthenticatedTestServer(t)
	if err := jobsModule.RegisterHandler("test.secretive", func(ctx context.Context, request json.RawMessage, report func(int, string) error) (any, error) {
		return map[string]any{"apiToken": "result-swordfish"}, nil
	}); err != nil {
		t.Fatalf("register handler: %v", err)
	}
	actor := "user-1"
	submitted, err := jobsModule.Submit(context.Background(), "test.secretive", map[string]any{
		"host":     "example.test",
		"password": "request-hunter2",
		"nested":   map[string]any{"sshPrivateKey": "request-pem"},
		"accounts": []any{map[string]any{"passphrase": "request-open-sesame"}},
	}, &actor)
	if err != nil {
		t.Fatalf("Submit returned an error: %v", err)
	}
	waitForState(t, jobsModule, submitted.ID, StateSucceeded)

	for _, path := range []string{"/api/v1/jobs?limit=50", fmt.Sprintf("/api/v1/jobs/%d", submitted.ID)} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.RemoteAddr = "127.0.0.1:12345"
		request.AddCookie(cookie)
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("%s = %d %s", path, response.Code, response.Body.String())
		}
		body := response.Body.String()
		for _, leaked := range []string{"request-hunter2", "request-pem", "request-open-sesame", "result-swordfish"} {
			if strings.Contains(body, leaked) {
				t.Fatalf("%s leaked %q: %s", path, leaked, body)
			}
		}
		// Non-credential fields must survive, or the operations view loses the
		// context that makes a job readable.
		if !strings.Contains(body, "example.test") {
			t.Fatalf("%s dropped a non-credential field: %s", path, body)
		}
	}
}

func newAuthenticatedTestServer(t *testing.T) (*controlplane.Server, *Module, *http.Cookie, string) {
	t.Helper()
	database, err := persistence.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := persistence.RunMigrations(context.Background(), database); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
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
		strings.NewReader(`{"username":"admin","password":"Str0ng-Console-Pass"}`))
	bootstrapRequest.RemoteAddr = "127.0.0.1:12345"
	bootstrapResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(bootstrapResponse, bootstrapRequest)
	if bootstrapResponse.Code != http.StatusCreated {
		t.Fatalf("bootstrap = %d %s", bootstrapResponse.Code, bootstrapResponse.Body.String())
	}
	var cookie *http.Cookie
	csrfToken := ""
	for _, issued := range bootstrapResponse.Result().Cookies() {
		switch issued.Name {
		case "nexa_session":
			cookie = issued
		case "nexa_csrf":
			csrfToken = issued.Value
		}
	}
	if cookie == nil || csrfToken == "" {
		t.Fatalf("bootstrap did not issue both session and csrf cookies")
	}
	now := time.Now().UTC()
	if _, err := database.ExecContext(ctx, "UPDATE identity_users SET totp_confirmed_at = ?", now); err != nil {
		t.Fatalf("mark test MFA enrollment: %v", err)
	}
	if _, err := database.ExecContext(ctx, "UPDATE identity_sessions SET mfa_verified_at = ?", now); err != nil {
		t.Fatalf("mark test session verified: %v", err)
	}
	return server, jobsModule, cookie, csrfToken
}

func newTestModule(t *testing.T) (*Module, *audit.Module) {
	t.Helper()
	database, err := persistence.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := persistence.RunMigrations(context.Background(), database); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
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
