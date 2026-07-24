package admintools

import (
	"context"
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
	"github.com/nexa-panel/nexa-panel/internal/platform/identity"
	"github.com/nexa-panel/nexa-panel/internal/platform/jobs"
	admintooloperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/admintools"
	"github.com/nexa-panel/nexa-panel/internal/platform/persistence"
	"github.com/nexa-panel/nexa-panel/internal/platform/secrets"
)

type fakeOperator struct {
	tools      []admintooloperator.Tool
	keepAlives []admintooloperator.Kind
}

func (f *fakeOperator) Discover(context.Context) ([]admintooloperator.Tool, error) {
	return append([]admintooloperator.Tool(nil), f.tools...), nil
}

func (f *fakeOperator) KeepAlive(_ context.Context, kind admintooloperator.Kind) error {
	f.keepAlives = append(f.keepAlives, kind)
	return nil
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
	if err := persistence.RunMigrations(context.Background(), database); err != nil {
		t.Fatalf("run migrations: %v", err)
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
	replacementLaunch, _, err := module.CreateLaunch(ctx, admintooloperator.PHPMyAdmin, LaunchRequest{SourceEngine: "mysql", DatabaseID: "database-1", AccountID: "account-1"}, user, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	replacementRequest := httptest.NewRequest("GET", path, nil)
	replacementRequest.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sessionToken})
	replacementRequest.AddCookie(&http.Cookie{Name: launchCookieName, Value: replacementLaunch})
	_, replacementSession, exchanged, err := module.authorizeProxy(ctx, admintooloperator.PHPMyAdmin, user.ID, replacementRequest)
	if err != nil || !exchanged || replacementSession == sessionToken {
		t.Fatalf("fresh launch did not replace the established tool session: exchanged=%v err=%v", exchanged, err)
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

// newLaunchGatewayModule builds a launch-capable module on a real control-plane
// database with a clock the test drives, so session lifetime is exercised
// without sleeping.
func newLaunchGatewayModule(t *testing.T, clock *time.Time) *Module {
	t.Helper()
	ctx := context.Background()
	database, err := persistence.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := persistence.RunMigrations(ctx, database); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	t.Cleanup(func() { database.Close() })
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
	resolver := func(context.Context, string, string, string) (Credential, error) {
		return Credential{Host: "host.containers.internal", Port: 5432, Database: "app_db", Username: "app_user", Secret: []byte("database-secret")}, nil
	}
	module, err := New(ctx, database, queue, &fakeOperator{tools: tools}, WithLaunchGateway(cipher, resolver, auditLog))
	if err != nil {
		t.Fatal(err)
	}
	module.now = func() time.Time { return *clock }
	if _, err := module.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	return module
}

// toolSubRequest models the XHR/fetch traffic pgAdmin's SPA issues after the
// first navigation — the requests that were failing with a 401 minutes into a
// working session.
func toolSubRequest(kind admintooloperator.Kind, sessionToken string) *http.Request {
	request := httptest.NewRequest(http.MethodGet, "/tools/"+string(kind)+"/browser/nodes/server/1/", nil)
	request.Header.Set("X-Requested-With", "XMLHttpRequest")
	request.Header.Set("Accept", "application/json")
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sessionToken})
	return request
}

func establishToolSession(t *testing.T, module *Module, kind admintooloperator.Kind, engine string, user identity.User) string {
	t.Helper()
	ctx := context.Background()
	launchToken, path, err := module.CreateLaunch(ctx, kind, LaunchRequest{SourceEngine: engine, DatabaseID: "database-1", AccountID: "account-1"}, user, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, path, nil)
	request.AddCookie(&http.Cookie{Name: launchCookieName, Value: launchToken})
	_, sessionToken, exchanged, err := module.authorizeProxy(ctx, kind, user.ID, request)
	if err != nil || !exchanged {
		t.Fatalf("launch exchange failed: exchanged=%v err=%v", exchanged, err)
	}
	return sessionToken
}

