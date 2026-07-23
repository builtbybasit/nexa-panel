package identity

import (
	"bytes"
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
	"github.com/nexa-panel/nexa-panel/internal/platform/controlplane"
	"github.com/nexa-panel/nexa-panel/internal/platform/module"
	"github.com/nexa-panel/nexa-panel/internal/platform/persistence"
	"github.com/nexa-panel/nexa-panel/internal/platform/secrets"
)

// csrfHarness is a bootstrapped panel behind the real control-plane handler, so
// the tests below exercise the middleware exactly as a browser would reach it.
type csrfHarness struct {
	module  *Module
	handler http.Handler
	cookies []*http.Cookie
}

func newCSRFHarness(t *testing.T, config Config) *csrfHarness {
	t.Helper()
	ctx := context.Background()
	database, err := persistence.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := persistence.RunMigrations(ctx, database); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	auditModule, err := audit.New(ctx, database)
	if err != nil {
		t.Fatalf("create audit module: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	secretBox, err := secrets.New(bytes.Repeat([]byte{3}, 32))
	if err != nil {
		t.Fatalf("create secret box: %v", err)
	}
	identityModule, err := NewWithConfig(ctx, database, auditModule, secretBox, logger, config)
	if err != nil {
		t.Fatalf("create identity module: %v", err)
	}
	server, err := controlplane.New("test", []module.Module{identityModule, auditModule}, logger,
		controlplane.WithAuthentication(identityModule), controlplane.WithAuthorization(testAuthorization{}))
	if err != nil {
		t.Fatalf("create control plane: %v", err)
	}
	harness := &csrfHarness{module: identityModule, handler: server.Handler()}
	bootstrap := performRequest(harness.handler, http.MethodPost, "/api/v1/auth/bootstrap",
		`{"username":"admin","password":"a-Str0ng-password"}`, nil)
	if bootstrap.Code != http.StatusCreated {
		t.Fatalf("bootstrap = %d %s", bootstrap.Code, bootstrap.Body.String())
	}
	// These tests isolate CSRF behavior, not administrator enrollment. Exercise
	// the same session as an operator so the mandatory admin MFA gate cannot mask
	// the transport assertion under test.
	if _, err := database.ExecContext(ctx, "UPDATE identity_users SET role = 'operator'"); err != nil {
		t.Fatalf("set CSRF fixture role: %v", err)
	}
	harness.cookies = bootstrap.Result().Cookies()
	return harness
}

func (h *csrfHarness) sessionCookie(t *testing.T) *http.Cookie {
	t.Helper()
	for _, cookie := range h.cookies {
		if cookie.Name == cookieName {
			return cookie
		}
	}
	t.Fatal("session cookie was not issued")
	return nil
}

func (h *csrfHarness) csrfToken(t *testing.T) string {
	t.Helper()
	for _, cookie := range h.cookies {
		if cookie.Name == csrfCookieName {
			return cookie.Value
		}
	}
	t.Fatal("csrf cookie was not issued")
	return ""
}

// changePassword posts to a route behind the authenticating middleware. The body
// is intentionally incomplete: a request that clears CSRF fails later on
// validation (400), which distinguishes "got past the guard" from "was blocked".
func (h *csrfHarness) changePassword(t *testing.T, token string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/password", strings.NewReader(`{}`))
	request.RemoteAddr = "127.0.0.1:12345"
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	for _, cookie := range cookies {
		request.AddCookie(cookie)
	}
	if token != "" {
		request.Header.Set(csrfHeaderName, token)
	}
	response := httptest.NewRecorder()
	h.handler.ServeHTTP(response, request)
	return response
}

func testConfig() Config {
	return Config{SessionTTL: time.Hour, PasswordMemoryKiB: 64, PasswordIterations: 1, PasswordThreads: 1}
}

func TestUnsafeRequestRequiresTheSessionsOwnCSRFToken(t *testing.T) {
	harness := newCSRFHarness(t, testConfig())
	cookie := harness.sessionCookie(t)

	accepted := harness.changePassword(t, harness.csrfToken(t), harness.cookies...)
	if accepted.Code != http.StatusBadRequest {
		t.Fatalf("matching token = %d %s, want the request to reach the handler", accepted.Code, accepted.Body.String())
	}

	missing := harness.changePassword(t, "", cookie)
	if missing.Code != http.StatusForbidden || !strings.Contains(missing.Body.String(), `"invalid_csrf_token"`) {
		t.Fatalf("missing token = %d %s, want 403 invalid_csrf_token", missing.Code, missing.Body.String())
	}

	wrong := harness.changePassword(t, strings.Repeat("a", 64), cookie)
	if wrong.Code != http.StatusForbidden || !strings.Contains(wrong.Body.String(), `"invalid_csrf_token"`) {
		t.Fatalf("wrong token = %d %s, want 403 invalid_csrf_token", wrong.Code, wrong.Body.String())
	}

	// A token minted for a different session must not be replayable against this
	// one: that is what binding the hash to the session row buys.
	other := newCSRFHarness(t, testConfig())
	replayed := harness.changePassword(t, other.csrfToken(t), cookie)
	if replayed.Code != http.StatusForbidden || !strings.Contains(replayed.Body.String(), `"invalid_csrf_token"`) {
		t.Fatalf("replayed token = %d %s, want 403 invalid_csrf_token", replayed.Code, replayed.Body.String())
	}

	// Reads are unaffected; requiring the token there would only widen its exposure.
	read := performRequest(harness.handler, http.MethodGet, "/api/v1/auth/session", "", cookie)
	if read.Code != http.StatusOK {
		t.Fatalf("read without a token = %d %s", read.Code, read.Body.String())
	}
}

func TestAdminToolProxyUsesItsOwnCSRFTokenButStillRequiresSameOrigin(t *testing.T) {
	harness := newCSRFHarness(t, testConfig())
	request := httptest.NewRequest(http.MethodPost, "/tools/pgadmin/browser/preferences", strings.NewReader(`{}`))
	request.RemoteAddr = "127.0.0.1:12345"
	request.Header.Set("Origin", "http://"+request.Host)
	for _, cookie := range harness.cookies {
		request.AddCookie(cookie)
	}
	response := httptest.NewRecorder()
	harness.module.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})).ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("same-origin pgAdmin POST = %d %s, want the request to reach pgAdmin's own CSRF guard", response.Code, response.Body.String())
	}

	crossOrigin := request.Clone(request.Context())
	crossOrigin.Header = request.Header.Clone()
	crossOrigin.Header.Set("Origin", "https://attacker.example")
	crossOriginResponse := httptest.NewRecorder()
	harness.module.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})).ServeHTTP(crossOriginResponse, crossOrigin)
	if crossOriginResponse.Code != http.StatusForbidden || !strings.Contains(crossOriginResponse.Body.String(), `"invalid_origin"`) {
		t.Fatalf("cross-origin pgAdmin POST = %d %s", crossOriginResponse.Code, crossOriginResponse.Body.String())
	}
}

