package secrets

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBoxEncryptsWithLabelAuthentication(t *testing.T) {
	box, err := New(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatalf("New returned an error: %v", err)
	}
	encoded, err := box.Encrypt("identity.totp", []byte("shared-secret"))
	if err != nil {
		t.Fatalf("Encrypt returned an error: %v", err)
	}
	plaintext, err := box.Decrypt("identity.totp", encoded)
	if err != nil || string(plaintext) != "shared-secret" {
		t.Fatalf("Decrypt = %q, %v", plaintext, err)
	}
	if _, err := box.Decrypt("another-label", encoded); err == nil {
		t.Fatal("Decrypt with another label should fail")
	}
}

func TestOpenKeyFileCreatesAndReusesPrivateKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "master.key")
	first, err := OpenKeyFile(path)
	if err != nil {
		t.Fatalf("first OpenKeyFile returned an error: %v", err)
	}
	encoded, err := first.Encrypt("test", []byte("value"))
	if err != nil {
		t.Fatalf("Encrypt returned an error: %v", err)
	}
	second, err := OpenKeyFile(path)
	if err != nil {
		t.Fatalf("second OpenKeyFile returned an error: %v", err)
	}
	plaintext, err := second.Decrypt("test", encoded)
	if err != nil || string(plaintext) != "value" {
		t.Fatalf("reopened Decrypt = %q, %v", plaintext, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat key: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("key permissions = %o, want 600", info.Mode().Perm())
	}
}

func TestOpenKeyFileRejectsSymlinkedKey(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "real.key")
	if _, err := OpenKeyFile(target); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(directory, "master.key")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenKeyFile(link); err == nil {
		t.Fatal("symlinked master key should be rejected")
	}
}

func TestRelocateLegacyKeyMovesTheKeyAndWarns(t *testing.T) {
	directory := t.TempDir()
	legacy := filepath.Join(directory, "state", "master.key")
	target := filepath.Join(directory, "config", "master.key")
	if _, err := OpenKeyFile(legacy); err != nil {
		t.Fatalf("seed legacy key: %v", err)
	}
	original, err := os.ReadFile(legacy)
	if err != nil {
		t.Fatal(err)
	}

	logs := &bytes.Buffer{}
	logger := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelWarn}))
	if err := relocateLegacyKey(target, legacy, logger); err != nil {
		t.Fatalf("relocateLegacyKey returned an error: %v", err)
	}

	relocated, err := os.ReadFile(target)
	if err != nil || !bytes.Equal(relocated, original) {
		t.Fatalf("relocated key = %v, %v", relocated, err)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("relocated key permissions = %o, want 600", info.Mode().Perm())
	}
	if _, err := os.Lstat(legacy); !os.IsNotExist(err) {
		t.Fatalf("legacy key still present: %v", err)
	}
	if !strings.Contains(logs.String(), "level=WARN") {
		t.Fatalf("relocation logs = %q, want a warning", logs.String())
	}
}

func TestRelocateLegacyKeyKeepsAnExistingKey(t *testing.T) {
	directory := t.TempDir()
	legacy := filepath.Join(directory, "state", "master.key")
	target := filepath.Join(directory, "config", "master.key")
	if _, err := OpenKeyFile(legacy); err != nil {
		t.Fatalf("seed legacy key: %v", err)
	}
	if _, err := OpenKeyFile(target); err != nil {
		t.Fatalf("seed current key: %v", err)
	}
	current, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}

	if err := relocateLegacyKey(target, legacy, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))); err != nil {
		t.Fatalf("relocateLegacyKey returned an error: %v", err)
	}
	after, err := os.ReadFile(target)
	if err != nil || !bytes.Equal(after, current) {
		t.Fatal("an existing master key must never be replaced by the legacy one")
	}
}

func TestRelocateLegacyKeyFailsInsteadOfLeavingTheKeyBehind(t *testing.T) {
	directory := t.TempDir()
	legacy := filepath.Join(directory, "state", "master.key")
	target := filepath.Join(directory, "config", "master.key")
	if _, err := OpenKeyFile(legacy); err != nil {
		t.Fatalf("seed legacy key: %v", err)
	}
	// The destination directory cannot be created because a regular file sits
	// where it belongs; startup must fail loudly rather than fall through to
	// OpenKeyFile, which would mint a key that decrypts nothing.
	if err := os.WriteFile(filepath.Dir(target), []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := relocateLegacyKey(target, legacy, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))); err == nil {
		t.Fatal("relocateLegacyKey should fail when the legacy key cannot be moved")
	}
	if _, err := os.Lstat(legacy); err != nil {
		t.Fatalf("failed relocation must leave the legacy key in place: %v", err)
	}
}

func TestCopyLegacyKeyVerifiesBeforeRemovingTheSource(t *testing.T) {
	directory := t.TempDir()
	legacy := filepath.Join(directory, "state", "master.key")
	target := filepath.Join(directory, "config", "master.key")
	if _, err := OpenKeyFile(legacy); err != nil {
		t.Fatalf("seed legacy key: %v", err)
	}
	original, err := os.ReadFile(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
		t.Fatal(err)
	}

	if err := copyLegacyKey(target, legacy); err != nil {
		t.Fatalf("copyLegacyKey returned an error: %v", err)
	}
	copied, err := os.ReadFile(target)
	if err != nil || !bytes.Equal(copied, original) {
		t.Fatalf("copied key = %v, %v", copied, err)
	}
	if _, err := os.Lstat(legacy); !os.IsNotExist(err) {
		t.Fatalf("legacy key still present: %v", err)
	}
}
