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
	if err := module.expireLaunches(ctx, admintooloperator.PHPMyAdmin); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := module.authorizeProxy(ctx, admintooloperator.PHPMyAdmin, user.ID, sessionRequest); err == nil {
		t.Fatal("expired phpMyAdmin session remained authorized across a lifecycle boundary")
	}

	firstPGLaunch, pgPath, err := module.CreateLaunch(ctx, admintooloperator.PGAdmin, LaunchRequest{SourceEngine: "postgresql", DatabaseID: "database-1", AccountID: "account-1"}, user, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	firstPGRequest := httptest.NewRequest("GET", pgPath, nil)
	firstPGRequest.AddCookie(&http.Cookie{Name: launchCookieName, Value: firstPGLaunch})
	_, firstPGSession, exchanged, err := module.authorizeProxy(ctx, admintooloperator.PGAdmin, user.ID, firstPGRequest)
	if err != nil || !exchanged {
		t.Fatalf("first pgAdmin launch exchange failed: %v", err)
	}
	if _, _, err := module.CreateLaunch(ctx, admintooloperator.PGAdmin, LaunchRequest{SourceEngine: "postgresql", DatabaseID: "database-2", AccountID: "account-2"}, user, "127.0.0.1"); err != nil {
		t.Fatal(err)
	}
	stalePGSession := httptest.NewRequest("GET", pgPath, nil)
	stalePGSession.AddCookie(&http.Cookie{Name: sessionCookieName, Value: firstPGSession})
	if _, _, _, err := module.authorizeProxy(ctx, admintooloperator.PGAdmin, user.ID, stalePGSession); err == nil {
		t.Fatal("previous pgAdmin session remained authorized after the global server catalog rotated")
	}
}

func TestPGAdminProxyHeaderIsDerivedServerSideAndStripsClientForgery(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/tools/pgadmin/", nil)
	request.Header.Set("X-Nexa-PgAdmin-forged", "attacker@nexa.example.com")
	sessionToken := "sessionCapabilityToken1234"
	header, err := setPGAdminSessionIdentity(request, sessionToken, "admin@nexa.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if request.Header.Get("X-Nexa-PgAdmin-forged") != "" {
		t.Fatal("client-supplied pgAdmin capability header was forwarded")
	}
	if got := request.Header.Get(header); got != "admin@nexa.example.com" {
		t.Fatalf("trusted header value = %q", got)
	}
	if strings.Contains(header, sessionToken) {
		t.Fatalf("trusted header name leaked the raw session token: %s", header)
	}
	if _, err := setPGAdminSessionIdentity(request, "short", "admin@nexa.example.com"); err == nil {
		t.Fatal("invalid session token was accepted for pgAdmin proxy authentication")
	}
}

func TestAdminToolProxyDoesNotForwardPanelCredentials(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "http://panel.example.com/tools/phpmyadmin/", nil)
	request.Header.Set("Authorization", "Bearer panel-secret")
	request.Header.Set("X-Nexa-Internal", "panel-secret")
	request.Header.Set("X-Forwarded-For", "198.51.100.7")
	request.AddCookie(&http.Cookie{Name: "nexa_session", Value: "panel-session"})
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "tool-gateway-session"})
	request.AddCookie(&http.Cookie{Name: "SignonSession", Value: "browser-forgery"})
	request.AddCookie(&http.Cookie{Name: "phpMyAdmin", Value: "upstream-session"})
	upstream := "SignonSession"

	sanitizeAdminToolProxyRequest(request, admintooloperator.PHPMyAdmin, &upstream)
	if request.Header.Get("Authorization") != "" || request.Header.Get("X-Nexa-Internal") != "" || request.Header.Get("X-Forwarded-For") != "" {
		t.Fatalf("panel or proxy credentials remained in upstream headers: %+v", request.Header)
	}
	if cookies := request.Cookies(); len(cookies) != 1 || cookies[0].Name != "phpMyAdmin" || cookies[0].Value != "upstream-session" {
		t.Fatalf("upstream cookies = %+v, want only the tool-owned session", cookies)
	}
}

func TestAdminToolProxyConfinesUpstreamRedirectsAndCookies(t *testing.T) {
	response := &http.Response{Header: make(http.Header)}
	response.Header.Set("Location", "//attacker.example/steal?next=1")
	response.Header.Set("X-Accel-Redirect", "/internal/secrets")
	response.Header.Add("Set-Cookie", "nexa_session=forged; Path=/; Domain=panel.example.com")
	response.Header.Add("Set-Cookie", "pga4_session=tool-session; Path=/; Domain=panel.example.com")

	rewriteAdminToolProxyResponse(response, admintooloperator.PGAdmin, "/tools/pgadmin", true)
	if got := response.Header.Get("Location"); got != "/tools/pgadmin/steal?next=1" {
		t.Fatalf("Location = %q, want a panel-local tool path", got)
	}
	if response.Header.Get("X-Accel-Redirect") != "" {
		t.Fatal("upstream X-Accel-Redirect escaped the application proxy")
	}
	cookies := response.Cookies()
	if len(cookies) != 1 || cookies[0].Name != "pga4_session" {
		t.Fatalf("response cookies = %+v, want only the tool cookie", cookies)
	}
	if cookies[0].Domain != "" || cookies[0].Path != "/tools/pgadmin/" || !cookies[0].HttpOnly || !cookies[0].Secure || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatalf("tool cookie was not securely scoped: %+v", cookies[0])
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