func TestClearSessionCookieClearsTheCSRFCookieOnlyWhenPresented(t *testing.T) {
	harness := newCSRFHarness(t, testConfig())
	deleted := func(response *httptest.ResponseRecorder) map[string]bool {
		names := map[string]bool{}
		for _, cookie := range response.Result().Cookies() {
			if cookie.MaxAge < 0 {
				names[cookie.Name] = true
			}
		}
		return names
	}

	// An expired session presenting both cookies has both cleared.
	if _, err := harness.module.database.ExecContext(context.Background(),
		"UPDATE identity_sessions SET expires_at = ?", harness.module.now().UTC().Add(-time.Hour)); err != nil {
		t.Fatalf("expire session: %v", err)
	}
	both := performRequest(harness.handler, http.MethodGet, "/api/v1/auth/session", "", harness.cookies...)
	if names := deleted(both); !names[cookieName] || !names[csrfCookieName] {
		t.Fatalf("cleared cookies = %v, want both", names)
	}

	// A request that withheld the CSRF cookie must not have it deleted: the
	// browser may still hold a valid one.
	sessionOnly := performRequest(harness.handler, http.MethodGet, "/api/v1/auth/session", "", harness.sessionCookie(t))
	if names := deleted(sessionOnly); !names[cookieName] || names[csrfCookieName] {
		t.Fatalf("cleared cookies = %v, want the session cookie only", names)
	}
}

func TestValidRequestOriginFailsClosed(t *testing.T) {
	cases := []struct {
		name    string
		method  string
		headers map[string]string
		allowed bool
	}{
		{name: "reads need no proof", method: http.MethodGet, allowed: true},
		{name: "bare write proves nothing", method: http.MethodPost},
		{name: "same origin header", method: http.MethodPost, headers: map[string]string{"Origin": "https://panel.example"}, allowed: true},
		{name: "cross origin header", method: http.MethodPost, headers: map[string]string{"Origin": "https://evil.example"}},
		{name: "opaque origin", method: http.MethodPost, headers: map[string]string{"Origin": "null"}},
		{name: "fetch metadata same-origin", method: http.MethodPost, headers: map[string]string{"Sec-Fetch-Site": "same-origin"}, allowed: true},
		{name: "fetch metadata none", method: http.MethodPost, headers: map[string]string{"Sec-Fetch-Site": "none"}, allowed: true},
		{name: "fetch metadata cross-site", method: http.MethodPost, headers: map[string]string{"Sec-Fetch-Site": "cross-site"}},
		{name: "fetch metadata same-site", method: http.MethodPost, headers: map[string]string{"Sec-Fetch-Site": "same-site"}},
		// A cross-site Origin is authoritative even when the metadata disagrees.
		{name: "origin wins over metadata", method: http.MethodPost, headers: map[string]string{"Origin": "https://evil.example", "Sec-Fetch-Site": "same-origin"}},
		{name: "referer fallback same host", method: http.MethodPost, headers: map[string]string{"Referer": "https://panel.example/sites"}, allowed: true},
		{name: "referer fallback other host", method: http.MethodPost, headers: map[string]string{"Referer": "https://evil.example/sites"}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			request := httptest.NewRequest(testCase.method, "https://panel.example/api/v1/sites", nil)
			for name, value := range testCase.headers {
				request.Header.Set(name, value)
			}
			if allowed := validRequestOrigin(request); allowed != testCase.allowed {
				t.Fatalf("validRequestOrigin = %v, want %v", allowed, testCase.allowed)
			}
		})
	}
}

