package postgres

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
)

func preparePostgresDirectory(root, path string) error {
	root, path = filepath.Clean(root), filepath.Clean(path)
	if path != root && !strings.HasPrefix(path, root+string(filepath.Separator)) {
		return errors.New("PostgreSQL backup directory is outside the managed root")
	}
	if err := os.MkdirAll(path, 0o750); err != nil {
		return fmt.Errorf("create PostgreSQL backup directory: %w", err)
	}
	account, err := user.Lookup("postgres")
	if err != nil {
		return errors.New("postgres operating-system account is unavailable")
	}
	uid, uidErr := strconv.Atoi(account.Uid)
	gid, gidErr := strconv.Atoi(account.Gid)
	if uidErr != nil || gidErr != nil {
		return errors.New("postgres operating-system account identifiers are invalid")
	}
	for current := path; ; current = filepath.Dir(current) {
		if err := os.Chown(current, uid, gid); err != nil {
			return fmt.Errorf("assign PostgreSQL backup directory: %w", err)
		}
		if err := os.Chmod(current, 0o750); err != nil {
			return fmt.Errorf("secure PostgreSQL backup directory: %w", err)
		}
		if current == root {
			break
		}
	}
	return nil
}

// writePgHba atomically replaces pg_hba.conf, preserving the postgres:postgres
// ownership and 0640 mode PostgreSQL requires. The rename makes the swap atomic
// so a concurrent reload never observes a partial file.
func writePgHba(path, content string) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".pg_hba.conf.tmp-*")
	if err != nil {
		return fmt.Errorf("stage pg_hba update: %w", err)
	}
	temporaryPath := temporary.Name()
	keep := false
	defer func() {
		_ = temporary.Close()
		if !keep {
			_ = os.Remove(temporaryPath)
		}
	}()
	if _, err := temporary.WriteString(content); err != nil {
		return fmt.Errorf("write pg_hba update: %w", err)
	}
	if err := temporary.Chmod(0o640); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if os.Geteuid() == 0 {
		account, err := user.Lookup("postgres")
		if err != nil {
			return errors.New("postgres operating-system account is unavailable")
		}
		uid, uidErr := strconv.Atoi(account.Uid)
		gid, gidErr := strconv.Atoi(account.Gid)
		if uidErr != nil || gidErr != nil {
			return errors.New("postgres operating-system account identifiers are invalid")
		}
		if err := os.Chown(temporaryPath, uid, gid); err != nil {
			return fmt.Errorf("assign pg_hba ownership: %w", err)
		}
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("install pg_hba update: %w", err)
	}
	keep = true
	return nil
}

func syncFile(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Sync()
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open PostgreSQL backup directory for sync: %w", err)
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("sync PostgreSQL backup directory: %w", err)
	}
	return nil
}

func fileDigest(path string) (string, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, fmt.Errorf("read PostgreSQL backup: %w", err)
	}
	defer file.Close()
	digest := sha256.New()
	size, err := io.Copy(digest, file)
	if err != nil {
		return "", 0, fmt.Errorf("hash PostgreSQL backup: %w", err)
	}
	return hex.EncodeToString(digest.Sum(nil)), size, nil
}
