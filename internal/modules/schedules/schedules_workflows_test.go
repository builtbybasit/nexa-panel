package schedules

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/modules/sites"
	"github.com/nexa-panel/nexa-panel/internal/platform/audit"
	"github.com/nexa-panel/nexa-panel/internal/platform/authorization"
	"github.com/nexa-panel/nexa-panel/internal/platform/identity"
	"github.com/nexa-panel/nexa-panel/internal/platform/jobs"
	scheduleoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/schedules"
	"github.com/nexa-panel/nexa-panel/internal/platform/operators/sitefs"
	"github.com/nexa-panel/nexa-panel/internal/platform/persistence"
	"github.com/nexa-panel/nexa-panel/internal/platform/secrets"
	"github.com/uptrace/bun"
)

type fakeCatalog map[string]sites.Site

func (c fakeCatalog) Get(_ context.Context, id string) (sites.Site, error) {
	site, ok := c[id]
	if !ok {
		return sites.Site{}, sql.ErrNoRows
	}
	return site, nil
}

// fakeAccessPolicy mirrors the identity module's grant semantics: only
// developers are scoped.
type fakeAccessPolicy struct{ allowed map[string]bool }

func (p fakeAccessPolicy) SiteAccessible(_ context.Context, user identity.User, siteID string) (bool, error) {
	if user.Role != "developer" {
		return true, nil
	}
	return p.allowed[siteID], nil
}

type operatorCall struct {
	method  string
	task    scheduleoperator.Task
	scope   sitefs.Scope
	removal bool
	plan    scheduleoperator.Plan
}

type fakeOperator struct {
	mu      sync.Mutex
	calls   []operatorCall
	fail    error
	planTTL time.Duration
}

func (o *fakeOperator) record(call operatorCall) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.calls = append(o.calls, call)
}

func (o *fakeOperator) last() operatorCall {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.calls[len(o.calls)-1]
}

func (o *fakeOperator) Plan(_ context.Context, task scheduleoperator.Task, removal bool) (scheduleoperator.Plan, error) {
	o.record(operatorCall{method: "Plan", task: task, removal: removal})
	if o.fail != nil {
		return scheduleoperator.Plan{}, o.fail
	}
	ttl := o.planTTL
	if ttl == 0 {
		ttl = scheduleoperator.PlanTTL
	}
	now := time.Now().UTC()
	return scheduleoperator.Plan{
		ID: strings.Repeat("0", 32), Kind: scheduleoperator.PlanKind, Task: task,
		Artifacts: []scheduleoperator.Artifact{
			{Path: "/etc/nexa-panel/generated/tasks/nexa-task-" + task.ID + ".sh", Mode: 0o750, Content: "#!/bin/sh\n", Owner: "root", Group: task.Scope.UnixUser, Remove: removal},
			{Path: "/etc/cron.d/nexa-task-" + task.ID, Mode: 0o644, Content: "* * * * *\n", Owner: "root", Group: "root", Remove: removal || !task.Enabled},
		},
		Before:    []scheduleoperator.Snapshot{{Path: "a"}, {Path: "b"}},
		Warnings:  []string{},
		PlannedAt: now, ExpiresAt: now.Add(ttl), Signature: "signed-by-agent",
	}, nil
}

func (o *fakeOperator) Apply(_ context.Context, plan scheduleoperator.Plan) (scheduleoperator.Observation, error) {
	o.record(operatorCall{method: "Apply", plan: plan})
	if o.fail != nil {
		return scheduleoperator.Observation{}, o.fail
	}
	return scheduleoperator.Observation{TaskID: plan.Task.ID, VerifiedAt: time.Now().UTC()}, nil
}

func (o *fakeOperator) Rollback(_ context.Context, plan scheduleoperator.Plan) (scheduleoperator.Observation, error) {
	o.record(operatorCall{method: "Rollback", plan: plan})
	if o.fail != nil {
		return scheduleoperator.Observation{}, o.fail
	}
	return scheduleoperator.Observation{TaskID: plan.Task.ID, VerifiedAt: time.Now().UTC()}, nil
}

