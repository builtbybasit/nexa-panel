package identity

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/platform/audit"
)

// enrollAndConfirm drives a seeded account through the real sign-in and
// enrollment endpoints, so these tests act on the same stored state a live
// account would carry rather than on hand-written columns.
func enrollAndConfirm(t *testing.T, harness *usersHarness, username string) (cookies []*http.Cookie, recoveryCodes []string) {
	t.Helper()
	login := performRequest(harness.mux, http.MethodPost, "/api/v1/auth/login",
		`{"username":"`+username+`","password":"a-Str0ng-password"}`, nil)
	if login.Code != http.StatusOK {
		t.Fatalf("login = %d %s", login.Code, login.Body.String())
	}
	cookies = login.Result().Cookies()

	enroll := performRequest(harness.mux, http.MethodPost, "/api/v1/auth/mfa/enroll", "", cookies...)
	if enroll.Code != http.StatusOK {
		t.Fatalf("enroll = %d %s", enroll.Code, enroll.Body.String())
	}
	var enrollment struct {
		Secret string `json:"secret"`
	}
	if err := json.Unmarshal(enroll.Body.Bytes(), &enrollment); err != nil {
		t.Fatalf("decode enrollment: %v", err)
	}
	secret, err := base32Encoding.DecodeString(enrollment.Secret)
	if err != nil {
		t.Fatalf("decode secret: %v", err)
	}
	now := harness.module.now().UTC()
	code := generateTOTP(secret, now.Unix()/totpPeriodSeconds, totpDigits)
	confirm := performRequest(harness.mux, http.MethodPost, "/api/v1/auth/mfa/confirm",
		`{"code":"`+code+`"}`, cookies...)
	if confirm.Code != http.StatusOK {
		t.Fatalf("confirm = %d %s", confirm.Code, confirm.Body.String())
	}
	var confirmation struct {
		RecoveryCodes []string `json:"recoveryCodes"`
	}
	if err := json.Unmarshal(confirm.Body.Bytes(), &confirmation); err != nil {
		t.Fatalf("decode confirmation: %v", err)
	}
	if len(confirmation.RecoveryCodes) == 0 {
		t.Fatal("confirmation returned no recovery codes")
	}
	return cookies, confirmation.RecoveryCodes
}

// A lost phone must not be an administrator-only problem. Before this, a
// developer or operator holding a valid recovery code could sign in with it but
// was refused the endpoint that re-pairs an authenticator, so their account was
// stuck answering challenges it could never satisfy again.
func TestMFARecoveryIsAvailableToEveryEnrolledRole(t *testing.T) {
	for _, role := range []string{"admin", "operator", "developer", "viewer"} {
		t.Run(role, func(t *testing.T) {
			harness := newUsersHarness(t)
			harness.seedUser(t, "person", role)
			cookies, recoveryCodes := enrollAndConfirm(t, harness, "person")

			// Sign in fresh, as someone who has just lost their authenticator would.
			login := performRequest(harness.mux, http.MethodPost, "/api/v1/auth/login",
				`{"username":"person","password":"a-Str0ng-password"}`, nil)
			replacementCookies := login.Result().Cookies()
			recovery := performRequest(harness.mux, http.MethodPost, "/api/v1/auth/mfa/recover",
				`{"recoveryCode":"`+recoveryCodes[0]+`"}`, replacementCookies...)
			if recovery.Code != http.StatusOK {
				t.Fatalf("recover as %s = %d %s", role, recovery.Code, recovery.Body.String())
			}
			var replacement struct {
				Secret string `json:"secret"`
			}
			if err := json.Unmarshal(recovery.Body.Bytes(), &replacement); err != nil || replacement.Secret == "" {
				t.Fatalf("replacement enrollment = %s, err %v", recovery.Body.String(), err)
			}
			// The session that consumed the code is the only one left, and it can
			// finish pairing the replacement.
			status := performRequest(harness.mux, http.MethodGet, "/api/v1/auth/status", "", cookies...)
			if !containsJSONField(status.Body.String(), `"authenticated":false`) {
				t.Fatalf("the pre-recovery session survived: %s", status.Body.String())
			}
		})
	}
}

