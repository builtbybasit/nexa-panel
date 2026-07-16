package postgres

import (
	"strconv"

	"encoding/hex"

	"errors"
	"fmt"
	"os"

	"io"

	"os/user"
	"strings"

	"crypto/sha256"

	"path/filepath"
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
