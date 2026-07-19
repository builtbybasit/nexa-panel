package admintools

import (
	"context"
	"net"
	"testing"
	"time"
)

func TestWaitForUpstreamReadyReturnsOnceListenerAccepts(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	if err := waitForUpstreamReady(context.Background(), port, time.Second); err != nil {
		t.Fatalf("ready listener rejected: %v", err)
	}
}

func TestWaitForUpstreamReadyTimesOutWhenPortDead(t *testing.T) {
	// Bind and immediately release a port so nothing is listening on it.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	start := time.Now()
	if err := waitForUpstreamReady(context.Background(), port, 300*time.Millisecond); err == nil {
		t.Fatal("expected timeout for dead port")
	}
	if elapsed := time.Since(start); elapsed < 250*time.Millisecond {
		t.Fatalf("returned before honoring the timeout: %s", elapsed)
	}
}

func TestWaitForUpstreamReadyHonorsContextCancellation(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitForUpstreamReady(ctx, port, 10*time.Second); err == nil {
		t.Fatal("expected cancellation error")
	}
}
