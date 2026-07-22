package identity

import (
	_ "embed"
	"strings"
	"sync"
)

// commonPasswordList is the embedded denylist: lowercase, sorted, one entry per
// line. It is generated offline (the panel may be air-gapped, so nothing is ever
// fetched at runtime) from the weak bases people decorate — dictionary words,
// keyboard walks, famous passphrases — crossed with the year/digit/symbol
// suffixes and leet substitutions that let a weak base satisfy the length and
// character-class rules. Entries below the minimum length are omitted because
// the length check rejects them first.
//
//go:embed common_passwords.txt
var commonPasswordList string

var (
	commonPasswordsOnce sync.Once
	commonPasswords     map[string]struct{}
)

// passwordIsCommon reports whether the password is on the denylist. The match is
// a plain lowercase exact comparison against a set built once: anything cleverer
// (stripping suffixes, edit distance) costs latency on every password change for
// coverage the generated decorations already provide.
func passwordIsCommon(password string) bool {
	commonPasswordsOnce.Do(func() {
		entries := strings.Split(commonPasswordList, "\n")
		commonPasswords = make(map[string]struct{}, len(entries))
		for _, entry := range entries {
			if trimmed := strings.TrimSpace(entry); trimmed != "" {
				commonPasswords[strings.ToLower(trimmed)] = struct{}{}
			}
		}
	})
	_, found := commonPasswords[strings.ToLower(password)]
	return found
}

// PasswordPolicy is the set-time password policy, published so the sign-up and
// password-change forms can render the same requirements the server enforces. It
// exposes only that a denylist is applied, never its contents.
type PasswordPolicy struct {
	MinLength         int  `json:"minLength"`
	MaxLength         int  `json:"maxLength"`
	RequiredClasses   int  `json:"requiredClasses"`
	ClassExemptLength int  `json:"classExemptLength"`
	DenylistApplied   bool `json:"denylistApplied"`
	RejectsUsername   bool `json:"rejectsUsername"`
}

func passwordPolicy() PasswordPolicy {
	return PasswordPolicy{
		MinLength: minimumPasswordLength, MaxLength: maximumPasswordLength,
		RequiredClasses: requiredPasswordClasses, ClassExemptLength: passphraseLength,
		DenylistApplied: true, RejectsUsername: true,
	}
}
