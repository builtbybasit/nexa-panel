package authorization_test

import (
	"bytes"
	"context"
	"crypto/sha256"
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
	"github.com/nexa-panel/nexa-panel/internal/platform/controlpanel"
	"github.com/nexa-panel/nexa-panel/internal/platform/identity"
	"github.com/nexa-panel/nexa-panel/internal/platform/module"
	"github.com/nexa-panel/nexa-panel/internal/platform/persistence"
	"github.com/nexa-panel/nexa-panel/internal/platform/secrets"
)

func TestViewerCannotReadAuditButAdminCan(t *testing.T) {
	ctx := context.Background()
	database, err := persistence.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := persistence.RunMigrations(context.Background(), database); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	auditLog, err := audit.New(ctx, database)
	if err != nil {
		t.Fatalf("create audit module: %v", err)
	}
	box, _ := secrets.New(bytes.Repeat([]byte{4}, 32))
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	identityModule, err := identity.NewWithConfig(ctx, database, auditLog, box, logger, identity.Config{
		SessionTTL: time.Hour, PasswordMemoryKiB: 64, PasswordIterations: 1, PasswordThreads: 1,
	})
	if err != nil {
		t.Fatalf("create identity module: %v", err)
	}
	server, err := controlpanel.New("test", []module.Module{auditLog, identityModule, sensitiveActionModule{}}, logger,
		controlpanel.WithAuthentication(identityModule), controlpanel.WithAuthorization(authorization.New()))
	if err != nil {
		t.Fatalf("create control panel: %v", err)
	}

	bootstrap := httptest.NewRequest(http.MethodPost, "/api/v1/auth/bootstrap",
		strings.NewReader(`{"username":"admin","password":"Str0ng-Console-Pass"}`))
	bootstrap.RemoteAddr = "127.0.0.1:12345"
	bootstrapResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(bootstrapResponse, bootstrap)
	if bootstrapResponse.Code != http.StatusCreated {
		t.Fatalf("bootstrap = %d %s", bootstrapResponse.Code, bootstrapResponse.Body.String())
	}
	cookie, csrfToken := sessionCredentials(t, bootstrapResponse.Result().Cookies())
	tokenHash := sha256.Sum256([]byte(cookie.Value))
	now := time.Now().UTC()
	if _, err := database.ExecContext(ctx, "UPDATE identity_users SET totp_confirmed_at = ?", now); err != nil {
		t.Fatalf("mark MFA enrolled: %v", err)
	}
	if _, err := database.ExecContext(ctx, "UPDATE identity_sessions SET mfa_verified_at = ? WHERE token_hash = ?", now, tokenHash[:]); err != nil {
		t.Fatalf("mark MFA verified: %v", err)
	}

	if status := requestAudit(server.Handler(), cookie); status != http.StatusOK {
		t.Fatalf("admin audit status = %d", status)
	}
	fresh := requestSensitiveAction(server.Handler(), "/api/v1/sensitive-test", cookie, csrfToken)
	if fresh.Code != http.StatusNoContent {
		t.Fatalf("fresh sensitive action = %d %s", fresh.Code, fresh.Body.String())
	}
	if _, err := database.ExecContext(ctx, "UPDATE identity_sessions SET mfa_verified_at = ? WHERE token_hash = ?", now.Add(-20*time.Minute), tokenHash[:]); err != nil {
		t.Fatalf("age MFA verification: %v", err)
	}
	stale := requestSensitiveAction(server.Handler(), "/api/v1/sensitive-test", cookie, csrfToken)
	if stale.Code != http.StatusForbidden || !strings.Contains(stale.Body.String(), `"mfa_step_up_required"`) {
		t.Fatalf("stale sensitive action = %d %s", stale.Code, stale.Body.String())
	}
	if _, err := database.ExecContext(ctx, "UPDATE identity_users SET role = 'viewer'"); err != nil {
		t.Fatalf("change role: %v", err)
	}
	if status := requestAudit(server.Handler(), cookie); status != http.StatusForbidden {
		t.Fatalf("viewer audit status = %d", status)
	}
}

