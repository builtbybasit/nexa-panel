package unixsocket

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// socketTempRoot is a short parent directory for Unix sockets created by tests.
// A socket path is capped near 104 bytes by sun_path, and on macOS t.TempDir()
// returns a long /var/folders/... path that overflows it. This used to be
// /private/tmp, which is short but exists only on macOS: every one of these
// tests failed on Linux, so CI had never once passed. /tmp is short and present
// on both.
const socketTempRoot = "/tmp"

func TestListenRejectsUnsafeTargets(t *testing.T) {
	if _, _, err := Listen("relative.sock", 0o750, 0o660); err == nil {
		t.Fatal("relative path should be rejected")
	}
	if _, _, err := Listen(filepath.Join(t.TempDir(), "api.sock"), 0, 0o660); err == nil {
		t.Fatal("missing directory mode should be rejected")
	}
	regular := filepath.Join(t.TempDir(), "api.sock")
	if err := os.WriteFile(regular, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Listen(regular, 0o750, 0o660); err == nil {
		t.Fatal("regular file at socket path should be rejected")
	}
	content, err := os.ReadFile(regular)
	if err != nil || string(content) != "keep" {
		t.Fatalf("regular file was changed: %q, %v", content, err)
	}
}

func TestListenAcceptsConnectionsAndCleansUp(t *testing.T) {
	path := filepath.Join(shortTempDir(t), "nested", "api.sock")
	listener, cleanup, err := Listen(path, 0o750, 0o660)
	if err != nil {
		t.Fatal(err)
	}
	if unixListener, ok := listener.(*net.UnixListener); ok {
		_ = unixListener.SetDeadline(time.Now().Add(time.Second))
	}
	dialed := make(chan error, 1)
	go func() {
		connection, dialErr := net.Dial("unix", path)
		if dialErr == nil {
			_ = connection.Close()
		}
		dialed <- dialErr
	}()
	connection, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	_ = connection.Close()
	if err := <-dialed; err != nil {
		t.Fatal(err)
	}
	cleanup()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("socket path still exists after cleanup: %v", err)
	}
}

func TestCleanupDoesNotRemoveReplacementPath(t *testing.T) {
	path := filepath.Join(shortTempDir(t), "api.sock")
	listener, cleanup, err := Listen(path, 0o750, 0o660)
	if err != nil {
		t.Fatal(err)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	cleanup()
	content, err := os.ReadFile(path)
	if err != nil || string(content) != "replacement" {
		t.Fatalf("replacement path was removed: %q, %v", content, err)
	}
}

func shortTempDir(t *testing.T) string {
	t.Helper()
	directory, err := os.MkdirTemp(socketTempRoot, "nexa-sock-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	return directory
}
