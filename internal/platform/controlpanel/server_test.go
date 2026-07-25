package controlpanel

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nexa-panel/nexa-panel/internal/platform/httpapi"
	"github.com/nexa-panel/nexa-panel/internal/platform/module"
)

func TestLiveEndpoint(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server, err := New("test", nil, logger)
	if err != nil {
		t.Fatalf("New returned an error: %v", err)
	}

	request := httptest.NewRequest(http.MethodGet, "/api/v1/health/live", nil)
	request.RemoteAddr = "127.0.0.1:12345"
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.Code)
	}
	if got := response.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q, want nosniff", got)
	}
	if got := response.Header().Get("X-Request-ID"); len(got) != 32 {
		t.Fatalf("X-Request-ID length = %d, want 32", len(got))
	}
}

func TestRequestIDOnlyAcceptsTrustedProxyValue(t *testing.T) {
	server, err := New("test", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	const forwarded = "0123456789abcdef0123456789abcdef"

	untrusted := httptest.NewRequest(http.MethodGet, "/api/v1/health/live", nil)
	untrusted.RemoteAddr = "127.0.0.1:12345"
	untrusted.Header.Set("X-Request-ID", forwarded)
	untrustedResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(untrustedResponse, untrusted)
	if got := untrustedResponse.Header().Get("X-Request-ID"); got == forwarded {
		t.Fatal("a direct client was allowed to choose the request ID")
	}

	trusted := httptest.NewRequest(http.MethodGet, "/api/v1/health/live", nil)
	trusted.RemoteAddr = "127.0.0.1:12345"
	trusted.Header.Set("X-Request-ID", forwarded)
	trusted = trusted.WithContext(httpapi.WithTrustedProxy(trusted.Context()))
	trustedResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(trustedResponse, trusted)
	if got := trustedResponse.Header().Get("X-Request-ID"); got != forwarded {
		t.Fatalf("trusted request ID = %q, want %q", got, forwarded)
	}
}

func TestReadinessReportsDependencyFailure(t *testing.T) {
	server, err := New("test", nil, nil, WithReadiness(func(context.Context) error {
		return errors.New("agent socket unavailable")
	}))
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/health/ready", nil)
	request.RemoteAddr = "127.0.0.1:12345"
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("readiness status = %d, want 503", response.Code)
	}
	if strings.Contains(response.Body.String(), "agent socket unavailable") {
		t.Fatal("readiness response leaked an internal dependency error")
	}
}

func TestMetricsExposeBoundedProcessCounters(t *testing.T) {
	server, err := New("test", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	request.RemoteAddr = "127.0.0.1:12345"
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("metrics status = %d", response.Code)
	}
	for _, metric := range []string{"nexa_build_info", "nexa_http_requests_total", "nexa_http_requests_in_flight", "nexa_http_panics_total"} {
		if !strings.Contains(response.Body.String(), metric) {
			t.Fatalf("metrics response missing %s: %s", metric, response.Body.String())
		}
	}
}

func TestMetricsRejectsUntrustedCaller(t *testing.T) {
	server, err := New("test", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Use HTTPS so the request clears requireSecureTransport and actually
	// reaches the localOnly gate rather than being turned away at 426.
	request := httptest.NewRequest(http.MethodGet, "https://panel.example/metrics", nil)
	// A non-loopback client (e.g. an external caller forwarded past nginx) is
	// off the local surface and must not see the metrics — nor confirm the route.
	request.RemoteAddr = "203.0.113.5:54321"
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("untrusted metrics status = %d, want 404", response.Code)
	}
	if strings.Contains(response.Body.String(), "nexa_") {
		t.Fatalf("untrusted caller received metrics body: %s", response.Body.String())
	}
}

func TestMetricsAcceptsLoopbackScraperThroughTrustedUnixSocketProxy(t *testing.T) {
	server, err := New("test", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "http://panel.example/metrics", nil)
	// A Unix peer address is deliberately not loopback by itself. The trusted
	// socket marker and the address supplied by the shipped Nginx location must
	// jointly identify the real loopback scraper.
	request.RemoteAddr = "@"
	request.Header.Set("X-Forwarded-For", "127.0.0.1")
	request = request.WithContext(httpapi.WithTrustedProxy(request.Context()))
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "nexa_build_info") {
		t.Fatalf("trusted loopback metrics = %d %s", response.Code, response.Body.String())
	}
}

