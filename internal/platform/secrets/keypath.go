package secrets

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
)

const (
	// DefaultKeyPath keeps the master key in the systemd ConfigurationDirectory,
	// away from the StateDirectory that holds control.db: a stolen state backup
	// or a lifted disk image must not hand over both the encrypted secrets and
	// the key that opens them.
	DefaultKeyPath = "/etc/nexa-panel/master.key"
	// LegacyKeyPath is where installs predating that split wrote the key.
	LegacyKeyPath = "/var/lib/nexa-panel/master.key"
)

// OpenDefaultKeyFile opens the master key at path, first relocating a key that
// an older install left beside control.db. Relocation only applies to the
// default path; an operator who points --master-key somewhere else has already
// chosen where the key lives.
func OpenDefaultKeyFile(path string, logger *slog.Logger) (*Box, error) {
	if filepath.Clean(path) == DefaultKeyPath {
		if err := relocateLegacyKey(DefaultKeyPath, LegacyKeyPath, logger); err != nil {
			return nil, err
		}
	}
	return OpenKeyFile(path)
}

// relocateLegacyKey moves the legacy key onto target before anything opens it.
// Every failure is returned rather than logged: OpenKeyFile generates a fresh
// key for a missing path, and a fresh key silently orphans every secret already
// stored, so a half-finished relocation must abort startup instead.
func relocateLegacyKey(target, legacy string, logger *slog.Logger) error {
	if logger == nil {
		logger = slog.Default()
	}
	legacyInfo, err := os.Lstat(legacy)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect legacy master key: %w", err)
	}
	if _, err := os.Lstat(target); err == nil {
		logger.Warn("a master key remains at the legacy location and is now unused; delete it once the panel is confirmed healthy", "legacy", legacy, "master_key", target)
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect master key: %w", err)
	}
	if !legacyInfo.Mode().IsRegular() {
		return fmt.Errorf("legacy master key %s must be a regular file", legacy)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
		return fmt.Errorf("create master key directory: %w", err)
	}

	if err := os.Rename(legacy, target); err != nil {
		// A rename cannot cross filesystems, and /etc and /var/lib are separate
		// mounts on plenty of hosts.
		if err := copyLegacyKey(target, legacy); err != nil {
			return err
		}
	}
	logger.Warn("relocated the master key out of the panel state directory; back it up separately from control.db, not alongside it", "from", legacy, "to", target)
	return nil
}

// copyLegacyKey is the cross-device fallback for the relocation. It only removes
// the source after the destination has been fsynced and read back byte for byte,
// and it tears the destination down again if the source cannot be removed, so
// the key is never left readable in two places.
func copyLegacyKey(target, legacy string) error {
	key, err := readKey(legacy)
	if err != nil {
		return fmt.Errorf("read legacy master key: %w", err)
	}
	file, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create relocated master key: %w", err)
	}
	if _, err := file.Write(key); err != nil {
		_ = file.Close()
		_ = os.Remove(target)
		return fmt.Errorf("write relocated master key: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		_ = os.Remove(target)
		return fmt.Errorf("sync relocated master key: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(target)
		return fmt.Errorf("close relocated master key: %w", err)
	}
	written, err := readKey(target)
	if err != nil {
		_ = os.Remove(target)
		return fmt.Errorf("verify relocated master key: %w", err)
	}
	if !bytes.Equal(written, key) {
		_ = os.Remove(target)
		return fmt.Errorf("relocated master key %s does not match the legacy key", target)
	}
	if err := syncDirectory(filepath.Dir(target)); err != nil {
		_ = os.Remove(target)
		return fmt.Errorf("persist relocated master key directory entry: %w", err)
	}
	if err := os.Remove(legacy); err != nil {
		_ = os.Remove(target)
		return fmt.Errorf("remove legacy master key %s after copying it to %s (move it by hand and restart): %w", legacy, target, err)
	}
	if err := syncDirectory(filepath.Dir(legacy)); err != nil {
		return fmt.Errorf("persist legacy master key removal: %w", err)
	}
	return nil
}