func TestSessionWithoutCSRFBindingIsEnded(t *testing.T) {
	harness := newCSRFHarness(t, testConfig())
	ctx := context.Background()
	// Sessions minted before the CSRF column existed carry no hash and can never
	// satisfy an unsafe request, so they are dropped rather than left half-usable.
	if _, err := harness.module.database.ExecContext(ctx, "UPDATE identity_sessions SET csrf_token_hash = NULL"); err != nil {
		t.Fatalf("clear csrf binding: %v", err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/auth/session", nil)
	request.AddCookie(harness.sessionCookie(t))
	if _, err := harness.module.authenticate(ctx, request); err == nil {
		t.Fatal("authenticate accepted a session with no CSRF binding")
	}
	count, err := harness.module.database.NewSelect().Model((*sessionModel)(nil)).Count(ctx)
	if err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if count != 0 {
		t.Fatalf("session count = %d, want the unbound session deleted", count)
	}
}

func TestSessionIdleTimeoutEndsAnUntouchedSession(t *testing.T) {
	config := testConfig()
	config.SessionTTL = 24 * time.Hour
	config.IdleTimeout = 30 * time.Minute
	harness := newCSRFHarness(t, config)
	ctx := context.Background()
	clock := harness.module.now().UTC()
	harness.module.now = func() time.Time { return clock }

	request := httptest.NewRequest(http.MethodGet, "/api/v1/auth/session", nil)
	request.AddCookie(harness.sessionCookie(t))

	// Inside the window the session survives, and the read slides last_seen_at
	// forward so an actively used session never trips the timeout.
	clock = clock.Add(29 * time.Minute)
	if _, err := harness.module.authenticate(ctx, request); err != nil {
		t.Fatalf("authenticate inside the idle window: %v", err)
	}
	clock = clock.Add(29 * time.Minute)
	if _, err := harness.module.authenticate(ctx, request); err != nil {
		t.Fatalf("authenticate after the window slid forward: %v", err)
	}

	// Left untouched past the timeout it is ended, well before SessionTTL's cap.
	clock = clock.Add(31 * time.Minute)
	if _, err := harness.module.authenticate(ctx, request); err == nil {
		t.Fatal("authenticate accepted a session idle past the timeout")
	}
	count, err := harness.module.database.NewSelect().Model((*sessionModel)(nil)).Count(ctx)
	if err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if count != 0 {
		t.Fatalf("session count = %d, want the idle session deleted", count)
	}
}

func TestIdleTimeoutMustBePositive(t *testing.T) {
	ctx := context.Background()
	database, err := persistence.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := persistence.RunMigrations(ctx, database); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	auditModule, err := audit.New(ctx, database)
	if err != nil {
		t.Fatalf("create audit module: %v", err)
	}
	secretBox, err := secrets.New(bytes.Repeat([]byte{3}, 32))
	if err != nil {
		t.Fatalf("create secret box: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	// An unset idle timeout takes the default rather than failing validation, so
	// callers that predate the field keep working.
	defaulted := testConfig()
	created, err := NewWithConfig(ctx, database, auditModule, secretBox, logger, defaulted)
	if err != nil {
		t.Fatalf("create identity module: %v", err)
	}
	if created.config.IdleTimeout != DefaultConfig().IdleTimeout {
		t.Fatalf("idle timeout = %s, want the default %s", created.config.IdleTimeout, DefaultConfig().IdleTimeout)
	}
	negative := testConfig()
	negative.IdleTimeout = -time.Minute
	if _, err := NewWithConfig(ctx, database, auditModule, secretBox, logger, negative); err == nil {
		t.Fatal("NewWithConfig accepted a negative idle timeout")
	}
}

func TestPasswordPolicyIsPublishedOnStatus(t *testing.T) {
	harness := newCSRFHarness(t, testConfig())
	status := performRequest(harness.handler, http.MethodGet, "/api/v1/auth/status", "", nil)
	if status.Code != http.StatusOK {
		t.Fatalf("status = %d %s", status.Code, status.Body.String())
	}
	body := status.Body.String()
	for _, fragment := range []string{`"minLength":12`, `"requiredClasses":3`, `"classExemptLength":20`, `"denylistApplied":true`} {
		if !strings.Contains(body, fragment) {
			t.Fatalf("status body = %s, want it to contain %s", body, fragment)
		}
	}
	// The constraints are published; the denylist itself never is.
	if strings.Contains(body, "password123") {
		t.Fatalf("status body leaked denylist contents: %s", body)
	}
}
