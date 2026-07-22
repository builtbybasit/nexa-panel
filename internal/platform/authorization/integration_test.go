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
	"github.com/nexa-panel/nexa-panel/internal/platform/controlplane"
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
	server, err := controlplane.New("test", []module.Module{auditLog, identityModule, sensitiveActionModule{}}, logger,
		controlplane.WithAuthentication(identityModule), controlplane.WithAuthorization(authorization.New()))
	if err != nil {
		t.Fatalf("create control plane: %v", err)
	}

	bootstrap := httptest.NewRequest(http.MethodPost, "/api/v1/auth/bootstrap",
		strings.NewReader(`{"username":"admin","password":"a-strong-password"}`))
	bootstrap.RemoteAddr = "127.0.0.1:12345"
	bootstrapResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(bootstrapResponse, bootstrap)
	if bootstrapResponse.Code != http.StatusCreated {
		t.Fatalf("bootstrap = %d %s", bootstrapResponse.Code, bootstrapResponse.Body.String())
	}
	cookie := bootstrapResponse.Result().Cookies()[0]
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
	fresh := requestSensitiveAction(server.Handler(), cookie)
	if fresh.Code != http.StatusNoContent {
		t.Fatalf("fresh sensitive action = %d %s", fresh.Code, fresh.Body.String())
	}
	if _, err := database.ExecContext(ctx, "UPDATE identity_sessions SET mfa_verified_at = ? WHERE token_hash = ?", now.Add(-20*time.Minute), tokenHash[:]); err != nil {
		t.Fatalf("age MFA verification: %v", err)
	}
	stale := requestSensitiveAction(server.Handler(), cookie)
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

type sensitiveActionModule struct{}

func (sensitiveActionModule) Descriptor() module.Descriptor {
	return module.Descriptor{ID: "sensitive-test", Name: "Sensitive test", Version: "test", Dependencies: []string{"identity"}}
}

func (sensitiveActionModule) Register(registry module.Registry) error {
	return registry.HandleAuthorized("POST /api/v1/sensitive-test", "operations.apply", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
}

func requestSensitiveAction(handler http.Handler, cookie *http.Cookie) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/api/v1/sensitive-test", nil)
	request.RemoteAddr = "127.0.0.1:12345"
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
