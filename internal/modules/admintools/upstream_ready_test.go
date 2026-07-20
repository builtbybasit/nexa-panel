package admintools

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestWaitForUpstreamReadyReturnsOnceUpstreamServesHTTP(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	port := server.Listener.Addr().(*net.TCPAddr).Port
	if err := waitForUpstreamReady(context.Background(), port, time.Second); err != nil {
		t.Fatalf("ready upstream rejected: %v", err)
	}
}

// phpMyAdmin answers a cookieless GET / with a 302 to its SignonURL that loops
// endlessly. The probe must treat the first response as ready rather than chase
// the redirect chain into an error, which would surface as a spurious 502.
func TestWaitForUpstreamReadyTreatsRedirectLoopAsReady(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", "/signon-failed")
		w.WriteHeader(http.StatusFound)
	}))
	defer server.Close()
	port := server.Listener.Addr().(*net.TCPAddr).Port
	if err := waitForUpstreamReady(context.Background(), port, time.Second); err != nil {
		t.Fatalf("redirecting upstream must be ready, got: %v", err)
	}
}

// A bare TCP listener that accepts connections but never speaks HTTP must not
// be treated as ready — this is exactly the Podman port-forwarder race the
// readiness probe exists to defend against.
func TestWaitForUpstreamReadyRejectsBareTCPListener(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	if err := waitForUpstreamReady(context.Background(), port, 300*time.Millisecond); err == nil {
		t.Fatal("expected timeout for listener that never serves HTTP")
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
