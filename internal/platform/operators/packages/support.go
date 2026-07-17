package packages

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

// outputLimit caps captured command output so a runaway apt log cannot bloat
// job results or audit records.
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

func randomID() string {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		panic(err)
	}
	return hex.EncodeToString(value)
}

func commandError(action string, output []byte, err error) error {
	message := strings.TrimSpace(string(output))
	// apt/dpkg/add-apt-repository failures (e.g. a Python traceback from a PPA
	// handler) carry the real cause in their tail; keep enough to diagnose it.
	if len(message) > 2000 {
		message = message[len(message)-2000:]
	}
	if message == "" {
		return fmt.Errorf("%s: %w", action, err)
	}
	return fmt.Errorf("%s: %w: %s", action, err, message)
}
