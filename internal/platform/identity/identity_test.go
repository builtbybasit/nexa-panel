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

func TestMFAEnrollmentIsOptionalButEnforcedOnceConfirmed(t *testing.T) {
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
		`{"username":"admin","password":"a-Str0ng-password"}`, nil)
	if bootstrap.Code != http.StatusCreated {
		t.Fatalf("bootstrap = %d %s", bootstrap.Code, bootstrap.Body.String())
	}
	bootstrapCookies := bootstrap.Result().Cookies()
	// The session cookie and its double-submit companion are issued together; only
	// the session cookie is withheld from script.
	if len(bootstrapCookies) != 2 || bootstrapCookies[0].Name != cookieName || !bootstrapCookies[0].HttpOnly ||
		bootstrapCookies[1].Name != csrfCookieName || bootstrapCookies[1].HttpOnly {
		t.Fatalf("unexpected bootstrap cookies: %+v", bootstrapCookies)
	}

	// Enrollment is offered at first run, but it is an offer: the password-only
	// administrator is already signed in and reaches every protected route.
	if !strings.Contains(bootstrap.Body.String(), `"next":"mfa_enrollment"`) {
		t.Fatalf("bootstrap did not offer administrator MFA enrollment: %s", bootstrap.Body.String())
	}
	openSession := performRequest(server.Handler(), http.MethodGet, "/api/v1/auth/session", "", bootstrapCookies...)
	if openSession.Code != http.StatusOK {
		t.Fatalf("session before administrator enrollment = %d %s", openSession.Code, openSession.Body.String())
	}
	openModules := performRequest(server.Handler(), http.MethodGet, "/api/v1/modules", "", bootstrapCookies...)
	if openModules.Code != http.StatusOK {
		t.Fatalf("protected route before administrator enrollment = %d %s", openModules.Code, openModules.Body.String())
	}
	openStatus := performRequest(server.Handler(), http.MethodGet, "/api/v1/auth/status", "", bootstrapCookies...)
	if !strings.Contains(openStatus.Body.String(), `"authenticated":true`) || !strings.Contains(openStatus.Body.String(), `"mfaEnabled":false`) || !strings.Contains(openStatus.Body.String(), `"mfaEnrollmentRecommended":true`) {
		t.Fatalf("status after bootstrap = %s", openStatus.Body.String())
	}

	// Enrollment endpoints are available to the signed-in session.
	enrollment := performRequest(server.Handler(), http.MethodPost, "/api/v1/auth/mfa/enroll", "", bootstrapCookies...)
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
		`{"code":"`+confirmationCode+`"}`, bootstrapCookies...)
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

	enabledStatus := performRequest(server.Handler(), http.MethodGet, "/api/v1/auth/status", "", bootstrapCookies...)
	if !strings.Contains(enabledStatus.Body.String(), `"authenticated":true`) || !strings.Contains(enabledStatus.Body.String(), `"mfaEnabled":true`) || !strings.Contains(enabledStatus.Body.String(), `"mfaEnrollmentRecommended":false`) {
		t.Fatalf("status after enabling MFA = %s", enabledStatus.Body.String())
	}

	duplicate := performRequest(server.Handler(), http.MethodPost, "/api/v1/auth/bootstrap",
		`{"username":"other","password":"an0ther-P4ssword"}`, nil)
	if duplicate.Code != http.StatusConflict {
		t.Fatalf("second bootstrap = %d %s", duplicate.Code, duplicate.Body.String())
	}

	logout := performRequest(server.Handler(), http.MethodPost, "/api/v1/auth/logout", "", bootstrapCookies...)
	if logout.Code != http.StatusNoContent {
		t.Fatalf("logout = %d %s", logout.Code, logout.Body.String())
	}
	expiredSession := performRequest(server.Handler(), http.MethodGet, "/api/v1/auth/session", "", bootstrapCookies...)
	if expiredSession.Code != http.StatusUnauthorized {
		t.Fatalf("session after logout = %d %s", expiredSession.Code, expiredSession.Body.String())
	}

	badLogin := performRequest(server.Handler(), http.MethodPost, "/api/v1/auth/login",
		`{"username":"admin","password":"incorrect-password"}`, nil)
	if badLogin.Code != http.StatusUnauthorized {
		t.Fatalf("bad login = %d %s", badLogin.Code, badLogin.Body.String())
	}
	login := performRequest(server.Handler(), http.MethodPost, "/api/v1/auth/login",
		`{"username":"ADMIN","password":"a-Str0ng-password"}`, nil)
	if login.Code != http.StatusOK {
		t.Fatalf("login = %d %s", login.Code, login.Body.String())
	}
	loginCookies := login.Result().Cookies()
	if !strings.Contains(login.Body.String(), `"next":"mfa_challenge"`) {
		t.Fatalf("enrolled login did not require MFA: %s", login.Body.String())
	}
	// An enrolled account that has not answered the challenge cannot reach
	// protected routes, and cannot short-circuit MFA by disabling it.
	blockedModules := performRequest(server.Handler(), http.MethodGet, "/api/v1/modules", "", loginCookies...)
	if blockedModules.Code != http.StatusUnauthorized || !strings.Contains(blockedModules.Body.String(), `"mfa_required"`) {
		t.Fatalf("protected route before challenge = %d %s", blockedModules.Code, blockedModules.Body.String())
	}
	blockedDisable := performRequest(server.Handler(), http.MethodPost, "/api/v1/auth/mfa/disable",
		`{"password":"a-Str0ng-password"}`, loginCookies...)
	if blockedDisable.Code != http.StatusUnauthorized || !strings.Contains(blockedDisable.Body.String(), `"mfa_required"`) {
		t.Fatalf("disable before challenge = %d %s", blockedDisable.Code, blockedDisable.Body.String())
	}
	reusedCode := performRequest(server.Handler(), http.MethodPost, "/api/v1/auth/mfa/verify",
		`{"code":"`+confirmationCode+`"}`, loginCookies...)
	if reusedCode.Code != http.StatusUnauthorized {
		t.Fatalf("reused TOTP = %d %s", reusedCode.Code, reusedCode.Body.String())
	}
	currentTime = currentTime.Add(30 * time.Second)
	challengeCode := generateTOTP(decodedSecret, currentTime.Unix()/totpPeriodSeconds, totpDigits)
	challenge := performRequest(server.Handler(), http.MethodPost, "/api/v1/auth/mfa/verify",
		`{"code":"`+challengeCode+`"}`, loginCookies...)
	if challenge.Code != http.StatusOK {
		t.Fatalf("MFA challenge = %d %s", challenge.Code, challenge.Body.String())
	}
	challengedModules := performRequest(server.Handler(), http.MethodGet, "/api/v1/modules", "", loginCookies...)
	if challengedModules.Code != http.StatusOK {
		t.Fatalf("protected route after challenge = %d %s", challengedModules.Code, challengedModules.Body.String())
	}

	secondLogout := performRequest(server.Handler(), http.MethodPost, "/api/v1/auth/logout", "", loginCookies...)
	if secondLogout.Code != http.StatusNoContent {
		t.Fatalf("second logout = %d %s", secondLogout.Code, secondLogout.Body.String())
	}
	// A recovery code is a genuine single-use factor for every role: it answers the
	// ordinary challenge and signs the administrator straight in.
	recoveryLogin := performRequest(server.Handler(), http.MethodPost, "/api/v1/auth/login",
		`{"username":"admin","password":"a-Str0ng-password"}`, nil)
	recoveryCookies := recoveryLogin.Result().Cookies()
	recoveryChallenge := performRequest(server.Handler(), http.MethodPost, "/api/v1/auth/mfa/verify",
		`{"recoveryCode":"`+confirmationBody.RecoveryCodes[0]+`"}`, recoveryCookies...)
	if recoveryChallenge.Code != http.StatusOK {
		t.Fatalf("administrator recovery-code login = %d %s", recoveryChallenge.Code, recoveryChallenge.Body.String())
	}
	recoveredModules := performRequest(server.Handler(), http.MethodGet, "/api/v1/modules", "", recoveryCookies...)
	if recoveredModules.Code != http.StatusOK {
		t.Fatalf("protected route after recovery-code login = %d %s", recoveredModules.Code, recoveredModules.Body.String())
	}
	secondLogin := performRequest(server.Handler(), http.MethodPost, "/api/v1/auth/login",
		`{"username":"admin","password":"a-Str0ng-password"}`, nil)
	secondCookies := secondLogin.Result().Cookies()
	recovery := performRequest(server.Handler(), http.MethodPost, "/api/v1/auth/mfa/recover",
		`{"recoveryCode":"`+confirmationBody.RecoveryCodes[1]+`"}`, secondCookies...)
	if recovery.Code != http.StatusOK {
		t.Fatalf("administrator MFA recovery = %d %s", recovery.Code, recovery.Body.String())
	}
	var replacementEnrollment struct {
		Secret          string `json:"secret"`
		ProvisioningURI string `json:"provisioningUri"`
	}
	if err := json.Unmarshal(recovery.Body.Bytes(), &replacementEnrollment); err != nil || replacementEnrollment.Secret == "" || replacementEnrollment.Secret == enrollmentBody.Secret {
		t.Fatalf("replacement enrollment = %s, err %v", recovery.Body.String(), err)
	}
	// Recovery retires the factor, so the account is unenrolled again: the panel
	// stays reachable while the replacement authenticator is paired, and the
	// session that consumed the recovery code is the only one left.
	afterRecovery := performRequest(server.Handler(), http.MethodGet, "/api/v1/modules", "", secondCookies...)
	if afterRecovery.Code != http.StatusOK {
		t.Fatalf("protected route during factor replacement = %d %s", afterRecovery.Code, afterRecovery.Body.String())
	}
	revoked := performRequest(server.Handler(), http.MethodGet, "/api/v1/auth/session", "", recoveryCookies...)
	if revoked.Code != http.StatusUnauthorized {
		t.Fatalf("other session after factor replacement = %d %s", revoked.Code, revoked.Body.String())
	}
	replacementSecret, err := base32Encoding.DecodeString(replacementEnrollment.Secret)
	if err != nil {
		t.Fatalf("decode replacement TOTP secret: %v", err)
	}
	replacementCode := generateTOTP(replacementSecret, currentTime.Unix()/totpPeriodSeconds, totpDigits)
	replacementConfirmation := performRequest(server.Handler(), http.MethodPost, "/api/v1/auth/mfa/confirm",
		`{"code":"`+replacementCode+`"}`, secondCookies...)
	if replacementConfirmation.Code != http.StatusOK {
		t.Fatalf("replacement MFA confirmation = %d %s", replacementConfirmation.Code, replacementConfirmation.Body.String())
	}

	events, err := auditModule.List(ctx, 40)
	if err != nil {
		t.Fatalf("list audit events: %v", err)
	}
	if len(events) < 7 {
		t.Fatalf("audit event count = %d, want at least 7", len(events))
	}
}

