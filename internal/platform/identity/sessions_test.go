package identity

import (
	"bytes"
	"context"
	"crypto/tls"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/platform/audit"
	"github.com/nexa-panel/nexa-panel/internal/platform/httpapi"
	"github.com/nexa-panel/nexa-panel/internal/platform/persistence"
	"github.com/nexa-panel/nexa-panel/internal/platform/secrets"
)

func TestStartSessionCookieSecureFollowsTransport(t *testing.T) {
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
	secretBox, err := secrets.New(bytes.Repeat([]byte{9}, 32))
	if err != nil {
		t.Fatalf("create secret box: %v", err)
	}
	module, err := NewWithConfig(ctx, database, auditModule, secretBox, logger, Config{
		SessionTTL: time.Hour, PasswordMemoryKiB: 64, PasswordIterations: 1, PasswordThreads: 1,
	})
	if err != nil {
		t.Fatalf("create identity module: %v", err)
	}

	user := User{ID: randomID(16), Username: "admin", Role: "admin"}
	if _, err := database.NewInsert().Model(&userModel{
		ID: user.ID, Username: user.Username, PasswordHash: "x", CreatedAt: module.now().UTC(), Role: user.Role,
	}).Exec(ctx); err != nil {
		t.Fatalf("insert user: %v", err)
	}

	secureCookie := func(t *testing.T, r *http.Request) bool {
		t.Helper()
		recorder := httptest.NewRecorder()
		if err := module.startSession(recorder, r, user); err != nil {
			t.Fatalf("startSession: %v", err)
		}
		for _, cookie := range recorder.Result().Cookies() {
			if cookie.Name == cookieName {
				return cookie.Secure
			}
		}
		t.Fatal("session cookie was not set")
		return false
	}

	// Plain HTTP over loopback (the documented bootstrap workflow): the cookie is
	// not marked Secure so the browser will actually send it back.
	plain := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8888/api/v1/auth/login", nil)
	plain.RemoteAddr = "127.0.0.1:5000"
	if secureCookie(t, plain) {
		t.Fatal("plain HTTP session cookie was marked Secure")
	}

	// TLS fronted by the trusted proxy: nginx terminates TLS and forwards
	// X-Forwarded-Proto https, so the cookie must be Secure.
	forwarded := httptest.NewRequest(http.MethodPost, "http://panel.example/api/v1/auth/login", nil)
	forwarded.Header.Set("X-Forwarded-Proto", "https")
	forwarded = forwarded.WithContext(httpapi.WithTrustedProxy(forwarded.Context()))
	if !secureCookie(t, forwarded) {
		t.Fatal("TLS-fronted session cookie was not marked Secure")
	}

	// Direct TLS connection: the cookie must be Secure.
	directTLS := httptest.NewRequest(http.MethodPost, "https://panel.example/api/v1/auth/login", nil)
	directTLS.TLS = &tls.ConnectionState{}
	if !secureCookie(t, directTLS) {
		t.Fatal("direct TLS session cookie was not marked Secure")
	}
}
