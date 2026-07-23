package identity

import (
	"crypto/sha256"
	"errors"
	"strings"
	"unicode"

	"golang.org/x/crypto/argon2"
)

const (
	minimumPasswordLength = 12
	maximumPasswordLength = 1024
	// requiredPasswordClasses is how many of {lowercase, uppercase, digit,
	// symbol} a short password must mix.
	requiredPasswordClasses = 3
	// passphraseLength is where the class requirement is dropped. NIST SP 800-63B
	// discourages composition rules precisely because they push people toward
	// predictable decoration; past this length raw entropy dominates, so a long
	// all-lowercase passphrase is deliberately accepted.
	passphraseLength = 20
	// minimumUsernameFragment keeps the username containment check from rejecting
	// a password merely because a two-letter account name appears somewhere in it.
	minimumUsernameFragment = 3
)

func validateCredentials(input credentials) error {
	if !usernamePattern.MatchString(input.Username) {
		return errors.New("Username must be 3-64 characters and use only letters, numbers, dot, underscore, or hyphen.")
	}
	return validatePassword(input.Password, input.Username)
}

// validatePassword enforces the policy at set/change time only. It is never
// reached from the sign-in path, so tightening it can never lock out an account
// whose existing password no longer satisfies it.
func validatePassword(password, username string) error {
	if len(password) < minimumPasswordLength || len(password) > maximumPasswordLength {
		return errors.New("Password must be between 12 and 1024 characters.")
	}
	if len(password) < passphraseLength && passwordClasses(password) < requiredPasswordClasses {
		return errors.New("Password must combine at least three of lowercase letters, uppercase letters, digits, and symbols, or be at least 20 characters long.")
	}
	if passwordIsCommon(password) {
		return errors.New("Password appears in a list of commonly used and breached passwords. Choose something less predictable.")
	}
	if passwordContainsUsername(password, username) {
		return errors.New("Password must not contain your username.")
	}
	return nil
}

func passwordClasses(password string) int {
	var lower, upper, digit, symbol bool
	for _, character := range password {
		switch {
		case unicode.IsLower(character):
			lower = true
		case unicode.IsUpper(character):
			upper = true
		case unicode.IsDigit(character):
			digit = true
		default:
			symbol = true
		}
	}
	classes := 0
	for _, present := range []bool{lower, upper, digit, symbol} {
		if present {
			classes++
		}
	}
	return classes
}

// passwordContainsUsername rejects the pair in both directions: "admin" must not
// yield "admin-2026-panel", and an account named "correcthorsebattery" must not
// be able to shorten its own name into a password.
func passwordContainsUsername(password, username string) bool {
	name := strings.ToLower(strings.TrimSpace(username))
	if len(name) < minimumUsernameFragment {
		return false
	}
	secret := strings.ToLower(password)
	return strings.Contains(secret, name) || strings.Contains(name, secret)
}

func argon2Dummy(password string, parameters passwordParameters) byte {
	key := sha256.Sum256([]byte("nexa-panel-unknown-user"))
	result := argon2IDKey([]byte(password), key[:16], parameters)
	return result[0]
}

func argon2IDKey(password, salt []byte, parameters passwordParameters) []byte {
	return argon2.IDKey(password, salt, parameters.iterations, parameters.memory, parameters.parallelism, 32)
}
