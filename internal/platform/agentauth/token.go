package agentauth

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const tokenBytes = 32

func OpenOrCreate(path string) (string, error) {
	if token, err := Read(path); err == nil {
		return token, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return "", fmt.Errorf("create agent token directory: %w", err)
	}
	random := make([]byte, tokenBytes)
	if _, err := rand.Read(random); err != nil {
		return "", fmt.Errorf("generate agent token: %w", err)
	}
	token := hex.EncodeToString(random)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o640)
	if errors.Is(err, os.ErrExist) {
		return Read(path)
	}
	if err != nil {
		return "", fmt.Errorf("create agent token: %w", err)
	}
	if _, err := file.WriteString(token + "\n"); err != nil {
		_ = file.Close()
		return "", fmt.Errorf("write agent token: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return "", fmt.Errorf("sync agent token: %w", err)
	}
	if err := file.Close(); err != nil {
		return "", fmt.Errorf("close agent token: %w", err)
	}
	return token, nil
}

func Read(path string) (string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("inspect agent token: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o007 != 0 {
		return "", errors.New("agent token must be a regular file inaccessible to other users")
	}
	token := strings.TrimSpace(string(content))
	decoded, err := hex.DecodeString(token)
	if err != nil || len(decoded) != tokenBytes {
		return "", errors.New("agent token is invalid")
	}
	return token, nil
}