func TestPanicRecoveryWritesOneSafeResponse(t *testing.T) {
	server, err := New("test", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Handle("GET /panic", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("secret failure")
	})); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	panicRequest := httptest.NewRequest(http.MethodGet, "/panic", nil)
	panicRequest.RemoteAddr = "127.0.0.1:12345"
	server.Handler().ServeHTTP(response, panicRequest)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("panic status = %d, want 500", response.Code)
	}
	if strings.Count(response.Body.String(), `"internal_error"`) != 1 || strings.Contains(response.Body.String(), "secret failure") {
		t.Fatalf("panic response = %s", response.Body.String())
	}
}

func TestPanicRecoveryDoesNotAppendErrorAfterResponseCommitted(t *testing.T) {
	server, err := New("test", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Handle("GET /partial-panic", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte("accepted"))
		panic("late failure")
	})); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	partialPanicRequest := httptest.NewRequest(http.MethodGet, "/partial-panic", nil)
	partialPanicRequest.RemoteAddr = "127.0.0.1:12345"
	server.Handler().ServeHTTP(response, partialPanicRequest)
	if response.Code != http.StatusAccepted || response.Body.String() != "accepted" {
		t.Fatalf("committed response was corrupted: %d %q", response.Code, response.Body.String())
	}
}

type backgroundTestModule struct {
	descriptor module.Descriptor
	events     *[]string
}

func (m *backgroundTestModule) Descriptor() module.Descriptor  { return m.descriptor }
func (m *backgroundTestModule) Register(module.Registry) error { return nil }
func (m *backgroundTestModule) Start(context.Context) {
	*m.events = append(*m.events, "start:"+m.descriptor.ID)
}

func (m *backgroundTestModule) Close() {
	*m.events = append(*m.events, "close:"+m.descriptor.ID)
}

func TestBackgroundModulesStartAfterValidationAndCloseInReverseOrder(t *testing.T) {
	events := []string{}
	dependency := &backgroundTestModule{descriptor: module.Descriptor{ID: "dependency", Name: "Dependency", Version: "1"}, events: &events}
	consumer := &backgroundTestModule{descriptor: module.Descriptor{ID: "consumer", Name: "Consumer", Version: "1", Dependencies: []string{"dependency"}}, events: &events}
	server, err := New("test", []module.Module{consumer, dependency}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("constructing the server started background work: %v", events)
	}
	server.Start(context.Background())
	server.Start(context.Background())
	server.Close()
	server.Close()
	want := []string{"start:dependency", "start:consumer", "close:consumer", "close:dependency"}
	if strings.Join(events, ",") != strings.Join(want, ",") {
		t.Fatalf("lifecycle events = %v, want %v", events, want)
	}
}

