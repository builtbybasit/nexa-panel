package mysql

import (
	"crypto/rand"
	"errors"

	"encoding/hex"
	"strings"
)

func randomResourceID(prefix string) string { return prefix + "_" + randomToken(12) }

func randomToken(size int) string {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		panic(err)
	}
	return hex.EncodeToString(value)
}

func friendlyUnique(err error, message string) error {
	if strings.Contains(strings.ToLower(err.Error()), "unique") {
		return errors.New(message)
	}
	return err
}