func TestActiveToolSessionSlidesPastTheLaunchDeadlineButNotPastTheLifetimeCap(t *testing.T) {
	ctx := context.Background()
	clock := time.Now().UTC().Truncate(time.Second)
	module := newLaunchGatewayModule(t, &clock)
	user := identity.User{ID: "user-1", Username: "admin", Role: "admin"}
	sessionToken := establishToolSession(t, module, admintooloperator.PGAdmin, "postgresql", user)

	// Steady sub-request traffic well past the former fixed 15-minute deadline.
	for elapsed := 0; elapsed < 90; elapsed += 5 {
		clock = clock.Add(5 * time.Minute)
		model, observed, _, err := module.authorizeProxy(ctx, admintooloperator.PGAdmin, user.ID, toolSubRequest(admintooloperator.PGAdmin, sessionToken))
		if err != nil || observed != sessionToken {
			t.Fatalf("active tool session expired after %d minutes of continuous use: %v", elapsed+5, err)
		}
		if !model.SessionExpiresAt.After(clock) {
			t.Fatalf("session deadline %s was not slid past the clock %s", model.SessionExpiresAt, clock)
		}
	}
	// The slide is bounded: activity cannot keep a launch alive indefinitely.
	clock = clock.Add(toolSessionMaxLifetime)
	if _, _, _, err := module.authorizeProxy(ctx, admintooloperator.PGAdmin, user.ID, toolSubRequest(admintooloperator.PGAdmin, sessionToken)); err == nil {
		t.Fatal("tool session outlived the absolute lifetime cap")
	}
}

func TestToolSessionRenewalIsSkippedWhileMostOfTheIdleWindowRemains(t *testing.T) {
	ctx := context.Background()
	clock := time.Now().UTC().Truncate(time.Second)
	module := newLaunchGatewayModule(t, &clock)
	user := identity.User{ID: "user-1", Username: "admin", Role: "admin"}
	sessionToken := establishToolSession(t, module, admintooloperator.PGAdmin, "postgresql", user)

	clock = clock.Add(time.Minute)
	_, _, renewed, err := module.authorizeProxy(ctx, admintooloperator.PGAdmin, user.ID, toolSubRequest(admintooloperator.PGAdmin, sessionToken))
	if err != nil || renewed {
		t.Fatalf("an early sub-request wrote a renewal: renewed=%v err=%v", renewed, err)
	}
	clock = clock.Add(toolSessionRenewAfter)
	_, _, renewed, err = module.authorizeProxy(ctx, admintooloperator.PGAdmin, user.ID, toolSubRequest(admintooloperator.PGAdmin, sessionToken))
	if err != nil || !renewed {
		t.Fatalf("the deadline was not extended once the renewal threshold passed: renewed=%v err=%v", renewed, err)
	}
}

func TestIdleToolSessionExpires(t *testing.T) {
	ctx := context.Background()
	clock := time.Now().UTC().Truncate(time.Second)
	module := newLaunchGatewayModule(t, &clock)
	user := identity.User{ID: "user-1", Username: "admin", Role: "admin"}
	sessionToken := establishToolSession(t, module, admintooloperator.PGAdmin, "postgresql", user)

	clock = clock.Add(toolSessionIdleWindow + time.Minute)
	if _, _, _, err := module.authorizeProxy(ctx, admintooloperator.PGAdmin, user.ID, toolSubRequest(admintooloperator.PGAdmin, sessionToken)); err == nil {
		t.Fatal("an idle tool session remained authorized past the idle window")
	}
}