// A recovery code is still the credential: a wrong one must not rotate anything,
// whatever the role.
func TestMFARecoveryStillRequiresAValidRecoveryCode(t *testing.T) {
	harness := newUsersHarness(t)
	harness.seedUser(t, "person", "developer")
	enrollAndConfirm(t, harness, "person")

	login := performRequest(harness.mux, http.MethodPost, "/api/v1/auth/login",
		`{"username":"person","password":"a-Str0ng-password"}`, nil)
	response := performRequest(harness.mux, http.MethodPost, "/api/v1/auth/mfa/recover",
		`{"recoveryCode":"not-a-real-code"}`, login.Result().Cookies()...)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("recovery with an invalid code = %d %s", response.Code, response.Body.String())
	}
	model := new(userModel)
	if err := harness.module.database.NewSelect().Model(model).
		Column("totp_confirmed_at").Where("username = ?", "person").Scan(context.Background()); err != nil {
		t.Fatalf("read account: %v", err)
	}
	if model.TOTPConfirmedAt == nil {
		t.Fatal("an invalid recovery code retired the factor")
	}
}

func TestResetSecondFactorClearsTheFactorAndEverySession(t *testing.T) {
	harness := newUsersHarness(t)
	person := harness.seedUser(t, "locked-out", "operator")
	enrollAndConfirm(t, harness, "locked-out")
	// A second, already-verified session: the reset must take that one too, or a
	// live session would outlive the factor it answered.
	harness.seedSession(t, person.ID)

	resetAt := time.Unix(1_700_001_000, 0).UTC()
	result, err := ResetSecondFactor(context.Background(), harness.module.database, harness.audit, "locked-out", resetAt)
	if err != nil {
		t.Fatalf("reset: %v", err)
	}
	if result.UserID != person.ID || result.Username != "locked-out" || result.Role != "operator" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if !result.WasEnrolled {
		t.Fatal("an enrolled account reported WasEnrolled false")
	}
	if result.SessionsRevoked < 2 {
		t.Fatalf("sessions revoked = %d, want at least the enrollment session and the seeded one", result.SessionsRevoked)
	}
	if remaining := harness.sessionCount(t, person.ID); remaining != 0 {
		t.Fatalf("sessions remaining after reset = %d", remaining)
	}

	model := new(userModel)
	if err := harness.module.database.NewSelect().Model(model).
		Column("totp_secret_encrypted", "totp_confirmed_at", "totp_last_step", "recovery_code_hashes").
		Where("id = ?", person.ID).Scan(context.Background()); err != nil {
		t.Fatalf("read account: %v", err)
	}
	if model.TOTPSecretEncrypted != nil || model.TOTPConfirmedAt != nil || model.TOTPLastStep != 0 || model.RecoveryCodeHashes != "[]" {
		t.Fatalf("factor material survived the reset: %+v", model)
	}

	// The account signs straight in on its password and is pointed at enrollment:
	// break-glass must not leave the operator locked out the other way.
	login := performRequest(harness.mux, http.MethodPost, "/api/v1/auth/login",
		`{"username":"locked-out","password":"a-Str0ng-password"}`, nil)
	if login.Code != http.StatusOK || !containsJSONField(login.Body.String(), `"next":"authenticated"`) {
		t.Fatalf("login after reset = %d %s", login.Code, login.Body.String())
	}
	status := performRequest(harness.mux, http.MethodGet, "/api/v1/auth/status", "", login.Result().Cookies()...)
	if !containsJSONField(status.Body.String(), `"mfaEnrollmentRecommended":true`) {
		t.Fatalf("status after reset = %s", status.Body.String())
	}
}

