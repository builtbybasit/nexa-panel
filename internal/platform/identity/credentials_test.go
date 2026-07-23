package identity

import (
	"strings"
	"testing"
)

func TestValidatePasswordPolicy(t *testing.T) {
	cases := []struct {
		name     string
		password string
		username string
		rejected string
	}{
		{name: "too short", password: "Sh0rt!", username: "admin", rejected: "between 12 and 1024"},
		{name: "too long", password: strings.Repeat("aB3$", 300), username: "admin", rejected: "between 12 and 1024"},
		{name: "two classes", password: "lowercase-only", username: "admin", rejected: "at least three"},
		{name: "three classes", password: "lowercase-0nly", username: "admin"},
		{name: "four classes", password: "a-Str0ng-password", username: "admin"},
		// Length is allowed to stand in for composition: a 20-character
		// all-lowercase passphrase is deliberately accepted.
		{name: "long passphrase exempt from classes", password: "waterfall pine anchor", username: "admin"},
		{name: "long passphrase still denylisted", password: "correcthorsebatterystaple", username: "admin", rejected: "commonly used"},
		{name: "denylisted with mixed case", password: "Password123!", username: "admin", rejected: "commonly used"},
		{name: "contains username", password: "the-admin-Str0ng", username: "admin", rejected: "must not contain your username"},
		{name: "contained by username", password: "reallylongaccountname", username: "reallylongaccountname.ops", rejected: "must not contain your username"},
		// A username short enough to appear inside an unrelated password must not
		// disqualify it.
		{name: "short username fragment ignored", password: "ab-Str0ng-password", username: "ab"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			err := validatePassword(testCase.password, testCase.username)
			if testCase.rejected == "" {
				if err != nil {
					t.Fatalf("validatePassword(%q) = %v, want accepted", testCase.password, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("validatePassword(%q) accepted, want rejection mentioning %q", testCase.password, testCase.rejected)
			}
			if !strings.Contains(err.Error(), testCase.rejected) {
				t.Fatalf("validatePassword(%q) = %q, want it to mention %q", testCase.password, err, testCase.rejected)
			}
			if !strings.HasPrefix(err.Error(), "Password ") {
				t.Fatalf("validatePassword(%q) = %q, want the user-facing sentence style", testCase.password, err)
			}
		})
	}
}

func TestValidateCredentialsPassesUsernameToThePasswordPolicy(t *testing.T) {
	if err := validateCredentials(credentials{Username: "webmaster", Password: "webmaster-1X!"}); err == nil {
		t.Fatal("validateCredentials accepted a password containing the username")
	}
	if err := validateCredentials(credentials{Username: "webmaster", Password: "a-Str0ng-password"}); err != nil {
		t.Fatalf("validateCredentials rejected a compliant pair: %v", err)
	}
}

func TestCommonPasswordDenylistIsCaseInsensitiveAndLoadedOnce(t *testing.T) {
	if !passwordIsCommon("PaSsWoRd123!") {
		t.Fatal("denylist did not match a common password case-insensitively")
	}
	if passwordIsCommon("waterfall pine anchor") {
		t.Fatal("denylist matched a password it should not contain")
	}
	if len(commonPasswords) < 1000 {
		t.Fatalf("denylist entry count = %d, want at least 1000", len(commonPasswords))
	}
}

func TestPasswordPolicyDescribesTheEnforcedRules(t *testing.T) {
	policy := passwordPolicy()
	if policy.MinLength != minimumPasswordLength || policy.MaxLength != maximumPasswordLength {
		t.Fatalf("policy lengths = %d..%d, want %d..%d", policy.MinLength, policy.MaxLength, minimumPasswordLength, maximumPasswordLength)
	}
	if policy.RequiredClasses != requiredPasswordClasses || policy.ClassExemptLength != passphraseLength {
		t.Fatalf("policy classes = %+v, want %d classes below %d characters", policy, requiredPasswordClasses, passphraseLength)
	}
	if !policy.DenylistApplied || !policy.RejectsUsername {
		t.Fatalf("policy = %+v, want the denylist and username rules advertised", policy)
	}
}
