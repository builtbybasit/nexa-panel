package agentauth

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpenOrCreatePersistsPrivateToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run", "agent.token")
	first, err := OpenOrCreate(path)
	if err != nil {
		t.Fatalf("OpenOrCreate returned an error: %v", err)
	}
	second, err := OpenOrCreate(path)
	if err != nil {
		t.Fatalf("second OpenOrCreate returned an error: %v", err)
	}
	if first != second || len(first) != 64 {
		t.Fatalf("tokens differ or have invalid length: %q %q", first, second)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("chmod token: %v", err)
	}
	if _, err := Read(path); err == nil {
		t.Fatal("world-readable token should be rejected")
	}
}

func TestReadRejectsSymlinkedToken(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "real.token")
	if _, err := OpenOrCreate(target); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(directory, "agent.token")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(link); err == nil {
		t.Fatal("symlinked token should be rejected")
	}
}
