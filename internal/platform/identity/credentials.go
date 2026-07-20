package identity

import (
	"crypto/sha256"
	"errors"

	"golang.org/x/crypto/argon2"
)

func validateCredentials(input credentials) error {
	if !usernamePattern.MatchString(input.Username) {
		return errors.New("Username must be 3-64 characters and use only letters, numbers, dot, underscore, or hyphen.")
	}
	return validatePassword(input.Password)
}

func validatePassword(password string) error {
	if len(password) < 12 || len(password) > 1024 {
		return errors.New("Password must be between 12 and 1024 characters.")
	}
	return nil
}

func argon2Dummy(password string, parameters passwordParameters) byte {
	key := sha256.Sum256([]byte("nexa-panel-unknown-user"))
	result := argon2IDKey([]byte(password), key[:16], parameters)
	return result[0]
}

func argon2IDKey(password, salt []byte, parameters passwordParameters) []byte {
	return argon2.IDKey(password, salt, parameters.iterations, parameters.memory, parameters.parallelism, 32)
}