func TestToolSessionIsNotUsableByAnotherPanelUser(t *testing.T) {
	ctx := context.Background()
	clock := time.Now().UTC().Truncate(time.Second)
	module := newLaunchGatewayModule(t, &clock)
	owner := identity.User{ID: "user-1", Username: "admin", Role: "admin"}
	other := identity.User{ID: "user-2", Username: "operator", Role: "admin"}

	launchToken, path, err := module.CreateLaunch(ctx, admintooloperator.PHPMyAdmin, LaunchRequest{SourceEngine: "mysql", DatabaseID: "database-1", AccountID: "account-1"}, owner, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	stolenLaunch := httptest.NewRequest(http.MethodGet, path, nil)
	stolenLaunch.AddCookie(&http.Cookie{Name: launchCookieName, Value: launchToken})
	if _, _, _, err := module.authorizeProxy(ctx, admintooloperator.PHPMyAdmin, other.ID, stolenLaunch); err == nil {
		t.Fatal("a second panel user redeemed another user's launch token")
	}
	// The rejection must not have burned the owner's single-use launch.
	ownerLaunch := httptest.NewRequest(http.MethodGet, path, nil)
	ownerLaunch.AddCookie(&http.Cookie{Name: launchCookieName, Value: launchToken})
	_, sessionToken, exchanged, err := module.authorizeProxy(ctx, admintooloperator.PHPMyAdmin, owner.ID, ownerLaunch)
	if err != nil || !exchanged {
		t.Fatalf("owner could not redeem their own launch after the cross-user attempt: %v", err)
	}
	if _, _, _, err := module.authorizeProxy(ctx, admintooloperator.PHPMyAdmin, other.ID, toolSubRequest(admintooloperator.PHPMyAdmin, sessionToken)); err == nil {
		t.Fatal("a second panel user used another user's established tool session")
	}
	// A cross-user attempt must not slide the owner's deadline either.
	clock = clock.Add(toolSessionIdleWindow - time.Minute)
	if _, _, _, err := module.authorizeProxy(ctx, admintooloperator.PHPMyAdmin, other.ID, toolSubRequest(admintooloperator.PHPMyAdmin, sessionToken)); err == nil {
		t.Fatal("a second panel user used another user's established tool session")
	}
	clock = clock.Add(2 * time.Minute)
	if _, _, _, err := module.authorizeProxy(ctx, admintooloperator.PHPMyAdmin, owner.ID, toolSubRequest(admintooloperator.PHPMyAdmin, sessionToken)); err == nil {
		t.Fatal("a foreign request kept the owner's session alive")
	}
}

func TestPGAdminProxyHeaderIsDerivedServerSideAndStripsClientForgery(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/tools/pgadmin/", nil)
	request.Header.Set("X-Nexa-PgAdmin-forged", "attacker@nexa.example.com")
	sessionToken := "sessionCapabilityToken1234"
	header, err := setPGAdminSessionIdentity(request, sessionToken)
	if err != nil {
		t.Fatal(err)
	}
	if request.Header.Get("X-Nexa-PgAdmin-forged") != "" {
		t.Fatal("client-supplied pgAdmin capability header was forwarded")
	}
	if got := request.Header.Get(header); got != admintooloperator.PGAdminLoginUser {
		t.Fatalf("trusted header value = %q, want the fixed owner identity %q", got, admintooloperator.PGAdminLoginUser)
	}
	if strings.Contains(header, sessionToken) {
		t.Fatalf("trusted header name leaked the raw session token: %s", header)
	}
	if _, err := setPGAdminSessionIdentity(request, "short"); err == nil {
		t.Fatal("invalid session token was accepted for pgAdmin proxy authentication")
	}
}

func TestAdminToolProxyDoesNotForwardPanelCredentials(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "http://panel.example.com/tools/phpmyadmin/", nil)
	request.Header.Set("Authorization", "Bearer panel-secret")
	request.Header.Set("X-Nexa-Internal", "panel-secret")
	request.Header.Set("X-Forwarded-For", "198.51.100.7")
	request.Header.Set("X-Script-Name", "/attacker-prefix")
	request.Header.Set("X-Scheme", "javascript")
	request.AddCookie(&http.Cookie{Name: "nexa_session", Value: "panel-session"})
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "tool-gateway-session"})
	request.AddCookie(&http.Cookie{Name: "SignonSession", Value: "browser-forgery"})
	request.AddCookie(&http.Cookie{Name: "phpMyAdmin", Value: "upstream-session"})
	upstream := "SignonSession"

	sanitizeAdminToolProxyRequest(request, admintooloperator.PHPMyAdmin, &upstream)
	if request.Header.Get("Authorization") != "" || request.Header.Get("X-Nexa-Internal") != "" || request.Header.Get("X-Forwarded-For") != "" || request.Header.Get("X-Script-Name") != "" || request.Header.Get("X-Scheme") != "" {
		t.Fatalf("panel or proxy credentials remained in upstream headers: %+v", request.Header)
	}
	if cookies := request.Cookies(); len(cookies) != 1 || cookies[0].Name != "phpMyAdmin" || cookies[0].Value != "upstream-session" {
		t.Fatalf("upstream cookies = %+v, want only the tool-owned session", cookies)
	}
}

func TestPGAdminProxySetsServerOwnedSubpathMetadata(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "https://panel.example.com/tools/pgadmin/browser/", nil)
	request.Header.Set("X-Script-Name", "/attacker-prefix")
	request.Header.Set("X-Scheme", "javascript")
	sanitizeAdminToolProxyRequest(request, admintooloperator.PGAdmin, nil)
	setPGAdminProxyMetadata(request, "/tools/pgadmin", true)

	if got := request.Header.Get("X-Script-Name"); got != "/tools/pgadmin" {
		t.Fatalf("X-Script-Name = %q, want the server-owned tool prefix", got)
	}
	if got := request.Header.Get("X-Scheme"); got != "https" {
		t.Fatalf("X-Scheme = %q, want https", got)
	}
}

