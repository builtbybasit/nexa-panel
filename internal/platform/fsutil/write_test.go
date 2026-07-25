package fsutil

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteCreatesFileWithMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config")
	if err := Write(path, []byte("hello"), 0o640); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "hello" {
		t.Fatalf("content = %q, want %q", got, "hello")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o640 {
		t.Fatalf("mode = %o, want 640", perm)
	}
}

func TestWriteReplacesAtomically(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := Write(path, []byte("new"), 0o644); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "new" {
		t.Fatalf("content = %q, want new", got)
	}
	// No stray temp files left behind.
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("directory has %d entries %v, want 1", len(entries), names)
	}
}

func TestWriteWithMkdirAll(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "deep", "config")
	if err := Write(path, []byte("x"), 0o600, WithMkdirAll(0o755)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("Stat: %v", err)
	}
}

func TestWriteWithoutMkdirFailsOnMissingParent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "missing", "config")
	if err := Write(path, []byte("x"), 0o600); err == nil {
		t.Fatal("expected error writing into a missing parent directory")
	}
}