// TestUnenrolledAccountIsNotAskedToStepUp pins the other half of the optional-MFA
// policy: an account that never opted into a second factor has nothing to step up
// with, so a sensitive action must not be blocked behind a challenge it can never
// answer. Enrolled accounts stay covered by the stale-step-up case above.
func TestUnenrolledAccountIsNotAskedToStepUp(t *testing.T) {
	ctx := context.Background()
	database, err := persistence.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := persistence.RunMigrations(ctx, database); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	auditLog, err := audit.New(ctx, database)
	if err != nil {
		t.Fatalf("create audit module: %v", err)
	}
	box, _ := secrets.New(bytes.Repeat([]byte{6}, 32))
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	identityModule, err := identity.NewWithConfig(ctx, database, auditLog, box, logger, identity.Config{
		SessionTTL: time.Hour, PasswordMemoryKiB: 64, PasswordIterations: 1, PasswordThreads: 1,
	})
	if err != nil {
		t.Fatalf("create identity module: %v", err)
	}
	server, err := controlpanel.New("test", []module.Module{auditLog, identityModule, sensitiveActionModule{}}, logger,
		controlpanel.WithAuthentication(identityModule), controlpanel.WithAuthorization(authorization.New()))
	if err != nil {
		t.Fatalf("create control panel: %v", err)
	}

	bootstrap := httptest.NewRequest(http.MethodPost, "/api/v1/auth/bootstrap",
		strings.NewReader(`{"username":"admin","password":"Str0ng-Console-Pass"}`))
	bootstrap.RemoteAddr = "127.0.0.1:12345"
	bootstrapResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(bootstrapResponse, bootstrap)
	if bootstrapResponse.Code != http.StatusCreated {
		t.Fatalf("bootstrap = %d %s", bootstrapResponse.Code, bootstrapResponse.Body.String())
	}
	cookie, csrfToken := sessionCredentials(t, bootstrapResponse.Result().Cookies())

	// No confirmed factor anywhere: the password-only administrator applies a
	// sensitive operation without a step-up demand.
	admin := requestSensitiveAction(server.Handler(), "/api/v1/sensitive-test", cookie, csrfToken)
	if admin.Code != http.StatusNoContent {
		t.Fatalf("unenrolled administrator sensitive action = %d %s", admin.Code, admin.Body.String())
	}
	if _, err := database.ExecContext(ctx, "UPDATE identity_users SET role = 'operator'"); err != nil {
		t.Fatalf("change role: %v", err)
	}
	operator := requestSensitiveAction(server.Handler(), "/api/v1/sensitive-operator-test", cookie, csrfToken)
	if operator.Code != http.StatusNoContent {
		t.Fatalf("unenrolled operator sensitive action = %d %s", operator.Code, operator.Body.String())
	}
}

type sensitiveActionModule struct{}

func (sensitiveActionModule) Descriptor() module.Descriptor {
	return module.Descriptor{ID: "sensitive-test", Name: "Sensitive test", Version: "test", Dependencies: []string{"identity"}}
}

func (sensitiveActionModule) Register(registry module.Registry) error {
	noContent := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	// Two sensitive permissions, because the roles that hold them differ: only an
	// admin may apply operations, while an operator may write firewall rules.
	if err := registry.HandleAuthorized("POST /api/v1/sensitive-test", "operations.apply", noContent); err != nil {
		return err
	}
	return registry.HandleAuthorized("POST /api/v1/sensitive-operator-test", "firewall.write", noContent)
}

// sessionCredentials splits the pair of cookies a sign-in issues: the session
// cookie is presented as-is, while the CSRF cookie's value has to be echoed
// back in a header for the double submit to pass.
func sessionCredentials(t *testing.T, cookies []*http.Cookie) (*http.Cookie, string) {
	t.Helper()
	var session *http.Cookie
	csrfToken := ""
	for _, cookie := range cookies {
		switch cookie.Name {
		case "nexa_session":
			session = cookie
		case "nexa_csrf":
			csrfToken = cookie.Value
		}
	}
	if session == nil || csrfToken == "" {
		t.Fatalf("bootstrap did not issue both session and csrf cookies: %v", cookies)
	}
	return session, csrfToken
}

func requestSensitiveAction(handler http.Handler, path string, cookie *http.Cookie, csrfToken string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, path, nil)
	request.RemoteAddr = "127.0.0.1:12345"
	request.Header.Set("Origin", "http://"+request.Host)
	request.Header.Set("X-CSRF-Token", csrfToken)
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func requestAudit(handler http.Handler, cookie *http.Cookie) int {
	request := httptest.NewRequest(http.MethodGet, "/api/v1/audit/events", nil)
	request.RemoteAddr = "127.0.0.1:12345"
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response.Code
}