// TestAdministratorCanDisableMFAWithStepUpAndPassword covers the deliberate exit
// from an optional factor: an administrator may turn MFA back off, but only from
// a session that answered a challenge and only with the account password, and
// the removal is audited.
func TestAdministratorCanDisableMFAWithStepUpAndPassword(t *testing.T) {
	database, err := persistence.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	ctx := context.Background()
	if err := persistence.RunMigrations(ctx, database); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	auditModule, err := audit.New(ctx, database)
	if err != nil {
		t.Fatalf("create audit module: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	secretBox, err := secrets.New(bytes.Repeat([]byte{7}, 32))
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

	bootstrap := performRequest(server.Handler(), http.MethodPost, "/api/v1/auth/bootstrap",
		`{"username":"admin","password":"a-Str0ng-password"}`, nil)
	if bootstrap.Code != http.StatusCreated {
		t.Fatalf("bootstrap = %d %s", bootstrap.Code, bootstrap.Body.String())
	}
	cookies := bootstrap.Result().Cookies()
	enrollment := performRequest(server.Handler(), http.MethodPost, "/api/v1/auth/mfa/enroll", "", cookies...)
	var enrollmentBody struct {
		Secret string `json:"secret"`
	}
	if err := json.Unmarshal(enrollment.Body.Bytes(), &enrollmentBody); err != nil {
		t.Fatalf("decode enrollment: %v", err)
	}
	secret, err := base32Encoding.DecodeString(enrollmentBody.Secret)
	if err != nil {
		t.Fatalf("decode TOTP secret: %v", err)
	}
	// Confirming the factor also marks this session challenged, which is the
	// step-up the disable route demands.
	confirmation := performRequest(server.Handler(), http.MethodPost, "/api/v1/auth/mfa/confirm",
		`{"code":"`+generateTOTP(secret, currentTime.Unix()/totpPeriodSeconds, totpDigits)+`"}`, cookies...)
	if confirmation.Code != http.StatusOK {
		t.Fatalf("MFA confirmation = %d %s", confirmation.Code, confirmation.Body.String())
	}

	wrongPassword := performRequest(server.Handler(), http.MethodPost, "/api/v1/auth/mfa/disable",
		`{"password":"not-the-P4ssword"}`, cookies...)
	if wrongPassword.Code != http.StatusUnauthorized || !strings.Contains(wrongPassword.Body.String(), `"invalid_credentials"`) {
		t.Fatalf("disable with the wrong password = %d %s", wrongPassword.Code, wrongPassword.Body.String())
	}
	disable := performRequest(server.Handler(), http.MethodPost, "/api/v1/auth/mfa/disable",
		`{"password":"a-Str0ng-password"}`, cookies...)
	if disable.Code != http.StatusOK || !strings.Contains(disable.Body.String(), `"mfaEnabled":false`) {
		t.Fatalf("disable administrator MFA = %d %s", disable.Code, disable.Body.String())
	}
	disabledStatus := performRequest(server.Handler(), http.MethodGet, "/api/v1/auth/status", "", cookies...)
	if !strings.Contains(disabledStatus.Body.String(), `"authenticated":true`) || !strings.Contains(disabledStatus.Body.String(), `"mfaEnabled":false`) {
		t.Fatalf("status after disabling MFA = %s", disabledStatus.Body.String())
	}
	// The password alone signs the administrator back in and reaches the panel.
	login := performRequest(server.Handler(), http.MethodPost, "/api/v1/auth/login",
		`{"username":"admin","password":"a-Str0ng-password"}`, nil)
	if login.Code != http.StatusOK || !strings.Contains(login.Body.String(), `"next":"authenticated"`) {
		t.Fatalf("password-only administrator login = %d %s", login.Code, login.Body.String())
	}
	if reached := performRequest(server.Handler(), http.MethodGet, "/api/v1/modules", "", login.Result().Cookies()...); reached.Code != http.StatusOK {
		t.Fatalf("protected route for a password-only administrator = %d %s", reached.Code, reached.Body.String())
	}

	events, err := auditModule.List(ctx, 20)
	if err != nil {
		t.Fatalf("list audit events: %v", err)
	}
	disabled := false
	for _, event := range events {
		if event.Action == "identity.mfa_disabled" {
			disabled = true
		}
	}
	if !disabled {
		t.Fatal("expected an identity.mfa_disabled audit event")
	}
}

func TestSelfServicePasswordChange(t *testing.T) {
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
		`{"username":"admin","password":"first-P4ssword"}`, nil)
	if bootstrap.Code != http.StatusCreated {
		t.Fatalf("bootstrap = %d %s", bootstrap.Code, bootstrap.Body.String())
	}
	cookies := bootstrap.Result().Cookies()

	// A wrong current password is rejected.
	wrong := performRequest(server.Handler(), http.MethodPost, "/api/v1/auth/password",
		`{"currentPassword":"nope","newPassword":"second-P4ssword"}`, cookies...)
	if wrong.Code != http.StatusUnauthorized || !strings.Contains(wrong.Body.String(), `"invalid_credentials"`) {
		t.Fatalf("wrong current password = %d %s", wrong.Code, wrong.Body.String())
	}
	// A too-short new password is rejected before anything changes.
	weak := performRequest(server.Handler(), http.MethodPost, "/api/v1/auth/password",
		`{"currentPassword":"first-P4ssword","newPassword":"short"}`, cookies...)
	if weak.Code != http.StatusUnprocessableEntity {
		t.Fatalf("weak new password = %d %s", weak.Code, weak.Body.String())
	}
	// A no-op change is rejected.
	unchanged := performRequest(server.Handler(), http.MethodPost, "/api/v1/auth/password",
		`{"currentPassword":"first-P4ssword","newPassword":"first-P4ssword"}`, cookies...)
	if unchanged.Code != http.StatusUnprocessableEntity || !strings.Contains(unchanged.Body.String(), `"password_unchanged"`) {
		t.Fatalf("unchanged password = %d %s", unchanged.Code, unchanged.Body.String())
	}

	// Open a second session that the change should revoke.
	otherLogin := performRequest(server.Handler(), http.MethodPost, "/api/v1/auth/login",
		`{"username":"admin","password":"first-P4ssword"}`, nil)
	if otherLogin.Code != http.StatusOK {
		t.Fatalf("second login = %d %s", otherLogin.Code, otherLogin.Body.String())
	}
	otherCookies := otherLogin.Result().Cookies()

	change := performRequest(server.Handler(), http.MethodPost, "/api/v1/auth/password",
		`{"currentPassword":"first-P4ssword","newPassword":"second-P4ssword"}`, cookies...)
	if change.Code != http.StatusNoContent {
		t.Fatalf("password change = %d %s", change.Code, change.Body.String())
	}
	// The current session stays live; the other session is revoked.
	keptSession := performRequest(server.Handler(), http.MethodGet, "/api/v1/auth/session", "", cookies...)
	if keptSession.Code != http.StatusOK {
		t.Fatalf("current session after change = %d %s", keptSession.Code, keptSession.Body.String())
	}
	revokedSession := performRequest(server.Handler(), http.MethodGet, "/api/v1/auth/session", "", otherCookies...)
	if revokedSession.Code != http.StatusUnauthorized {
		t.Fatalf("other session after change = %d %s", revokedSession.Code, revokedSession.Body.String())
	}

	// The old password no longer works; the new one does.
	oldLogin := performRequest(server.Handler(), http.MethodPost, "/api/v1/auth/login",
		`{"username":"admin","password":"first-P4ssword"}`, nil)
	if oldLogin.Code != http.StatusUnauthorized {
		t.Fatalf("old password login = %d %s", oldLogin.Code, oldLogin.Body.String())
	}
	newLogin := performRequest(server.Handler(), http.MethodPost, "/api/v1/auth/login",
		`{"username":"admin","password":"second-P4ssword"}`, nil)
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

// performRequest stands in for the panel's own UI: it presents whichever cookies
// the server issued and, because the origin check now fails closed, the fetch
// metadata a browser would attach. Passing the CSRF cookie among cookies also
// echoes its value back in the header, completing the double submit.
func performRequest(handler http.Handler, method, path, body string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	// The control plane refuses non-loopback plaintext HTTP; these tests exercise
	// the documented loopback bootstrap workflow, so present a loopback client.
	request.RemoteAddr = "127.0.0.1:12345"
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	for _, cookie := range cookies {
		if cookie == nil {
			continue
		}
		request.AddCookie(cookie)
		if cookie.Name == csrfCookieName {
			request.Header.Set(csrfHeaderName, cookie.Value)
		}
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
