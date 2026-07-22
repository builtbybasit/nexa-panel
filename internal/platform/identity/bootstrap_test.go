package identity

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
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

// hardenTestEnv wires a fresh identity module and control plane for the
// security-hardening tests, returning the module, its HTTP handler, and a
// pointer to the mutable clock the module reads.
func hardenTestEnv(t *testing.T, config Config) (*Module, http.Handler, audit.Recorder, *time.Time) {
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
	auditModule, err := audit.New(ctx, database)
	if err != nil {
		t.Fatalf("create audit module: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	secretBox, err := secrets.New(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatalf("create secret box: %v", err)
	}
	identityModule, err := NewWithConfig(ctx, database, auditModule, secretBox, logger, config)
	if err != nil {
		t.Fatalf("create identity module: %v", err)
	}
	clock := time.Unix(1_700_000_000, 0).UTC()
	identityModule.now = func() time.Time { return clock }
	server, err := controlplane.New("test", []module.Module{identityModule, auditModule}, logger,
		controlplane.WithAuthentication(identityModule), controlplane.WithAuthorization(testAuthorization{}))
	if err != nil {
		t.Fatalf("create control plane: %v", err)
	}
	return identityModule, server.Handler(), auditModule, &clock
}

// remoteRequest simulates a genuinely remote client: a public peer address over
// TLS (so the control plane's secure-transport guard admits it), which is the
// exact shape the bootstrap land-grab defence must withstand.
func remoteRequest(handler http.Handler, method, path, body, bootstrapToken string, cookie *http.Cookie) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.RemoteAddr = "203.0.113.10:44321"
	request.TLS = &tls.ConnectionState{}
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if bootstrapToken != "" {
		request.Header.Set(bootstrapTokenHeader, bootstrapToken)
	}
	if cookie != nil {
		request.AddCookie(cookie)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func TestBootstrapRemoteRequiresToken(t *testing.T) {
	identityModule, handler, _, _ := hardenTestEnv(t, Config{
		SessionTTL: time.Hour, PasswordMemoryKiB: 64, PasswordIterations: 1, PasswordThreads: 1,
	})
	tokenPath := filepath.Join(t.TempDir(), "state", "bootstrap.token")
	identityModule.SetBootstrapTokenPath(tokenPath)
	if err := identityModule.EnsureBootstrapToken(context.Background()); err != nil {
		t.Fatalf("ensure bootstrap token: %v", err)
	}
	info, err := os.Stat(tokenPath)
	if err != nil {
		t.Fatalf("stat token file: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("token file permissions = %o, want 600", perm)
	}
	tokenBytes, err := os.ReadFile(tokenPath)
	if err != nil {
		t.Fatalf("read token file: %v", err)
	}
	token := strings.TrimSpace(string(tokenBytes))
	if token == "" {
		t.Fatal("bootstrap token file is empty")
	}

	// A remote client is told it must present the token.
	status := remoteRequest(handler, http.MethodGet, "/api/v1/auth/status", "", "", nil)
	if !strings.Contains(status.Body.String(), `"bootstrapTokenRequired":true`) {
		t.Fatalf("remote status = %s", status.Body.String())
	}

	// A remote client with no token cannot claim the admin account.
	noToken := remoteRequest(handler, http.MethodPost, "/api/v1/auth/bootstrap",
		`{"username":"admin","password":"a-strong-password"}`, "", nil)
	if noToken.Code != http.StatusForbidden || !strings.Contains(noToken.Body.String(), `"bootstrap_forbidden"`) {
		t.Fatalf("remote bootstrap without token = %d %s", noToken.Code, noToken.Body.String())
	}

	// A wrong token is likewise rejected.
	wrongToken := remoteRequest(handler, http.MethodPost, "/api/v1/auth/bootstrap",
		`{"username":"admin","password":"a-strong-password"}`, "deadbeef", nil)
	if wrongToken.Code != http.StatusForbidden {
		t.Fatalf("remote bootstrap with wrong token = %d %s", wrongToken.Code, wrongToken.Body.String())
	}

	// The account is still unclaimed after the rejected attempts.
	if required, err := identityModule.bootstrapRequired(context.Background()); err != nil || !required {
		t.Fatalf("bootstrapRequired after rejected attempts = %v, %v", required, err)
	}

	// The correct token lets the operator claim admin exactly once.
	ok := remoteRequest(handler, http.MethodPost, "/api/v1/auth/bootstrap",
		`{"username":"admin","password":"a-strong-password"}`, token, nil)
	if ok.Code != http.StatusCreated {
		t.Fatalf("remote bootstrap with token = %d %s", ok.Code, ok.Body.String())
	}

	// The token file is removed so the secret can never be replayed.
	if _, err := os.Stat(tokenPath); !os.IsNotExist(err) {
		t.Fatalf("token file still present after bootstrap: %v", err)
	}

	// A remote caller replaying the now-stale token is rejected before it can even
	// reach the account check: the in-memory secret was wiped on success.
	stale := remoteRequest(handler, http.MethodPost, "/api/v1/auth/bootstrap",
		`{"username":"other","password":"another-password"}`, token, nil)
	if stale.Code != http.StatusForbidden {
		t.Fatalf("remote bootstrap with stale token = %d %s", stale.Code, stale.Body.String())
	}

	// A loopback caller passes the local-access gate but still hits the closed
	// window: the existing already_bootstrapped conflict is preserved.
	closed := performRequest(handler, http.MethodPost, "/api/v1/auth/bootstrap",
		`{"username":"other","password":"another-password"}`, nil)
	if closed.Code != http.StatusConflict || !strings.Contains(closed.Body.String(), `"already_bootstrapped"`) {
		t.Fatalf("loopback bootstrap after admin exists = %d %s", closed.Code, closed.Body.String())
	}
}

func TestBootstrapLoopbackAllowedWithoutToken(t *testing.T) {
	identityModule, handler, _, _ := hardenTestEnv(t, Config{
		SessionTTL: time.Hour, PasswordMemoryKiB: 64, PasswordIterations: 1, PasswordThreads: 1,
	})
	// A token is provisioned, but a loopback operator proves local access without
	// it: performRequest presents a 127.0.0.1 peer.
	identityModule.SetBootstrapTokenPath(filepath.Join(t.TempDir(), "bootstrap.token"))
	if err := identityModule.EnsureBootstrapToken(context.Background()); err != nil {
		t.Fatalf("ensure bootstrap token: %v", err)
	}
	loopback := performRequest(handler, http.MethodPost, "/api/v1/auth/bootstrap",
		`{"username":"admin","password":"a-strong-password"}`, nil)
	if loopback.Code != http.StatusCreated {
		t.Fatalf("loopback bootstrap without token = %d %s", loopback.Code, loopback.Body.String())
	}
}

// invalidTOTP returns a six-digit code that is guaranteed not to validate for
// the given secret around now, so a verification attempt always fails.
func invalidTOTP(secret []byte, now time.Time) string {
	step := now.Unix() / totpPeriodSeconds
	forbidden := map[string]struct{}{}
	for _, offset := range []int64{-2, -1, 0, 1, 2} {
		forbidden[generateTOTP(secret, step+offset, totpDigits)] = struct{}{}
	}
	for candidate := 0; ; candidate++ {
		code := leftPadDigits(candidate)
		if _, taken := forbidden[code]; !taken {
			return code
		}
	}
}

func leftPadDigits(value int) string {
	digits := []byte("000000")
	for i := len(digits) - 1; i >= 0 && value > 0; i-- {
		digits[i] = byte('0' + value%10)
		value /= 10
	}
	return string(digits)
}

func TestMFAVerifyLockoutIsUserKeyedAndPersistsAcrossSessions(t *testing.T) {
	_, handler, _, clock := hardenTestEnv(t, Config{
		SessionTTL: time.Hour, PasswordMemoryKiB: 64, PasswordIterations: 1, PasswordThreads: 1,
		AttemptLimit: 5, AttemptWindow: 5 * time.Minute,
		MFALockoutLimit: 8, MFALockoutWindow: 15 * time.Minute,
	})

	// Bootstrap (loopback) and enroll MFA to reach an enrolled admin.
	if resp := performRequest(handler, http.MethodPost, "/api/v1/auth/bootstrap",
		`{"username":"admin","password":"a-strong-password"}`, nil); resp.Code != http.StatusCreated {
		t.Fatalf("bootstrap = %d %s", resp.Code, resp.Body.String())
	}
	loginA := performRequest(handler, http.MethodPost, "/api/v1/auth/login",
		`{"username":"admin","password":"a-strong-password"}`, nil)
	sessionCookie := loginA.Result().Cookies()[0]
	enroll := performRequest(handler, http.MethodPost, "/api/v1/auth/mfa/enroll", "", sessionCookie)
	var enrollBody struct {
		Secret string `json:"secret"`
	}
	if err := json.Unmarshal(enroll.Body.Bytes(), &enrollBody); err != nil {
		t.Fatalf("decode enrollment: %v", err)
	}
	decodedSecret, err := base32Encoding.DecodeString(enrollBody.Secret)
	if err != nil {
		t.Fatalf("decode secret: %v", err)
	}
	confirmCode := generateTOTP(decodedSecret, clock.Unix()/totpPeriodSeconds, totpDigits)
	if resp := performRequest(handler, http.MethodPost, "/api/v1/auth/mfa/confirm",
		`{"code":"`+confirmCode+`"}`, sessionCookie); resp.Code != http.StatusOK {
		t.Fatalf("confirm = %d %s", resp.Code, resp.Body.String())
	}
	performRequest(handler, http.MethodPost, "/api/v1/auth/logout", "", sessionCookie)

	badCode := invalidTOTP(decodedSecret, *clock)
	body := `{"code":"` + badCode + `"}`

	// Fresh session; exhaust the burst limit (5) with wrong codes.
	firstLogin := performRequest(handler, http.MethodPost, "/api/v1/auth/login",
		`{"username":"admin","password":"a-strong-password"}`, nil)
	firstCookie := firstLogin.Result().Cookies()[0]
	for i := 0; i < 5; i++ {
		resp := performRequest(handler, http.MethodPost, "/api/v1/auth/mfa/verify", body, firstCookie)
		if resp.Code != http.StatusUnauthorized {
			t.Fatalf("verify attempt %d = %d %s, want 401", i, resp.Code, resp.Body.String())
		}
	}

	// A brand-new session must NOT reset the counter: this is the core fix. Before
	// it, re-logging-in minted a fresh session id and reset the 5-attempt bucket.
	secondLogin := performRequest(handler, http.MethodPost, "/api/v1/auth/login",
		`{"username":"admin","password":"a-strong-password"}`, nil)
	secondCookie := secondLogin.Result().Cookies()[0]
	persisted := performRequest(handler, http.MethodPost, "/api/v1/auth/mfa/verify", body, secondCookie)
	if persisted.Code != http.StatusTooManyRequests {
		t.Fatalf("verify on new session = %d %s, want 429 (counter must survive re-login)", persisted.Code, persisted.Body.String())
	}

	// Continued hammering trips the hard lockout with a distinct message.
	locked := false
	for i := 0; i < 6; i++ {
		resp := performRequest(handler, http.MethodPost, "/api/v1/auth/mfa/verify", body, secondCookie)
		if resp.Code != http.StatusTooManyRequests {
			t.Fatalf("verify while limited = %d %s, want 429", resp.Code, resp.Body.String())
		}
		if strings.Contains(resp.Body.String(), `"account_locked"`) {
			locked = true
			break
		}
	}
	if !locked {
		t.Fatal("second factor never reached the account_locked state")
	}
}

func TestResetMFAAttemptsClearsBothLimiters(t *testing.T) {
	identityModule, _, _, _ := hardenTestEnv(t, Config{
		SessionTTL: time.Hour, PasswordMemoryKiB: 64, PasswordIterations: 1, PasswordThreads: 1,
		AttemptLimit: 3, AttemptWindow: 5 * time.Minute,
		MFALockoutLimit: 5, MFALockoutWindow: 15 * time.Minute,
	})
	const user = "user-abc"
	for i := 0; i < 3; i++ {
		if ok, _ := identityModule.allowMFAAttempt(user); !ok {
			t.Fatalf("attempt %d unexpectedly blocked", i)
		}
	}
	if ok, locked := identityModule.allowMFAAttempt(user); ok || locked {
		t.Fatalf("burst limit not enforced: ok=%v locked=%v", ok, locked)
	}
	// A successful verification clears both buckets.
	identityModule.resetMFAAttempts(user)
	if ok, _ := identityModule.allowMFAAttempt(user); !ok {
		t.Fatal("attempt blocked after reset")
	}
}