func (o *fakeOperator) Run(_ context.Context, task scheduleoperator.Task) (scheduleoperator.RunResult, error) {
	o.record(operatorCall{method: "Run", task: task})
	if o.fail != nil {
		return scheduleoperator.RunResult{}, o.fail
	}
	return scheduleoperator.RunResult{ExitCode: 0, DurationMs: 42, StartedAt: time.Now().UTC(), OutputTail: "ok\n"}, nil
}

func (o *fakeOperator) Runs(_ context.Context, scope sitefs.Scope, taskID string) ([]scheduleoperator.RunRecord, error) {
	o.record(operatorCall{method: "Runs", scope: scope, task: scheduleoperator.Task{ID: taskID}})
	if o.fail != nil {
		return nil, o.fail
	}
	return []scheduleoperator.RunRecord{
		{StartedAt: time.Date(2026, 7, 16, 10, 0, 0, 0, time.UTC), DurationSeconds: 2, ExitCode: 0, Trigger: "cron"},
		{StartedAt: time.Date(2026, 7, 16, 10, 5, 0, 0, time.UTC), ExitCode: -1, Trigger: "manual"},
	}, nil
}

type authenticatedRegistry struct {
	mux            *http.ServeMux
	authentication interface {
		Middleware(http.Handler) http.Handler
	}
	authorization *authorization.Policy
}

func (r authenticatedRegistry) Handle(pattern string, handler http.Handler) error {
	r.mux.Handle(pattern, handler)
	return nil
}

func (r authenticatedRegistry) HandleAuthenticated(pattern string, handler http.Handler) error {
	r.mux.Handle(pattern, r.authentication.Middleware(handler))
	return nil
}

func (r authenticatedRegistry) HandleAuthorized(pattern, permission string, handler http.Handler) error {
	r.mux.Handle(pattern, r.authentication.Middleware(r.authorization.Middleware(permission, handler)))
	return nil
}

