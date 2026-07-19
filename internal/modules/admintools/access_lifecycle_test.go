package admintools

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/platform/audit"
	"github.com/nexa-panel/nexa-panel/internal/platform/identity"
	"github.com/nexa-panel/nexa-panel/internal/platform/jobs"
	admintooloperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/admintools"
	"github.com/nexa-panel/nexa-panel/internal/platform/persistence"
	"github.com/nexa-panel/nexa-panel/internal/platform/secrets"
)

type fakeOperator struct{ tools []admintooloperator.Tool }

func (f *fakeOperator) Discover(context.Context) ([]admintooloperator.Tool, error) {
	return append([]admintooloperator.Tool(nil), f.tools...), nil
}
func (f *fakeOperator) Plan(_ context.Context, change admintooloperator.Change) (admintooloperator.Plan, error) {
	now := time.Now().UTC()
	return admintooloperator.Plan{ID: "agent-plan", Kind: admintooloperator.PlanKind, Change: change, ObservedFingerprint: "observed", PlannedAt: now, ExpiresAt: now.Add(time.Hour), Signature: "signed"}, nil
}
func (f *fakeOperator) Apply(_ context.Context, execution admintooloperator.Execution) (admintooloperator.Observation, error) {
	plan := execution.Plan
	tool := plan.Change.Tool
	if plan.Change.Action == admintooloperator.ActionStop {
		tool.Status = "stopped"
	} else {
		tool.Status = "active"
	}
	observation := admintooloperator.Observation{Tool: tool, Verified: true}
	if plan.Change.Action == admintooloperator.ActionLaunch && plan.Change.Tool.Kind == admintooloperator.PHPMyAdmin {
		observation.UpstreamCookieName = "SignonSession"
		observation.UpstreamCookieValue = plan.Change.Launch.SessionID
	}
	return observation, nil
}

func TestLaunchTokenExchangesOnceForScopedHttpOnlySession(t *testing.T) {
	ctx := context.Background()
	database, err := persistence.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	auditLog, err := audit.New(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	queue, err := jobs.NewWithConfig(ctx, database, auditLog, slog.New(slog.NewTextHandler(io.Discard, nil)), jobs.Config{PollInterval: 5 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := secrets.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	tools := admintooloperator.Defaults()
	for index := range tools {
		tools[index].Status = "active"
	}
	operator := &fakeOperator{tools: tools}
	resolver := func(context.Context, string, string, string) (Credential, error) {
		return Credential{Host: "host.containers.internal", Port: 3306, Database: "app_db", Username: "app_user", Secret: []byte("database-secret")}, nil
	}
	module, err := New(ctx, database, queue, operator, WithLaunchGateway(cipher, resolver, auditLog))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = module.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	user := identity.User{ID: "user-1", Username: "admin", Role: "admin"}
	launchToken, path, err := module.CreateLaunch(ctx, admintooloperator.PHPMyAdmin, LaunchRequest{SourceEngine: "mysql", DatabaseID: "database-1", AccountID: "account-1"}, user, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(path, launchToken) || path != "/tools/phpmyadmin/" {
		t.Fatalf("browser path leaked token: %s", path)
	}
	request := httptest.NewRequest("GET", path, nil)
	request.AddCookie(&http.Cookie{Name: launchCookieName, Value: launchToken})
	model, sessionToken, exchanged, err := module.authorizeProxy(ctx, admintooloperator.PHPMyAdmin, user.ID, request)
	if err != nil || !exchanged || sessionToken == "" || model.UpstreamCookieName == nil {
		t.Fatalf("exchange model=%+v exchanged=%v err=%v", model, exchanged, err)
	}
	if _, _, _, err := module.authorizeProxy(ctx, admintooloperator.PHPMyAdmin, user.ID, request); err == nil {
		t.Fatal("launch token was reusable")
	}
	sessionRequest := httptest.NewRequest("GET", path, nil)
	sessionRequest.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sessionToken})
	_, observed, reexchanged, err := module.authorizeProxy(ctx, admintooloperator.PHPMyAdmin, user.ID, sessionRequest)
	if err != nil || reexchanged || observed != sessionToken {
		t.Fatalf("session authorization failed: %v", err)
	}
	events, err := auditLog.List(ctx, 10)
	if err != nil || len(events) == 0 || events[0].Action != "admin_tool.launch" {
		t.Fatalf("audit=%+v err=%v", events, err)
	}
}

func TestDeployAndStopUseReviewedJobs(t *testing.T) {
	ctx := context.Background()
	database, err := persistence.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	auditLog, err := audit.New(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	queue, err := jobs.NewWithConfig(ctx, database, auditLog, slog.New(slog.NewTextHandler(io.Discard, nil)), jobs.Config{PollInterval: 5 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	operator := &fakeOperator{tools: admintooloperator.Defaults()}
	module, err := New(ctx, database, queue, operator)
	if err != nil {
		t.Fatal(err)
	}
	queue.Start(ctx)
	defer queue.Close()
	tool, planJob, err := module.RequestChange(ctx, admintooloperator.PGAdmin, admintooloperator.ActionDeploy, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitAdminJob(t, queue, planJob.ID, jobs.StateSucceeded)
	stored, err := module.StoredPlan(ctx, tool.Kind)
	if err != nil || stored.AgentPlan.Signature != "signed" {
		t.Fatalf("stored=%+v err=%v", stored, err)
	}
	applyJob, err := module.ApplyPlan(ctx, tool.Kind, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitAdminJob(t, queue, applyJob.ID, jobs.StateSucceeded)
	items, err := module.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, item := range items {
		if item.Kind == admintooloperator.PGAdmin {
			found = true
			if item.Status != string(StatusActive) || item.MemoryMB != 256 || !item.OnDemand {
				t.Fatalf("pgadmin=%+v", item)
			}
		}
	}
	if !found {
		t.Fatal("pgAdmin not persisted")
	}
}
func TestSyncKeepsPlanReadyStatusApplicable(t *testing.T) {
	ctx := context.Background()
	database, err := persistence.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	auditLog, err := audit.New(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	queue, err := jobs.NewWithConfig(ctx, database, auditLog, slog.New(slog.NewTextHandler(io.Discard, nil)), jobs.Config{PollInterval: 5 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	// Discover reports the container as stopped (systemd is-active) — the state
	// a not-yet-deployed tool is really in while its plan awaits approval.
	operator := &fakeOperator{tools: admintooloperator.Defaults()}
	module, err := New(ctx, database, queue, operator)
	if err != nil {
		t.Fatal(err)
	}
	queue.Start(ctx)
	defer queue.Close()
	tool, planJob, err := module.RequestChange(ctx, admintooloperator.PGAdmin, admintooloperator.ActionDeploy, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitAdminJob(t, queue, planJob.ID, jobs.StateSucceeded)
	// Mirror the frontend refetching the admin-tools list after the plan job:
	// the resulting Sync must not clobber plan_ready with the observed status.
	if _, err = module.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = module.ApplyPlan(ctx, tool.Kind, nil); err != nil {
		t.Fatalf("plan_ready tool was not applicable after Sync: %v", err)
	}
}
func waitAdminJob(t *testing.T, queue *jobs.Module, id int64, wanted jobs.State) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		job, err := queue.Get(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if job.State.Terminal() {
			if job.State != wanted {
				t.Fatalf("job=%+v", job)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("job timeout")
}
