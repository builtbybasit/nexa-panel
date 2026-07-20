package php

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/nexa-panel/nexa-panel/internal/platform/secureid"
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

// aptCommand builds a non-interactive apt-safe invocation.
func aptCommand(name string, args ...string) Command {
	return Command{Name: name, Args: args, Env: []string{"DEBIAN_FRONTEND=noninteractive"}}
}

// phpBinary is the CLI interpreter for a branch; extensionPackage is one apt
// module package. Both interpolate only a version already matched by
// versionPattern and confirmed installed, or a suffix matched by
// extensionPattern.
func phpBinary(version string) string { return "php" + version }

func extensionPackage(version, name string) string { return "php" + version + "-" + name }

func fpmUnit(version string) string { return "php" + version + "-fpm" }

// fpmConfigDir, fpmMainIni, and managedDropInPath locate a branch's FPM config
// under the operator's config root, so tests can point the root at a temp tree.
func (o *HostOperator) fpmConfigDir(version string) string {
	return filepath.Join(o.configRoot, version, "fpm", "conf.d")
}

func (o *HostOperator) fpmMainIni(version string) string {
	return filepath.Join(o.configRoot, version, "fpm", "php.ini")
}

func (o *HostOperator) managedDropInPath(version string) string {
	return filepath.Join(o.fpmConfigDir(version), managedDropInName)
}

// accessLabel renders PHP's ini access bitmask (1 USER, 2 PERDIR, 4 SYSTEM) as a
// stable string. PHP_INI_ALL is the union; anything else names the widest scope.
func accessLabel(mask int) string {
	switch {
	case mask&7 == 7:
		return "PHP_INI_ALL"
	case mask&4 != 0:
		return "PHP_INI_SYSTEM"
	case mask&2 != 0:
		return "PHP_INI_PERDIR"
	case mask&1 != 0:
		return "PHP_INI_USER"
	default:
		return "PHP_INI_NONE"
	}
}