func seedIdentitySession(t *testing.T, database *bun.DB, id, username, role, token string) *http.Cookie {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	if _, err := database.ExecContext(ctx,
		`INSERT INTO identity_users (id, username, password_hash, created_at, role, totp_confirmed_at) VALUES (?, ?, 'unused', ?, ?, ?)`,
		id, username, now, role, now); err != nil {
		t.Fatalf("seed user %s: %v", username, err)
	}
	hash := sha256.Sum256([]byte(token))
	if _, err := database.ExecContext(ctx,
		`INSERT INTO identity_sessions (id, user_id, token_hash, created_at, expires_at, last_seen_at, mfa_verified_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"session_"+id, id, hash[:], now, now.Add(time.Hour), now, now); err != nil {
		t.Fatalf("seed session for %s: %v", username, err)
	}
	return &http.Cookie{Name: "nexa_session", Value: token}
}

type harness struct {
	mux      *http.ServeMux
	operator *fakeOperator
	queue    *jobs.Module
	module   *Module
	site     sites.Site
	inactive sites.Site
	failed   sites.Site
	draft    sites.Site
	admin    *http.Cookie
	dev      *http.Cookie
	viewer   *http.Cookie
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	ctx := context.Background()
	database, err := persistence.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	auditLog, err := audit.New(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	secretBox, err := secrets.New(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	identityModule, err := identity.NewWithConfig(ctx, database, auditLog, secretBox, logger, identity.Config{
		SessionTTL: time.Hour, PasswordMemoryKiB: 64, PasswordIterations: 1, PasswordThreads: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	queue, err := jobs.NewWithConfig(ctx, database, auditLog, logger, jobs.Config{PollInterval: 5 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	queue.Start(context.Background())
	t.Cleanup(queue.Close)

	active := sites.Site{ID: "site_active", Slug: "granted", RootPath: "/srv/nexa/sites/granted", UnixUser: "nexa_granted", Status: sites.StatusActive}
	inactive := sites.Site{ID: "site_planning", Slug: "pending", RootPath: "/srv/nexa/sites/pending", UnixUser: "nexa_pending", Status: sites.StatusPlanning}
	failed := sites.Site{ID: "site_failed", Slug: "broken", RootPath: "/srv/nexa/sites/broken", UnixUser: "nexa_broken", Status: sites.StatusFailed}
	draft := sites.Site{ID: "site_draft", Slug: "sketch", RootPath: "/srv/nexa/sites/sketch", UnixUser: "nexa_sketch", Status: sites.StatusDraft}
	operator := &fakeOperator{}
	catalog := fakeCatalog{active.ID: active, inactive.ID: inactive, failed.ID: failed, draft.ID: draft}
	schedulesModule, err := New(ctx, database, queue, catalog, fakeAccessPolicy{allowed: map[string]bool{active.ID: true}}, operator)
	if err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	registry := authenticatedRegistry{mux: mux, authentication: identityModule, authorization: authorization.New()}
	if err := schedulesModule.Register(registry); err != nil {
		t.Fatal(err)
	}
	return &harness{
		mux: mux, operator: operator, queue: queue, module: schedulesModule,
		site: active, inactive: inactive, failed: failed, draft: draft,
		admin:  seedIdentitySession(t, database, "admin-1", "root", "admin", strings.Repeat("a", 64)),
		dev:    seedIdentitySession(t, database, "dev-1", "dev", "developer", strings.Repeat("b", 64)),
		viewer: seedIdentitySession(t, database, "viewer-1", "viewer", "viewer", strings.Repeat("c", 64)),
	}
}

func (h *harness) do(t *testing.T, method, path string, body string, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	request := httptest.NewRequest(method, path, reader)
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	h.mux.ServeHTTP(response, request)
	return response
}

func (h *harness) waitForJob(t *testing.T, id int64) jobs.Job {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		job, err := h.queue.Get(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if job.State.Terminal() {
			return job
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("job did not complete")
	return jobs.Job{}
}

type acceptedTask struct {
	Task Task     `json:"task"`
	Job  jobs.Job `json:"job"`
}

func (h *harness) decodeAccepted(t *testing.T, response *httptest.ResponseRecorder) acceptedTask {
	t.Helper()
	if response.Code != http.StatusAccepted {
		t.Fatalf("accepted status = %d %s", response.Code, response.Body.String())
	}
	var accepted acceptedTask
	if err := json.Unmarshal(response.Body.Bytes(), &accepted); err != nil {
		t.Fatalf("decode accepted %q: %v", response.Body.String(), err)
	}
	return accepted
}

func (h *harness) getTask(t *testing.T, siteID, taskID string) (Task, *httptest.ResponseRecorder) {
	t.Helper()
	response := h.do(t, http.MethodGet, "/api/v1/sites/"+siteID+"/tasks/"+taskID, "", h.admin)
	var payload struct {
		Task Task `json:"task"`
	}
	if response.Code == http.StatusOK {
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
			t.Fatalf("decode task %q: %v", response.Body.String(), err)
		}
	}
	return payload.Task, response
}

// provision drives a task through create → plan → apply and returns it active.
func (h *harness) provision(t *testing.T, body string) Task {
	t.Helper()
	accepted := h.decodeAccepted(t, h.do(t, http.MethodPost, "/api/v1/sites/"+h.site.ID+"/tasks", body, h.admin))
	if job := h.waitForJob(t, accepted.Job.ID); job.State != jobs.StateSucceeded {
		t.Fatalf("plan job = %+v", job)
	}
	response := h.do(t, http.MethodPost, "/api/v1/sites/"+h.site.ID+"/tasks/"+accepted.Task.ID+"/apply", "", h.admin)
	if response.Code != http.StatusAccepted {
		t.Fatalf("apply = %d %s", response.Code, response.Body.String())
	}
	var applyJob jobs.Job
	if err := json.Unmarshal(response.Body.Bytes(), &applyJob); err != nil {
		t.Fatal(err)
	}
	if job := h.waitForJob(t, applyJob.ID); job.State != jobs.StateSucceeded {
		t.Fatalf("apply job = %+v", job)
	}
	task, _ := h.getTask(t, h.site.ID, accepted.Task.ID)
	if task.Status != StatusActive {
		t.Fatalf("provisioned task = %+v", task)
	}
	return task
}

func errorCode(t *testing.T, response *httptest.ResponseRecorder) string {
	t.Helper()
	var payload struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode error payload %q: %v", response.Body.String(), err)
	}
	return payload.Code
}

const createBody = `{"name":"Nightly backup","cronExpression":"0 3 * * *","command":"php artisan backup:run","timeoutSeconds":120,"enabled":true}`

func TestSchedulesLifecycle(t *testing.T) {
	h := newHarness(t)

	// Create renders a plan through a job and parks the task at plan_ready.
	accepted := h.decodeAccepted(t, h.do(t, http.MethodPost, "/api/v1/sites/"+h.site.ID+"/tasks", createBody, h.admin))
	if accepted.Task.Status != StatusPlanning || accepted.Job.Kind != "schedule.plan" || accepted.Task.SiteID != h.site.ID {
		t.Fatalf("create accepted = %+v", accepted)
	}
	if len(accepted.Task.ID) != 32 {
		t.Fatalf("task id shape = %q", accepted.Task.ID)
	}
	if job := h.waitForJob(t, accepted.Job.ID); job.State != jobs.StateSucceeded {
		t.Fatalf("plan job = %+v", job)
	}
	call := h.operator.last()
	if call.method != "Plan" || call.removal || call.task.ID != accepted.Task.ID || call.task.Scope.RootPath != h.site.RootPath || call.task.Scope.UnixUser != h.site.UnixUser {
		t.Fatalf("plan call = %+v", call)
	}

	// The plan preview is embedded while the task is plan_ready.
	response := h.do(t, http.MethodGet, "/api/v1/sites/"+h.site.ID+"/tasks/"+accepted.Task.ID, "", h.admin)
	var detail struct {
		Task          Task                   `json:"task"`
		Plan          *scheduleoperator.Plan `json:"plan"`
		PlanExpiresAt *time.Time             `json:"planExpiresAt"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &detail); err != nil || response.Code != http.StatusOK {
		t.Fatalf("get = %d %s (%v)", response.Code, response.Body.String(), err)
	}
	if detail.Task.Status != StatusPlanReady || detail.Plan == nil || len(detail.Plan.Artifacts) != 2 || detail.Plan.Artifacts[0].Content == "" || detail.PlanExpiresAt == nil {
		t.Fatalf("plan preview = %+v", detail)
	}

	// Apply executes the persisted signed plan and activates the task.
	apply := h.do(t, http.MethodPost, "/api/v1/sites/"+h.site.ID+"/tasks/"+accepted.Task.ID+"/apply", "", h.admin)
	if apply.Code != http.StatusAccepted {
		t.Fatalf("apply = %d %s", apply.Code, apply.Body.String())
	}
	var applyJob jobs.Job
	if err := json.Unmarshal(apply.Body.Bytes(), &applyJob); err != nil || applyJob.Kind != "schedule.apply" {
		t.Fatalf("apply response = %s (%v)", apply.Body.String(), err)
	}
	if job := h.waitForJob(t, applyJob.ID); job.State != jobs.StateSucceeded {
		t.Fatalf("apply job = %+v", job)
	}
	if call := h.operator.last(); call.method != "Apply" || call.plan.Task.ID != accepted.Task.ID || call.plan.Signature != "signed-by-agent" {
		t.Fatalf("apply call = %+v", call)
	}
	task, _ := h.getTask(t, h.site.ID, accepted.Task.ID)
	if task.Status != StatusActive || task.Failure != "" {
		t.Fatalf("active task = %+v", task)
	}

	// A second apply needs a fresh plan.
	if response := h.do(t, http.MethodPost, "/api/v1/sites/"+h.site.ID+"/tasks/"+accepted.Task.ID+"/apply", "", h.admin); response.Code != http.StatusConflict || errorCode(t, response) != "schedule_not_ready" {
		t.Fatalf("re-apply = %d %s", response.Code, response.Body.String())
	}

	// Rollback restores the captured before-state.
	rollback := h.do(t, http.MethodPost, "/api/v1/sites/"+h.site.ID+"/tasks/"+accepted.Task.ID+"/rollback", "", h.admin)
	var rollbackJob jobs.Job
	if err := json.Unmarshal(rollback.Body.Bytes(), &rollbackJob); err != nil || rollback.Code != http.StatusAccepted || rollbackJob.Kind != "schedule.rollback" {
		t.Fatalf("rollback = %d %s (%v)", rollback.Code, rollback.Body.String(), err)
	}
	if job := h.waitForJob(t, rollbackJob.ID); job.State != jobs.StateSucceeded {
		t.Fatalf("rollback job = %+v", job)
	}
	if task, _ := h.getTask(t, h.site.ID, accepted.Task.ID); task.Status != StatusRolledBack {
		t.Fatalf("rolled back task = %+v", task)
	}

	// Updates re-enter planning with the new desired state.
	update := h.decodeAccepted(t, h.do(t, http.MethodPut, "/api/v1/sites/"+h.site.ID+"/tasks/"+accepted.Task.ID,
		`{"name":"Nightly backup","cronExpression":"30 4 * * *","command":"php artisan backup:run --fresh","timeoutSeconds":300,"enabled":false}`, h.admin))
	if update.Task.Status != StatusPlanning || update.Task.CronExpression != "30 4 * * *" || update.Task.Enabled {
		t.Fatalf("update accepted = %+v", update)
	}
	if job := h.waitForJob(t, update.Job.ID); job.State != jobs.StateSucceeded {
		t.Fatalf("replan job = %+v", job)
	}
	if call := h.operator.last(); call.method != "Plan" || call.removal || call.task.CronExpression != "30 4 * * *" || call.task.TimeoutSeconds != 300 || call.task.Enabled {
		t.Fatalf("replan call = %+v", call)
	}

	// Deleting plans a removal; applying it retires the row entirely.
	deletion := h.decodeAccepted(t, h.do(t, http.MethodDelete, "/api/v1/sites/"+h.site.ID+"/tasks/"+accepted.Task.ID, "", h.admin))
	if !deletion.Task.PendingRemoval || deletion.Task.Status != StatusPlanning {
		t.Fatalf("deletion accepted = %+v", deletion)
	}
	if job := h.waitForJob(t, deletion.Job.ID); job.State != jobs.StateSucceeded {
		t.Fatalf("removal plan job = %+v", job)
	}
	if call := h.operator.last(); call.method != "Plan" || !call.removal {
		t.Fatalf("removal plan call = %+v", call)
	}
	removalApply := h.do(t, http.MethodPost, "/api/v1/sites/"+h.site.ID+"/tasks/"+accepted.Task.ID+"/apply", "", h.admin)
	var removalJob jobs.Job
	if err := json.Unmarshal(removalApply.Body.Bytes(), &removalJob); err != nil || removalApply.Code != http.StatusAccepted {
		t.Fatalf("removal apply = %d %s (%v)", removalApply.Code, removalApply.Body.String(), err)
	}
	if job := h.waitForJob(t, removalJob.ID); job.State != jobs.StateSucceeded {
		t.Fatalf("removal apply job = %+v", job)
	}
	if _, response := h.getTask(t, h.site.ID, accepted.Task.ID); response.Code != http.StatusNotFound {
		t.Fatalf("removed task get = %d %s", response.Code, response.Body.String())
	}
	list := h.do(t, http.MethodGet, "/api/v1/sites/"+h.site.ID+"/tasks", "", h.admin)
	var listing struct {
		Items []Task `json:"items"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &listing); err != nil || len(listing.Items) != 0 {
		t.Fatalf("post-removal list = %s (%v)", list.Body.String(), err)
	}
}

func TestSchedulesPlanFailureMarksTaskFailed(t *testing.T) {
	h := newHarness(t)
	h.operator.fail = &scheduleoperator.OperationError{Code: scheduleoperator.CodeInvalid, Message: "renderer said no"}
	accepted := h.decodeAccepted(t, h.do(t, http.MethodPost, "/api/v1/sites/"+h.site.ID+"/tasks", createBody, h.admin))
	if job := h.waitForJob(t, accepted.Job.ID); job.State != jobs.StateFailed {
		t.Fatalf("failed plan job = %+v", job)
	}
	task, _ := h.getTask(t, h.site.ID, accepted.Task.ID)
	if task.Status != StatusFailed || !strings.Contains(task.Failure, "renderer said no") {
		t.Fatalf("failed task = %+v", task)
	}
	// A failed task can be re-planned via update once the operator recovers.
	h.operator.fail = nil
	update := h.decodeAccepted(t, h.do(t, http.MethodPut, "/api/v1/sites/"+h.site.ID+"/tasks/"+task.ID, createBody, h.admin))
	if job := h.waitForJob(t, update.Job.ID); job.State != jobs.StateSucceeded {
		t.Fatalf("recovery replan = %+v", job)
	}
	if task, _ := h.getTask(t, h.site.ID, task.ID); task.Status != StatusPlanReady || task.Failure != "" {
		t.Fatalf("recovered task = %+v", task)
	}
}

func TestSchedulesRunGuardsAndResult(t *testing.T) {
	h := newHarness(t)

	// A task that is not yet applied cannot run.
	accepted := h.decodeAccepted(t, h.do(t, http.MethodPost, "/api/v1/sites/"+h.site.ID+"/tasks", createBody, h.admin))
	if job := h.waitForJob(t, accepted.Job.ID); job.State != jobs.StateSucceeded {
		t.Fatalf("plan job = %+v", job)
	}
	if response := h.do(t, http.MethodPost, "/api/v1/sites/"+h.site.ID+"/tasks/"+accepted.Task.ID+"/run", "", h.admin); response.Code != http.StatusConflict || errorCode(t, response) != "schedule_not_runnable" {
		t.Fatalf("run before apply = %d %s", response.Code, response.Body.String())
	}

	// A disabled task cannot run even while active.
	disabled := h.provision(t, `{"name":"Disabled","cronExpression":"* * * * *","command":"true","timeoutSeconds":60,"enabled":false}`)
	if response := h.do(t, http.MethodPost, "/api/v1/sites/"+h.site.ID+"/tasks/"+disabled.ID+"/run", "", h.admin); response.Code != http.StatusConflict || errorCode(t, response) != "schedule_not_runnable" {
		t.Fatalf("run disabled = %d %s", response.Code, response.Body.String())
	}

	// An active, enabled task runs with the worker-capped timeout.
	long := h.provision(t, `{"name":"Long report","cronExpression":"0 1 * * *","command":"php artisan report","timeoutSeconds":3600,"enabled":true}`)
	run := h.do(t, http.MethodPost, "/api/v1/sites/"+h.site.ID+"/tasks/"+long.ID+"/run", "", h.admin)
	if run.Code != http.StatusAccepted {
		t.Fatalf("run = %d %s", run.Code, run.Body.String())
	}
	var runJob jobs.Job
	if err := json.Unmarshal(run.Body.Bytes(), &runJob); err != nil || runJob.Kind != "schedule.run" {
		t.Fatalf("run response = %s (%v)", run.Body.String(), err)
	}
	finished := h.waitForJob(t, runJob.ID)
	if finished.State != jobs.StateSucceeded {
		t.Fatalf("run job = %+v", finished)
	}
	var result scheduleoperator.RunResult
	if err := json.Unmarshal(finished.Result, &result); err != nil || result.ExitCode != 0 || result.OutputTail != "ok\n" || result.DurationMs != 42 {
		t.Fatalf("run result = %s (%v)", finished.Result, err)
	}
	call := h.operator.last()
	if call.method != "Run" || call.task.ID != long.ID || call.task.TimeoutSeconds != 900 {
		t.Fatalf("run call must cap the timeout at 900s: %+v", call)
	}

	// Run records come straight from the agent.
	runs := h.do(t, http.MethodGet, "/api/v1/sites/"+h.site.ID+"/tasks/"+long.ID+"/runs", "", h.admin)
	var history struct {
		Items []scheduleoperator.RunRecord `json:"items"`
	}
	if err := json.Unmarshal(runs.Body.Bytes(), &history); err != nil || runs.Code != http.StatusOK || len(history.Items) != 2 || history.Items[1].ExitCode != -1 {
		t.Fatalf("runs = %d %s (%v)", runs.Code, runs.Body.String(), err)
	}
	if call := h.operator.last(); call.method != "Runs" || call.scope.SiteID != h.site.ID || call.task.ID != long.ID {
		t.Fatalf("runs call = %+v", call)
	}
}

func TestSchedulesSiteGuardsAndScoping(t *testing.T) {
	h := newHarness(t)

	if response := h.do(t, http.MethodGet, "/api/v1/sites/site_absent/tasks", "", h.admin); response.Code != http.StatusNotFound {
		t.Fatalf("missing site = %d %s", response.Code, response.Body.String())
	}
	// Mutations require an active site; reads only require leaving draft.
	if response := h.do(t, http.MethodPost, "/api/v1/sites/"+h.inactive.ID+"/tasks", createBody, h.admin); response.Code != http.StatusConflict || errorCode(t, response) != "site_not_active" {
		t.Fatalf("create on inactive = %d %s", response.Code, response.Body.String())
	}
	if response := h.do(t, http.MethodGet, "/api/v1/sites/"+h.failed.ID+"/tasks", "", h.admin); response.Code != http.StatusOK {
		t.Fatalf("list on failed site = %d %s", response.Code, response.Body.String())
	}
	if response := h.do(t, http.MethodGet, "/api/v1/sites/"+h.draft.ID+"/tasks", "", h.admin); response.Code != http.StatusConflict || errorCode(t, response) != "site_not_active" {
		t.Fatalf("list on draft site = %d %s", response.Code, response.Body.String())
	}
	if len(h.operator.calls) != 0 {
		t.Fatal("guards must run before the operator is called")
	}

	// Developers see ungranted sites as missing, not forbidden.
	if response := h.do(t, http.MethodGet, "/api/v1/sites/"+h.site.ID+"/tasks", "", h.dev); response.Code != http.StatusOK {
		t.Fatalf("developer granted list = %d %s", response.Code, response.Body.String())
	}
	hidden := h.do(t, http.MethodGet, "/api/v1/sites/"+h.failed.ID+"/tasks", "", h.dev)
	if hidden.Code != http.StatusNotFound || errorCode(t, hidden) != "site_not_found" {
		t.Fatalf("developer hidden site = %d %s", hidden.Code, hidden.Body.String())
	}

	// Permission wiring: viewers read, only writers mutate.
	if response := h.do(t, http.MethodGet, "/api/v1/sites/"+h.site.ID+"/tasks", "", h.viewer); response.Code != http.StatusOK {
		t.Fatalf("viewer list = %d %s", response.Code, response.Body.String())
	}
	if response := h.do(t, http.MethodPost, "/api/v1/sites/"+h.site.ID+"/tasks", createBody, h.viewer); response.Code != http.StatusForbidden {
		t.Fatalf("viewer create = %d %s", response.Code, response.Body.String())
	}
	if response := h.do(t, http.MethodPost, "/api/v1/sites/"+h.site.ID+"/tasks", createBody, h.dev); response.Code != http.StatusAccepted {
		t.Fatalf("developer create = %d %s", response.Code, response.Body.String())
	}
	// Planning remains available to schedule writers, but applying, rolling
	// back, and manually running site-user commands require the sensitive
	// operations permission (and its recent-MFA step-up).
	task := h.provision(t, `{"name":"Scoped","cronExpression":"* * * * *","command":"true","timeoutSeconds":60,"enabled":true}`)
	for _, action := range []string{"apply", "rollback", "run"} {
		response := h.do(t, http.MethodPost, "/api/v1/sites/"+h.site.ID+"/tasks/"+task.ID+"/"+action, "", h.dev)
		if response.Code != http.StatusForbidden || errorCode(t, response) != "permission_denied" {
			t.Fatalf("developer %s = %d %s", action, response.Code, response.Body.String())
		}
	}

	// Tasks are scoped to their site: the same task ID under another site 404s.
	if response := h.do(t, http.MethodGet, "/api/v1/sites/"+h.failed.ID+"/tasks/"+task.ID, "", h.admin); response.Code != http.StatusNotFound || errorCode(t, response) != "schedule_not_found" {
		t.Fatalf("cross-site task get = %d %s", response.Code, response.Body.String())
	}
	if response := h.do(t, http.MethodGet, "/api/v1/sites/"+h.failed.ID+"/tasks/"+task.ID+"/runs", "", h.admin); response.Code != http.StatusNotFound {
		t.Fatalf("cross-site runs = %d %s", response.Code, response.Body.String())
	}
}

func TestSchedulesParameterValidation(t *testing.T) {
	h := newHarness(t)
	base := "/api/v1/sites/" + h.site.ID + "/tasks"
	invalid := map[string]string{
		"bad cron":         `{"name":"x","cronExpression":"@daily","command":"true","timeoutSeconds":60,"enabled":true}`,
		"six cron fields":  `{"name":"x","cronExpression":"* * * * * *","command":"true","timeoutSeconds":60,"enabled":true}`,
		"cron bounds":      `{"name":"x","cronExpression":"60 * * * *","command":"true","timeoutSeconds":60,"enabled":true}`,
		"empty name":       `{"name":"","cronExpression":"* * * * *","command":"true","timeoutSeconds":60,"enabled":true}`,
		"oversized name":   `{"name":"` + strings.Repeat("n", 65) + `","cronExpression":"* * * * *","command":"true","timeoutSeconds":60,"enabled":true}`,
		"newline command":  `{"name":"x","cronExpression":"* * * * *","command":"true\ntrue","timeoutSeconds":60,"enabled":true}`,
		"empty command":    `{"name":"x","cronExpression":"* * * * *","command":"","timeoutSeconds":60,"enabled":true}`,
		"timeout too low":  `{"name":"x","cronExpression":"* * * * *","command":"true","timeoutSeconds":5,"enabled":true}`,
		"timeout too high": `{"name":"x","cronExpression":"* * * * *","command":"true","timeoutSeconds":90000,"enabled":true}`,
	}
	for name, body := range invalid {
		t.Run(name, func(t *testing.T) {
			response := h.do(t, http.MethodPost, base, body, h.admin)
			if response.Code != http.StatusUnprocessableEntity || errorCode(t, response) != "schedule_invalid" {
				t.Fatalf("create = %d %s", response.Code, response.Body.String())
			}
		})
	}
	if response := h.do(t, http.MethodPost, base, `{"name":"x","unknown":true}`, h.admin); response.Code != http.StatusBadRequest {
		t.Fatalf("unknown field = %d %s", response.Code, response.Body.String())
	}
	if len(h.operator.calls) != 0 {
		t.Fatal("invalid tasks must never reach the operator")
	}
}

func TestSchedulesPlanExpiryBlocksApply(t *testing.T) {
	h := newHarness(t)
	h.operator.planTTL = -time.Minute
	accepted := h.decodeAccepted(t, h.do(t, http.MethodPost, "/api/v1/sites/"+h.site.ID+"/tasks", createBody, h.admin))
	if job := h.waitForJob(t, accepted.Job.ID); job.State != jobs.StateSucceeded {
		t.Fatalf("plan job = %+v", job)
	}
	response := h.do(t, http.MethodPost, "/api/v1/sites/"+h.site.ID+"/tasks/"+accepted.Task.ID+"/apply", "", h.admin)
	if response.Code != http.StatusConflict || errorCode(t, response) != "schedule_plan_expired" {
		t.Fatalf("expired apply = %d %s", response.Code, response.Body.String())
	}
	for _, call := range h.operator.calls {
		if call.method == "Apply" {
			t.Fatal("an expired plan must never reach the operator")
		}
	}
}