func TestAdminToolProxyConfinesUpstreamRedirectsAndCookies(t *testing.T) {
	response := &http.Response{Header: make(http.Header)}
	response.Header.Set("Location", "//attacker.example/steal?next=1")
	response.Header.Set("X-Accel-Redirect", "/internal/secrets")
	response.Header.Add("Set-Cookie", "nexa_session=forged; Path=/; Domain=panel.example.com")
	response.Header.Add("Set-Cookie", "pga4_session=tool-session; Path=/; Domain=panel.example.com")

	if err := rewriteAdminToolProxyResponse(response, admintooloperator.PGAdmin, "/tools/pgadmin", true); err != nil {
		t.Fatal(err)
	}
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

func TestPGAdminHTMLRootAssetsStayInsideTheAuthorizedToolPrefix(t *testing.T) {
	body := `<html><head><link href="/browser/browser.css"><script src='/static/vendor/require.min.js'></script></head><body><a href="/tools/pgadmin/browser/">Browser</a></body></html>`
	response := &http.Response{
		Header:        make(http.Header),
		Body:          io.NopCloser(strings.NewReader(body)),
		ContentLength: int64(len(body)),
	}
	response.Header.Set("Content-Type", "text/html; charset=utf-8")
	response.Header.Set("Content-Length", fmt.Sprint(len(body)))
	if err := rewriteAdminToolProxyResponse(response, admintooloperator.PGAdmin, "/tools/pgadmin", false); err != nil {
		t.Fatal(err)
	}
	rewritten, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	text := string(rewritten)
	for _, escaped := range []string{`href="/browser/`, `src='/static/`} {
		if strings.Contains(text, escaped) {
			t.Fatalf("pgAdmin asset escaped the authenticated proxy prefix: %s", text)
		}
	}
	for _, wanted := range []string{`href="/tools/pgadmin/browser/browser.css"`, `src='/tools/pgadmin/static/vendor/require.min.js'`} {
		if !strings.Contains(text, wanted) {
			t.Fatalf("rewritten pgAdmin page is missing %q: %s", wanted, text)
		}
	}
	if strings.Count(text, "/tools/pgadmin/tools/pgadmin/") != 0 {
		t.Fatalf("an already-prefixed URL was rewritten twice: %s", text)
	}
}

// phpMyAdmin is served natively rather than through pgAdmin's script-name
// contract, so the gateway must neither invent subpath metadata for it nor
// rewrite its HTML — while still confining its redirects and cookies.
func TestPHPMyAdminProxyGetsNoSubpathMetadata(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "https://panel.example.com/tools/phpmyadmin/index.php", nil)
	request.Header.Set("X-Script-Name", "/attacker-prefix")
	request.Header.Set("X-Scheme", "javascript")
	sanitizeAdminToolProxyRequest(request, admintooloperator.PHPMyAdmin, nil)

	if got := request.Header.Get("X-Script-Name"); got != "" {
		t.Fatalf("X-Script-Name = %q, want no forwarded value for phpMyAdmin", got)
	}
	if got := request.Header.Get("X-Scheme"); got != "" {
		t.Fatalf("X-Scheme = %q, want no forwarded value for phpMyAdmin", got)
	}
}

func TestPHPMyAdminProxyConfinesUpstreamRedirectsAndCookies(t *testing.T) {
	response := &http.Response{Header: make(http.Header)}
	response.Header.Set("Location", "//attacker.example/steal?next=1")
	response.Header.Set("X-Sendfile", "/internal/secrets")
	response.Header.Set("Access-Control-Allow-Origin", "*")
	response.Header.Add("Set-Cookie", "nexa_session=forged; Path=/; Domain=panel.example.com")
	response.Header.Add("Set-Cookie", "phpMyAdmin=tool-session; Path=/; Domain=panel.example.com")
	response.Header.Add("Set-Cookie", "pmaAuth-1=tool-auth; Path=/")

	if err := rewriteAdminToolProxyResponse(response, admintooloperator.PHPMyAdmin, "/tools/phpmyadmin", true); err != nil {
		t.Fatal(err)
	}
	if got := response.Header.Get("Location"); got != "/tools/phpmyadmin/steal?next=1" {
		t.Fatalf("Location = %q, want a panel-local tool path", got)
	}
	if response.Header.Get("X-Sendfile") != "" || response.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("upstream response controls escaped the application proxy: %+v", response.Header)
	}
	cookies := response.Cookies()
	if len(cookies) != 2 || cookies[0].Name != "phpMyAdmin" || cookies[1].Name != "pmaAuth-1" {
		t.Fatalf("response cookies = %+v, want only the tool cookies", cookies)
	}
	for _, cookie := range cookies {
		if cookie.Domain != "" || cookie.Path != "/tools/phpmyadmin/" || !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteStrictMode {
			t.Fatalf("tool cookie was not securely scoped: %+v", cookie)
		}
	}
}

