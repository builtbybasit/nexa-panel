package sites

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

var verifySite = Site{Slug: "eample-site", PrimaryDomain: "example.com"}

// welcomePage mimics the stock Debian default_server, which answers 200 for any
// host it does not recognise and carries no X-Nexa-Site header.
func welcomePage(writer http.ResponseWriter, _ *http.Request) {
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write([]byte("<html><title>Welcome to nginx!</title></html>"))
}

func newVerifySystem(t *testing.T, timeout time.Duration, handler http.HandlerFunc) *HostSystem {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	address := strings.TrimPrefix(server.URL, "http://")
	dialer := &net.Dialer{}
	return &HostSystem{
		client: &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, "tcp", address)
		}}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		verifyTimeout:  timeout,
		verifyInterval: 5 * time.Millisecond,
	}
}

func TestVerifyHostAcceptsResponseFromTheSiteServerBlock(t *testing.T) {
	system := newVerifySystem(t, time.Second, func(writer http.ResponseWriter, request *http.Request) {
		if request.Host != verifySite.PrimaryDomain {
			t.Errorf("Host = %q, want %q", request.Host, verifySite.PrimaryDomain)
		}
		writer.Header().Set(SiteHeader, verifySite.Slug)
		_, _ = writer.Write([]byte("Nexa Panel site eample-site is ready on PHP 8.3.6\n"))
	})
	if err := system.VerifyHost(context.Background(), verifySite); err != nil {
		t.Fatalf("VerifyHost() = %v, want nil", err)
	}
}

// A site whose document root already holds a real application has no Nexa
// placeholder in its body. The server block header still proves Nginx routed
// the request correctly, so verification must accept it.
func TestVerifyHostAcceptsApplicationContentWithoutThePlaceholder(t *testing.T) {
	system := newVerifySystem(t, time.Second, func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set(SiteHeader, verifySite.Slug)
		_, _ = writer.Write([]byte("<html><body>Some customer application</body></html>"))
	})
	if err := system.VerifyHost(context.Background(), verifySite); err != nil {
		t.Fatalf("VerifyHost() = %v, want nil", err)
	}
}

// Regression: "systemctl reload nginx" returns before Nginx serves the new
// configuration, so the first probes land on old workers and fall through to
// the default_server. VerifyHost must wait rather than fail the activation.
func TestVerifyHostWaitsForNginxReloadToTakeEffect(t *testing.T) {
	var probes atomic.Int32
	system := newVerifySystem(t, 2*time.Second, func(writer http.ResponseWriter, request *http.Request) {
		if probes.Add(1) <= 3 {
			welcomePage(writer, request)
			return
		}
		writer.Header().Set(SiteHeader, verifySite.Slug)
		_, _ = writer.Write([]byte("Nexa Panel site eample-site is ready on PHP 8.3.6\n"))
	})
	if err := system.VerifyHost(context.Background(), verifySite); err != nil {
		t.Fatalf("VerifyHost() = %v, want nil once the reload took effect", err)
	}
	if got := probes.Load(); got < 4 {
		t.Fatalf("probes = %d, want the probe to have retried past the stale config", got)
	}
}

func TestVerifyHostRejectsTheDefaultServerWelcomePage(t *testing.T) {
	system := newVerifySystem(t, 50*time.Millisecond, welcomePage)
	err := system.VerifyHost(context.Background(), verifySite)
	if err == nil {
		t.Fatal("VerifyHost() = nil, want an error when the default_server answers")
	}
	if !strings.Contains(err.Error(), "different Nginx server block") {
		t.Fatalf("VerifyHost() = %v, want an error naming the wrong server block", err)
	}
}

func TestVerifyHostRejectsServerErrorFromTheSite(t *testing.T) {
	system := newVerifySystem(t, 50*time.Millisecond, func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set(SiteHeader, verifySite.Slug)
		writer.WriteHeader(http.StatusBadGateway)
	})
	err := system.VerifyHost(context.Background(), verifySite)
	if err == nil {
		t.Fatal("VerifyHost() = nil, want an error when PHP-FPM is unreachable")
	}
	if !strings.Contains(err.Error(), "502") {
		t.Fatalf("VerifyHost() = %v, want an error naming status 502", err)
	}
}

func TestVerifyHostStopsWhenTheContextIsCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	system := newVerifySystem(t, time.Minute, func(writer http.ResponseWriter, request *http.Request) {
		cancel()
		welcomePage(writer, request)
	})
	if err := system.VerifyHost(ctx, verifySite); err == nil {
		t.Fatal("VerifyHost() = nil, want an error once the context is cancelled")
	}
}