func TestSecurityHeadersAllowPanelStylesAndRequireHTTPSForHSTS(t *testing.T) {
	server := &Server{}
	handler := server.securityHeaders(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	request := httptest.NewRequest(http.MethodGet, "https://panel.example.com/", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	policy := response.Header().Get("Content-Security-Policy")
	if !strings.Contains(policy, "style-src 'self' 'unsafe-inline'") || !strings.Contains(policy, "object-src 'none'") {
		t.Fatalf("panel CSP = %q", policy)
	}
	if got := response.Header().Get("Strict-Transport-Security"); got == "" {
		t.Fatal("HTTPS response is missing HSTS")
	}
	if got := response.Header().Get("Permissions-Policy"); !strings.Contains(got, "camera=()") {
		t.Fatalf("Permissions-Policy = %q", got)
	}

	plainRequest := httptest.NewRequest(http.MethodGet, "http://panel.example.com/", nil)
	plainResponse := httptest.NewRecorder()
	handler.ServeHTTP(plainResponse, plainRequest)
	if got := plainResponse.Header().Get("Strict-Transport-Security"); got != "" {
		t.Fatalf("HTTP response unexpectedly set HSTS: %q", got)
	}
}

func TestSecurityHeadersDoNotOverrideProxiedToolCSP(t *testing.T) {
	server := &Server{}
	handler := server.securityHeaders(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'none'")
		w.WriteHeader(http.StatusNoContent)
	}))
	request := httptest.NewRequest(http.MethodGet, "https://panel.example.com/tools/pgadmin/", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if got := response.Header().Values("Content-Security-Policy"); len(got) != 1 || got[0] != "default-src 'none'" {
		t.Fatalf("proxied tool CSP = %q", got)
	}
}

func TestRequireSecureTransportBlocksPublicCleartext(t *testing.T) {
	tests := []struct {
		name           string
		allowInsecure  bool
		remoteAddr     string
		tls            bool
		trustedProxy   bool
		forwardedFor   string
		forwardedProto string
		wantStatus     int
	}{
		{name: "direct TLS", remoteAddr: "198.51.100.8:443", tls: true, wantStatus: http.StatusOK},
		{name: "trusted forwarded https", remoteAddr: "127.0.0.1:5000", trustedProxy: true, forwardedFor: "198.51.100.8", forwardedProto: "https", wantStatus: http.StatusOK},
		{name: "trusted forwarded loopback http", remoteAddr: "127.0.0.1:5000", trustedProxy: true, forwardedFor: "127.0.0.1", forwardedProto: "http", wantStatus: http.StatusOK},
		{name: "trusted forwarded public http", remoteAddr: "127.0.0.1:5000", trustedProxy: true, forwardedFor: "198.51.100.8", forwardedProto: "http", wantStatus: http.StatusUpgradeRequired},
		{name: "direct public peer http", remoteAddr: "198.51.100.8:5000", wantStatus: http.StatusUpgradeRequired},
		{name: "insecure allowed public http", allowInsecure: true, remoteAddr: "198.51.100.8:5000", wantStatus: http.StatusOK},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server, err := New("test", nil, nil, WithInsecureHTTPAllowed(test.allowInsecure))
			if err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(http.MethodGet, "/api/v1/health/live", nil)
			request.RemoteAddr = test.remoteAddr
			if test.tls {
				request.TLS = &tls.ConnectionState{}
			}
			if test.forwardedFor != "" {
				request.Header.Set("X-Forwarded-For", test.forwardedFor)
			}
			if test.forwardedProto != "" {
				request.Header.Set("X-Forwarded-Proto", test.forwardedProto)
			}
			if test.trustedProxy {
				request = request.WithContext(httpapi.WithTrustedProxy(request.Context()))
			}
			response := httptest.NewRecorder()
			server.Handler().ServeHTTP(response, request)
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d (%s)", response.Code, test.wantStatus, response.Body.String())
			}
			if test.wantStatus == http.StatusUpgradeRequired {
				if !strings.Contains(response.Body.String(), "insecure_transport") {
					t.Fatalf("refusal body = %q", response.Body.String())
				}
			}
		})
	}
}

// authRequest builds a POST to an auth path that clears the secure-transport
// guard as a trusted-proxy request forwarding an https client, so it reaches the
// throttle keyed on the forwarded client IP.
func authRequest(method, path, clientIP string) *http.Request {
	request := httptest.NewRequest(method, path, nil)
	request.RemoteAddr = "127.0.0.1:5000"
	request.Header.Set("X-Forwarded-For", clientIP)
	request.Header.Set("X-Forwarded-Proto", "https")
	return request.WithContext(httpapi.WithTrustedProxy(request.Context()))
}

func TestAuthThrottleRejectsCredentialSprayPerClientIP(t *testing.T) {
	server, err := New("test", nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	// The credential-submitting budget lets an operator retype and retry, then
	// sheds the surplus with 429 once a single client exceeds it.
	throttled := false
	for i := 0; i < authThrottleLimit+1; i++ {
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, authRequest(http.MethodPost, "/api/v1/auth/login", "198.51.100.8"))
		if response.Code == http.StatusTooManyRequests {
			throttled = true
			if !strings.Contains(response.Body.String(), "rate_limited") {
				t.Fatalf("throttled body = %q", response.Body.String())
			}
			if got := response.Header().Get("Retry-After"); got == "" {
				t.Fatal("throttled response omitted Retry-After")
			}
		}
	}
	if !throttled {
		t.Fatalf("no request past the %d auth budget was throttled", authThrottleLimit)
	}

	// A different source address keeps its own budget: spraying from one host
	// must not lock out a legitimate operator elsewhere.
	other := httptest.NewRecorder()
	server.Handler().ServeHTTP(other, authRequest(http.MethodPost, "/api/v1/auth/login", "203.0.113.9"))
	if other.Code == http.StatusTooManyRequests {
		t.Fatal("a distinct client IP was throttled by another client's spray")
	}
}

func TestAuthThrottleSkipsReadOnlyGets(t *testing.T) {
	server, err := New("test", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	// The SPA polls status/session; GETs must never exhaust the strict budget.
	for i := 0; i < authThrottleLimit*3; i++ {
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, authRequest(http.MethodGet, "/api/v1/auth/status", "198.51.100.8"))
		if response.Code == http.StatusTooManyRequests {
			t.Fatalf("read-only auth GET %d was throttled by the credential budget", i)
		}
	}
}
