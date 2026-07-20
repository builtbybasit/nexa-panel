package main

import (
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestOpenAPIUnixSocketRejectsUnsafeTargets(t *testing.T) {
	if _, _, err := openAPIUnixSocket("relative.sock"); err == nil {
		t.Fatal("relative socket path should be rejected")
	}
	regular := filepath.Join(t.TempDir(), "api.sock")
	if err := os.WriteFile(regular, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := openAPIUnixSocket(regular); err == nil {
		t.Fatal("regular file at socket path should be rejected")
	}
	content, err := os.ReadFile(regular)
	if err != nil || string(content) != "keep" {
		t.Fatalf("regular file was changed: %q, %v", content, err)
	}
}

func TestOpenAPIUnixSocketAcceptsConnectionsAndCleansUp(t *testing.T) {
	directory, err := os.MkdirTemp("/private/tmp", "nexa-api-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	path := filepath.Join(directory, "api.sock")
	listener, cleanup, err := openAPIUnixSocket(path)
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
