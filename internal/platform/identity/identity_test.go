package identity

import (
	"bytes"
	"context"
	"encoding/json"
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

func TestPasswordHashRoundTrip(t *testing.T) {
	parameters := passwordParameters{memory: 64, iterations: 1, parallelism: 1, saltLength: 16, keyLength: 32}
	encoded, err := hashPassword("correct horse battery staple", parameters)
	if err != nil {
		t.Fatalf("hashPassword returned an error: %v", err)
	}
	valid, err := verifyPassword("correct horse battery staple", encoded)
	if err != nil || !valid {
		t.Fatalf("verifyPassword(valid) = %v, %v", valid, err)
	}
	valid, err = verifyPassword("wrong password", encoded)
	if err != nil || valid {
		t.Fatalf("verifyPassword(invalid) = %v, %v", valid, err)
	}
}

func TestBootstrapOptionalMFAEnableDisableAndLogin(t *testing.T) {
	database, err := persistence.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	ctx := context.Background()
	auditModule, err := audit.New(ctx, database)
	if err != nil {
		t.Fatalf("create audit module: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	secretBox, err := secrets.New(bytes.Repeat([]byte{9}, 32))
	if err != nil {
		t.Fatalf("create secret box: %v", err)
	}
	identityModule, err := NewWithConfig(ctx, database, auditModule, secretBox, logger, Config{
		SessionTTL: time.Hour, PasswordMemoryKiB: 64, PasswordIterations: 1, PasswordThreads: 1,
	})
	if err != nil {
		t.Fatalf("create identity module: %v", err)
	}
	currentTime := time.Unix(1_700_000_000, 0).UTC()
	identityModule.now = func() time.Time { return currentTime }
	server, err := controlplane.New("test", []module.Module{identityModule, auditModule}, logger,
		controlplane.WithAuthentication(identityModule), controlplane.WithAuthorization(testAuthorization{}))
	if err != nil {
		t.Fatalf("create control plane: %v", err)
	}

	status := performRequest(server.Handler(), http.MethodGet, "/api/v1/auth/status", "", nil)
	if status.Code != http.StatusOK || !strings.Contains(status.Body.String(), `"bootstrapRequired":true`) {
		t.Fatalf("initial status = %d %s", status.Code, status.Body.String())
	}

	bootstrap := performRequest(server.Handler(), http.MethodPost, "/api/v1/auth/bootstrap",
		`{"username":"admin","password":"a-strong-password"}`, nil)
	if bootstrap.Code != http.StatusCreated {
		t.Fatalf("bootstrap = %d %s", bootstrap.Code, bootstrap.Body.String())
	}
	bootstrapCookies := bootstrap.Result().Cookies()
	if len(bootstrapCookies) != 1 || bootstrapCookies[0].Name != cookieName || !bootstrapCookies[0].HttpOnly {
		t.Fatalf("unexpected bootstrap cookies: %+v", bootstrapCookies)
	}

	// Bootstrap offers enrollment but no longer forces it: the session works
	// immediately, and the account can reach protected routes without a second
	// factor.
	if !strings.Contains(bootstrap.Body.String(), `"next":"mfa_enrollment"`) {
		t.Fatalf("bootstrap did not offer enrollment: %s", bootstrap.Body.String())
	}
	openSession := performRequest(server.Handler(), http.MethodGet, "/api/v1/auth/session", "", bootstrapCookies[0])
	if openSession.Code != http.StatusOK || !strings.Contains(openSession.Body.String(), `"username":"admin"`) {
		t.Fatalf("session without MFA = %d %s", openSession.Code, openSession.Body.String())
	}
	openModules := performRequest(server.Handler(), http.MethodGet, "/api/v1/modules", "", bootstrapCookies[0])
	if openModules.Code != http.StatusOK {
		t.Fatalf("protected route without MFA = %d %s", openModules.Code, openModules.Body.String())
	}
	openStatus := performRequest(server.Handler(), http.MethodGet, "/api/v1/auth/status", "", bootstrapCookies[0])
	if !strings.Contains(openStatus.Body.String(), `"authenticated":true`) || !strings.Contains(openStatus.Body.String(), `"mfaEnabled":false`) {
		t.Fatalf("status without MFA = %s", openStatus.Body.String())
	}

	// Enable MFA later, from within an already-authenticated session.
	enrollment := performRequest(server.Handler(), http.MethodPost, "/api/v1/auth/mfa/enroll", "", bootstrapCookies[0])
	if enrollment.Code != http.StatusOK {
		t.Fatalf("enrollment = %d %s", enrollment.Code, enrollment.Body.String())
	}
	var enrollmentBody struct {
		Secret          string `json:"secret"`
		ProvisioningURI string `json:"provisioningUri"`
	}
	if err := json.Unmarshal(enrollment.Body.Bytes(), &enrollmentBody); err != nil {
		t.Fatalf("decode enrollment: %v", err)
	}
	if enrollmentBody.Secret == "" || !strings.HasPrefix(enrollmentBody.ProvisioningURI, "otpauth://totp/") {
		t.Fatalf("unexpected enrollment: %+v", enrollmentBody)
	}
	decodedSecret, err := base32Encoding.DecodeString(enrollmentBody.Secret)
	if err != nil {
		t.Fatalf("decode TOTP secret: %v", err)
	}
	confirmationCode := generateTOTP(decodedSecret, currentTime.Unix()/totpPeriodSeconds, totpDigits)
	confirmation := performRequest(server.Handler(), http.MethodPost, "/api/v1/auth/mfa/confirm",
		`{"code":"`+confirmationCode+`"}`, bootstrapCookies[0])
	if confirmation.Code != http.StatusOK {
		t.Fatalf("MFA confirmation = %d %s", confirmation.Code, confirmation.Body.String())
	}
	var confirmationBody struct {
		RecoveryCodes []string `json:"recoveryCodes"`
	}
	if err := json.Unmarshal(confirmation.Body.Bytes(), &confirmationBody); err != nil {
		t.Fatalf("decode confirmation: %v", err)
	}
	if len(confirmationBody.RecoveryCodes) != 10 {
		t.Fatalf("recovery code count = %d, want 10", len(confirmationBody.RecoveryCodes))
	}

	enabledStatus := performRequest(server.Handler(), http.MethodGet, "/api/v1/auth/status", "", bootstrapCookies[0])
	if !strings.Contains(enabledStatus.Body.String(), `"authenticated":true`) || !strings.Contains(enabledStatus.Body.String(), `"mfaEnabled":true`) {
		t.Fatalf("status after enabling MFA = %s", enabledStatus.Body.String())
	}

	duplicate := performRequest(server.Handler(), http.MethodPost, "/api/v1/auth/bootstrap",
		`{"username":"other","password":"another-password"}`, nil)
	if duplicate.Code != http.StatusConflict {
		t.Fatalf("second bootstrap = %d %s", duplicate.Code, duplicate.Body.String())
	}

	logout := performRequest(server.Handler(), http.MethodPost, "/api/v1/auth/logout", "", bootstrapCookies[0])
	if logout.Code != http.StatusNoContent {
		t.Fatalf("logout = %d %s", logout.Code, logout.Body.String())
	}
	expiredSession := performRequest(server.Handler(), http.MethodGet, "/api/v1/auth/session", "", bootstrapCookies[0])
	if expiredSession.Code != http.StatusUnauthorized {
		t.Fatalf("session after logout = %d %s", expiredSession.Code, expiredSession.Body.String())
	}

	badLogin := performRequest(server.Handler(), http.MethodPost, "/api/v1/auth/login",
		`{"username":"admin","password":"incorrect-password"}`, nil)
	if badLogin.Code != http.StatusUnauthorized {
		t.Fatalf("bad login = %d %s", badLogin.Code, badLogin.Body.String())
	}
	login := performRequest(server.Handler(), http.MethodPost, "/api/v1/auth/login",
		`{"username":"ADMIN","password":"a-strong-password"}`, nil)
	if login.Code != http.StatusOK {
		t.Fatalf("login = %d %s", login.Code, login.Body.String())
	}
	loginCookie := login.Result().Cookies()[0]
	if !strings.Contains(login.Body.String(), `"next":"mfa_challenge"`) {
		t.Fatalf("enrolled login did not require MFA: %s", login.Body.String())
	}
	// An enrolled account that has not answered the challenge cannot reach
	// protected routes, and cannot short-circuit MFA by disabling it.
	blockedModules := performRequest(server.Handler(), http.MethodGet, "/api/v1/modules", "", loginCookie)
	if blockedModules.Code != http.StatusUnauthorized || !strings.Contains(blockedModules.Body.String(), `"mfa_required"`) {
		t.Fatalf("protected route before challenge = %d %s", blockedModules.Code, blockedModules.Body.String())
	}
	blockedDisable := performRequest(server.Handler(), http.MethodPost, "/api/v1/auth/mfa/disable",
		`{"password":"a-strong-password"}`, loginCookie)
	if blockedDisable.Code != http.StatusUnauthorized || !strings.Contains(blockedDisable.Body.String(), `"mfa_required"`) {
		t.Fatalf("disable before challenge = %d %s", blockedDisable.Code, blockedDisable.Body.String())
	}
	reusedCode := performRequest(server.Handler(), http.MethodPost, "/api/v1/auth/mfa/verify",
		`{"code":"`+confirmationCode+`"}`, loginCookie)
	if reusedCode.Code != http.StatusUnauthorized {
		t.Fatalf("reused TOTP = %d %s", reusedCode.Code, reusedCode.Body.String())
	}
	currentTime = currentTime.Add(30 * time.Second)
	challengeCode := generateTOTP(decodedSecret, currentTime.Unix()/totpPeriodSeconds, totpDigits)
	challenge := performRequest(server.Handler(), http.MethodPost, "/api/v1/auth/mfa/verify",
		`{"code":"`+challengeCode+`"}`, loginCookie)
	if challenge.Code != http.StatusOK {
		t.Fatalf("MFA challenge = %d %s", challenge.Code, challenge.Body.String())
	}

	secondLogout := performRequest(server.Handler(), http.MethodPost, "/api/v1/auth/logout", "", loginCookie)
	if secondLogout.Code != http.StatusNoContent {
		t.Fatalf("second logout = %d %s", secondLogout.Code, secondLogout.Body.String())
	}
	secondLogin := performRequest(server.Handler(), http.MethodPost, "/api/v1/auth/login",
		`{"username":"admin","password":"a-strong-password"}`, nil)
	secondCookie := secondLogin.Result().Cookies()[0]
	recovery := performRequest(server.Handler(), http.MethodPost, "/api/v1/auth/mfa/verify",
		`{"recoveryCode":"`+confirmationBody.RecoveryCodes[0]+`"}`, secondCookie)
	if recovery.Code != http.StatusOK {
		t.Fatalf("recovery challenge = %d %s", recovery.Code, recovery.Body.String())
	}

	// Disabling requires the current password; a wrong one is rejected.
	wrongDisable := performRequest(server.Handler(), http.MethodPost, "/api/v1/auth/mfa/disable",
		`{"password":"not-my-password"}`, secondCookie)
	if wrongDisable.Code != http.StatusUnauthorized || !strings.Contains(wrongDisable.Body.String(), `"invalid_credentials"`) {
		t.Fatalf("disable with wrong password = %d %s", wrongDisable.Code, wrongDisable.Body.String())
	}
	disable := performRequest(server.Handler(), http.MethodPost, "/api/v1/auth/mfa/disable",
		`{"password":"a-strong-password"}`, secondCookie)
	if disable.Code != http.StatusOK || !strings.Contains(disable.Body.String(), `"mfaEnabled":false`) {
		t.Fatalf("disable MFA = %d %s", disable.Code, disable.Body.String())
	}

	// After disabling, the next sign-in skips the challenge entirely.
	thirdLogout := performRequest(server.Handler(), http.MethodPost, "/api/v1/auth/logout", "", secondCookie)
	if thirdLogout.Code != http.StatusNoContent {
		t.Fatalf("third logout = %d %s", thirdLogout.Code, thirdLogout.Body.String())
	}
	finalLogin := performRequest(server.Handler(), http.MethodPost, "/api/v1/auth/login",
		`{"username":"admin","password":"a-strong-password"}`, nil)
	if finalLogin.Code != http.StatusOK || !strings.Contains(finalLogin.Body.String(), `"next":"authenticated"`) {
		t.Fatalf("login after disabling MFA = %d %s", finalLogin.Code, finalLogin.Body.String())
	}
	finalCookie := finalLogin.Result().Cookies()[0]
	finalModules := performRequest(server.Handler(), http.MethodGet, "/api/v1/modules", "", finalCookie)
	if finalModules.Code != http.StatusOK {
		t.Fatalf("protected route after disabling MFA = %d %s", finalModules.Code, finalModules.Body.String())
	}

	events, err := auditModule.List(ctx, 30)
	if err != nil {
		t.Fatalf("list audit events: %v", err)
	}
	if len(events) < 9 {
		t.Fatalf("audit event count = %d, want at least 9", len(events))
	}
}

func TestSelfServicePasswordChange(t *testing.T) {
	database, err := persistence.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	ctx := context.Background()
	auditModule, err := audit.New(ctx, database)
	if err != nil {
		t.Fatalf("create audit module: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	secretBox, err := secrets.New(bytes.Repeat([]byte{5}, 32))
	if err != nil {
		t.Fatalf("create secret box: %v", err)
	}
	identityModule, err := NewWithConfig(ctx, database, auditModule, secretBox, logger, Config{
		SessionTTL: time.Hour, PasswordMemoryKiB: 64, PasswordIterations: 1, PasswordThreads: 1,
	})
	if err != nil {
		t.Fatalf("create identity module: %v", err)
	}
	server, err := controlplane.New("test", []module.Module{identityModule, auditModule}, logger,
		controlplane.WithAuthentication(identityModule), controlplane.WithAuthorization(testAuthorization{}))
	if err != nil {
		t.Fatalf("create control plane: %v", err)
	}

	bootstrap := performRequest(server.Handler(), http.MethodPost, "/api/v1/auth/bootstrap",
		`{"username":"admin","password":"first-password"}`, nil)
	if bootstrap.Code != http.StatusCreated {
		t.Fatalf("bootstrap = %d %s", bootstrap.Code, bootstrap.Body.String())
	}
	cookie := bootstrap.Result().Cookies()[0]

	// A wrong current password is rejected.
	wrong := performRequest(server.Handler(), http.MethodPost, "/api/v1/auth/password",
		`{"currentPassword":"nope","newPassword":"second-password"}`, cookie)
	if wrong.Code != http.StatusUnauthorized || !strings.Contains(wrong.Body.String(), `"invalid_credentials"`) {
		t.Fatalf("wrong current password = %d %s", wrong.Code, wrong.Body.String())
	}
	// A too-short new password is rejected before anything changes.
	weak := performRequest(server.Handler(), http.MethodPost, "/api/v1/auth/password",
		`{"currentPassword":"first-password","newPassword":"short"}`, cookie)
	if weak.Code != http.StatusUnprocessableEntity {
		t.Fatalf("weak new password = %d %s", weak.Code, weak.Body.String())
	}
	// A no-op change is rejected.
	unchanged := performRequest(server.Handler(), http.MethodPost, "/api/v1/auth/password",
		`{"currentPassword":"first-password","newPassword":"first-password"}`, cookie)
	if unchanged.Code != http.StatusUnprocessableEntity || !strings.Contains(unchanged.Body.String(), `"password_unchanged"`) {
		t.Fatalf("unchanged password = %d %s", unchanged.Code, unchanged.Body.String())
	}

	// Open a second session that the change should revoke.
	otherLogin := performRequest(server.Handler(), http.MethodPost, "/api/v1/auth/login",
		`{"username":"admin","password":"first-password"}`, nil)
	if otherLogin.Code != http.StatusOK {
		t.Fatalf("second login = %d %s", otherLogin.Code, otherLogin.Body.String())
	}
	otherCookie := otherLogin.Result().Cookies()[0]

	change := performRequest(server.Handler(), http.MethodPost, "/api/v1/auth/password",
		`{"currentPassword":"first-password","newPassword":"second-password"}`, cookie)
	if change.Code != http.StatusNoContent {
		t.Fatalf("password change = %d %s", change.Code, change.Body.String())
	}
	// The current session stays live; the other session is revoked.
	keptSession := performRequest(server.Handler(), http.MethodGet, "/api/v1/auth/session", "", cookie)
	if keptSession.Code != http.StatusOK {
		t.Fatalf("current session after change = %d %s", keptSession.Code, keptSession.Body.String())
	}
	revokedSession := performRequest(server.Handler(), http.MethodGet, "/api/v1/auth/session", "", otherCookie)
	if revokedSession.Code != http.StatusUnauthorized {
		t.Fatalf("other session after change = %d %s", revokedSession.Code, revokedSession.Body.String())
	}

	// The old password no longer works; the new one does.
	oldLogin := performRequest(server.Handler(), http.MethodPost, "/api/v1/auth/login",
		`{"username":"admin","password":"first-password"}`, nil)
	if oldLogin.Code != http.StatusUnauthorized {
		t.Fatalf("old password login = %d %s", oldLogin.Code, oldLogin.Body.String())
	}
	newLogin := performRequest(server.Handler(), http.MethodPost, "/api/v1/auth/login",
		`{"username":"admin","password":"second-password"}`, nil)
	if newLogin.Code != http.StatusOK || !strings.Contains(newLogin.Body.String(), `"next":"authenticated"`) {
		t.Fatalf("new password login = %d %s", newLogin.Code, newLogin.Body.String())
	}

	changed := false
	events, err := auditModule.List(ctx, 30)
	if err != nil {
		t.Fatalf("list audit events: %v", err)
	}
	for _, event := range events {
		if event.Action == "identity.password_changed" {
			changed = true
		}
	}
	if !changed {
		t.Fatal("expected an identity.password_changed audit event")
	}
}

type testAuthorization struct{}

func (testAuthorization) Middleware(_ string, next http.Handler) http.Handler { return next }

func performRequest(handler http.Handler, method, path, body string, cookie *http.Cookie) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if cookie != nil {
		request.AddCookie(cookie)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
