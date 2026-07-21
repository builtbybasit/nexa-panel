package services

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/nexa-panel/nexa-panel/internal/platform/secureid"
)

// outputLimit caps captured command output so a runaway systemctl log cannot
// bloat job results or audit records.
const outputLimit = 64 * 1024

func capped(output []byte, err error) ([]byte, error) {
	if len(output) > outputLimit {
		output = output[:outputLimit]
	}
	return output, err
}

func fingerprint(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func randomID() string { return secureid.Hex(16) }

func commandError(action string, output []byte, err error) error {
	message := strings.TrimSpace(string(output))
	if len(message) > 2000 {
		message = message[len(message)-2000:]
	}
	if message == "" {
		return fmt.Errorf("%s: %w", action, err)
	}
	return fmt.Errorf("%s: %w: %s", action, err, message)
}

// displayName strips the ".service" suffix so the UI shows "nginx" rather than
// "nginx.service"; templated units keep their instance (e.g.
// "postgresql@16-main").
func displayName(unit string) string {
	return strings.TrimSuffix(unit, ".service")
}
