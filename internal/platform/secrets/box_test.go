package secrets

import (
	"bytes"
	"os"
	"path/filepath"
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
