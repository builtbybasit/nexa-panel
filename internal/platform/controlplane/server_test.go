package controlplane

import (
	"context"
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
	untrusted.Header.Set("X-Request-ID", forwarded)
	untrustedResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(untrustedResponse, untrusted)
	if got := untrustedResponse.Header().Get("X-Request-ID"); got == forwarded {
		t.Fatal("a direct client was allowed to choose the request ID")
	}

	trusted := httptest.NewRequest(http.MethodGet, "/api/v1/health/live", nil)
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
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/panic", nil))
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
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/partial-panic", nil))
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