func TestPHPMyAdminHTMLIsForwardedWithoutAssetRewriting(t *testing.T) {
	body := `<html><head><link href="themes/pmahomme/css/theme.css"></head><body><a href="index.php?route=/database">db</a></body></html>`
	response := &http.Response{
		Header:        make(http.Header),
		Body:          io.NopCloser(strings.NewReader(body)),
		ContentLength: int64(len(body)),
	}
	response.Header.Set("Content-Type", "text/html; charset=utf-8")
	response.Header.Set("Content-Length", fmt.Sprint(len(body)))
	if err := rewriteAdminToolProxyResponse(response, admintooloperator.PHPMyAdmin, "/tools/phpmyadmin", false); err != nil {
		t.Fatal(err)
	}
	forwarded, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(forwarded) != body {
		t.Fatalf("phpMyAdmin HTML was rewritten: %s", forwarded)
	}
	if got := response.Header.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
}

func TestDeployAndStopUseReviewedJobs(t *testing.T) {
	ctx := context.Background()
	database, err := persistence.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := persistence.RunMigrations(context.Background(), database); err != nil {
		t.Fatalf("run migrations: %v", err)
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
			if item.Status != string(StatusActive) || item.MemoryMB != 512 || !item.OnDemand {
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
	if err := persistence.RunMigrations(context.Background(), database); err != nil {
		t.Fatalf("run migrations: %v", err)
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
	// Discover reports the tool as stopped (systemd is-active) — the state
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

func TestSyncSurfacesIdleStoppedOnDemandTool(t *testing.T) {
	ctx := context.Background()
	database, err := persistence.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := persistence.RunMigrations(ctx, database); err != nil {
		t.Fatalf("run migrations: %v", err)
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
	// Both tools start running: the on-demand pgAdmin container and the always-on
	// native phpMyAdmin route.
	running := admintooloperator.Defaults()
	for index := range running {
		running[index].Status = "active"
	}
	operator := &fakeOperator{tools: running}
	module, err := New(ctx, database, queue, operator)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := module.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	// The idle-stop timer fires: the container is stopped but never uninstalled, so
	// systemd now reports both tools inactive. The idle-stop unit does not touch the
	// panel database, so this Sync is the first observation of the change.
	stopped := admintooloperator.Defaults()
	for index := range stopped {
		stopped[index].Status = "stopped"
	}
	operator.tools = stopped
	items, err := module.Sync(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byKind := map[admintooloperator.Kind]string{}
	for _, item := range items {
		byKind[item.Kind] = item.Status
	}
	// The on-demand tool that was running and is now down surfaces as idle — a
	// distinct, honest state, not the stale "active" the proxy used to trust nor the
	// "stopped" that reads as uninstalled.
	if byKind[admintooloperator.PGAdmin] != string(StatusIdle) {
		t.Fatalf("pgAdmin status = %q, want idle", byKind[admintooloperator.PGAdmin])
	}
	// A non-on-demand tool going inactive is not idle — it is genuinely stopped.
	if byKind[admintooloperator.PHPMyAdmin] != string(StatusStopped) {
		t.Fatalf("phpMyAdmin status = %q, want stopped", byKind[admintooloperator.PHPMyAdmin])
	}
	// A subsequent uninstall (recorded as stopped) must not be re-surfaced as idle:
	// once the database records the stop intent, an inactive unit stays stopped.
	if _, err := database.NewUpdate().Model((*toolModel)(nil)).Set("status = ?", StatusStopped).Where("kind = ?", admintooloperator.PGAdmin).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	items, err = module.Sync(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.Kind == admintooloperator.PGAdmin && item.Status != string(StatusStopped) {
			t.Fatalf("uninstalled pgAdmin status = %q, want stopped", item.Status)
		}
	}
}

func TestDescriptorDoesNotRequirePodmanForNativePHPMyAdmin(t *testing.T) {
	descriptor := (&Module{}).Descriptor()
	for _, capability := range descriptor.RequiredCapabilities {
		if capability == "podman" || capability == "quadlet" {
			t.Fatalf("module-wide capability %q would disable native phpMyAdmin", capability)
		}
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