func TestResetSecondFactorWritesAnAuditRecord(t *testing.T) {
	harness := newUsersHarness(t)
	person := harness.seedUser(t, "locked-out", "admin")
	enrollAndConfirm(t, harness, "locked-out")

	if _, err := ResetSecondFactor(context.Background(), harness.module.database, harness.audit,
		"locked-out", time.Unix(1_700_001_000, 0).UTC()); err != nil {
		t.Fatalf("reset: %v", err)
	}
	events, err := harness.audit.List(context.Background(), 50)
	if err != nil {
		t.Fatalf("list audit events: %v", err)
	}
	var recorded *audit.Event
	for index := range events {
		if events[index].Action == "identity.mfa_break_glass_reset" {
			recorded = &events[index]
			break
		}
	}
	if recorded == nil {
		t.Fatalf("no break-glass audit event in %d events", len(events))
	}
	if recorded.Subject != "user:"+person.ID {
		t.Fatalf("audit subject = %q", recorded.Subject)
	}
	// A root operator has no panel identity; the record must say so rather than
	// attribute the removal to the account it acted on.
	if recorded.ActorUserID != nil {
		t.Fatalf("audit actor = %v, want none", *recorded.ActorUserID)
	}
	if recorded.Metadata["username"] != "locked-out" || recorded.Metadata["via"] != "cli" {
		t.Fatalf("audit metadata = %v", recorded.Metadata)
	}
	// The chain must still verify: an unaudited or chain-breaking break-glass is
	// the worst possible outcome for the one event an incident review looks for.
	verdict, err := harness.audit.Verify(context.Background())
	if err != nil || !verdict.Intact {
		t.Fatalf("audit chain after break-glass = %+v, err %v", verdict, err)
	}
}

func TestResetSecondFactorOnAnUnenrolledAccountSaysSo(t *testing.T) {
	harness := newUsersHarness(t)
	person := harness.seedUser(t, "never-enrolled", "viewer")
	harness.seedSession(t, person.ID)

	result, err := ResetSecondFactor(context.Background(), harness.module.database, harness.audit,
		"never-enrolled", time.Unix(1_700_001_000, 0).UTC())
	if err != nil {
		t.Fatalf("reset: %v", err)
	}
	if result.WasEnrolled {
		t.Fatal("an account with no confirmed factor reported WasEnrolled true")
	}
	if result.SessionsRevoked != 1 {
		t.Fatalf("sessions revoked = %d, want 1", result.SessionsRevoked)
	}
}

func TestResetSecondFactorRejectsAnUnknownAccount(t *testing.T) {
	harness := newUsersHarness(t)
	harness.seedUser(t, "present", "admin")

	_, err := ResetSecondFactor(context.Background(), harness.module.database, harness.audit,
		"absent", time.Unix(1_700_001_000, 0).UTC())
	if !errors.Is(err, ErrUnknownAccount) {
		t.Fatalf("reset of an unknown account = %v, want ErrUnknownAccount", err)
	}
	// Nothing else may change: a typo must not clear the factor of the account
	// that does exist.
	model := new(userModel)
	if err := harness.module.database.NewSelect().Model(model).
		Column("username").Where("username = ?", "present").Scan(context.Background()); err != nil {
		t.Fatalf("read account: %v", err)
	}
}

func TestResetSecondFactorRequiresAUsername(t *testing.T) {
	harness := newUsersHarness(t)
	if _, err := ResetSecondFactor(context.Background(), harness.module.database, harness.audit,
		"   ", time.Unix(1_700_001_000, 0).UTC()); err == nil {
		t.Fatal("a blank username was accepted")
	}
}

// containsJSONField asserts on a field of a response body while still proving
// the body is JSON, so a substring match can never pass against an error page.
func containsJSONField(body, field string) bool {
	return json.Valid([]byte(body)) && strings.Contains(body, field)
}
