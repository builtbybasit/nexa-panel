package postgres

import (
	"errors"
	"strings"

	"github.com/nexa-panel/nexa-panel/internal/platform/secureid"
)

func randomResourceID(prefix string) string { return prefix + "_" + randomToken(12) }

func randomToken(size int) string { return secureid.Hex(size) }

func friendlyUnique(err error, message string) error {
	if strings.Contains(strings.ToLower(err.Error()), "unique") {
		return errors.New(message)
	}
	return err
}
