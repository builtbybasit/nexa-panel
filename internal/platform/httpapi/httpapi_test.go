package httpapi

import (
	"context"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDecodeJSONIsStrictAndBounded(t *testing.T) {
	tests := []struct {
		name string
		body string
		err  error
	}{
		{name: "valid", body: `{"name":"nexa"}`},
		{name: "unknown field", body: `{"name":"nexa","extra":true}`, err: ErrInvalidJSON},
		{name: "trailing value", body: `{"name":"nexa"} {}`, err: ErrTrailingJSON},
		{name: "too large", body: `{"name":"long"}`, err: ErrInvalidJSON},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(test.body))
			recorder := httptest.NewRecorder()
			var payload struct {
				Name string `json:"name"`
			}
			limit := int64(1024)
			if test.name == "too large" {
				limit = 8
			}
			err := DecodeJSONLimit(recorder, request, &payload, limit)
			if err != test.err {
				t.Fatalf("error = %v, want %v", err, test.err)
			}
		})
	}
}

func TestWriteJSONFallsBackBeforeCommittingHeaders(t *testing.T) {
	recorder := httptest.NewRecorder()
	WriteJSON(recorder, http.StatusOK, map[string]any{"unsupported": make(chan int)})
	if recorder.Code != http.StatusInternalServerError || !strings.Contains(recorder.Body.String(), `"response_encoding_failed"`) {
		t.Fatalf("response = %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestForwardedMetadataRequiresTrustedProxy(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "http://panel.example/", nil)
	request.RemoteAddr = "192.0.2.20:1234"
	request.Header.Set("X-Forwarded-For", "198.51.100.8")
	request.Header.Set("X-Forwarded-Proto", "https")
	if got := RemoteAddress(request); got != "192.0.2.20" || IsHTTPS(request) {
		t.Fatalf("untrusted metadata accepted: address=%q https=%v", got, IsHTTPS(request))
	}

	request = request.WithContext(WithTrustedProxy(request.Context()))
	if got := RemoteAddress(request); got != "198.51.100.8" || !IsHTTPS(request) {
		t.Fatalf("trusted metadata ignored: address=%q https=%v", got, IsHTTPS(request))
	}

	request = request.WithContext(context.Background())
	request.TLS = &tls.ConnectionState{}
	if !IsHTTPS(request) {
		t.Fatal("direct TLS connection was not recognized")
	}
}

func TestIsLoopback(t *testing.T) {
	// A direct TCP peer is judged by its socket address, ignoring forwarded
	// headers it may try to spoof.
	direct := httptest.NewRequest(http.MethodGet, "http://panel.example/", nil)
	direct.RemoteAddr = "127.0.0.1:5000"
	direct.Header.Set("X-Forwarded-For", "198.51.100.8")
	if !IsLoopback(direct) {
		t.Fatal("direct loopback peer was not recognized")
	}

	// An untrusted client cannot forge a loopback client via X-Forwarded-For:
	// the real peer wins.
	spoofed := httptest.NewRequest(http.MethodGet, "http://panel.example/", nil)
	spoofed.RemoteAddr = "198.51.100.8:5000"
	spoofed.Header.Set("X-Forwarded-For", "127.0.0.1")
	if IsLoopback(spoofed) {
		t.Fatal("untrusted forwarded loopback address was accepted")
	}

	// Through the trusted proxy, the forwarded loopback client is honored (nginx
	// on 127.0.0.1 or an SSH tunnel).
	trustedLoopback := spoofed.WithContext(WithTrustedProxy(spoofed.Context()))
	if !IsLoopback(trustedLoopback) {
		t.Fatal("trusted forwarded loopback client was rejected")
	}

	// Through the trusted proxy, a forwarded public client is not loopback.
	trustedPublic := httptest.NewRequest(http.MethodGet, "http://panel.example/", nil)
	trustedPublic.RemoteAddr = "127.0.0.1:5000"
	trustedPublic.Header.Set("X-Forwarded-For", "198.51.100.8")
	trustedPublic = trustedPublic.WithContext(WithTrustedProxy(trustedPublic.Context()))
	if IsLoopback(trustedPublic) {
		t.Fatal("trusted forwarded public client was treated as loopback")
	}
}

// A process on this host that opens the API socket itself has no network peer,
// so it must be judged local. The seed helper's readiness probe takes exactly
// this path, and treating it as a public cleartext client made every published
// install roll back a healthy panel.
func TestLocalSocketCallerIsLoopbackButForwardedClientsAreNot(t *testing.T) {
	local := httptest.NewRequest(http.MethodGet, "/api/v1/health/ready", nil)
	local.RemoteAddr = "@"
	local = local.WithContext(WithTrustedProxy(local.Context()))
	if !IsLocalSocketCaller(local) || !IsLoopback(local) {
		t.Fatal("a direct Unix-socket caller with no forwarded headers must be local")
	}

	// nginx always adds X-Forwarded-For, so a forwarded remote client must never
	// inherit the local caller's exemption from the TLS gate.
	forwarded := httptest.NewRequest(http.MethodGet, "/api/v1/health/ready", nil)
	forwarded.RemoteAddr = "@"
	forwarded.Header.Set("X-Forwarded-For", "203.0.113.7")
	forwarded = forwarded.WithContext(WithTrustedProxy(forwarded.Context()))
	if IsLocalSocketCaller(forwarded) || IsLoopback(forwarded) {
		t.Fatal("a forwarded public client must not be treated as local")
	}

	// A proxied client that only announces its scheme is still not local.
	scheme := httptest.NewRequest(http.MethodGet, "/api/v1/health/ready", nil)
	scheme.RemoteAddr = "@"
	scheme.Header.Set("X-Forwarded-Proto", "http")
	scheme = scheme.WithContext(WithTrustedProxy(scheme.Context()))
	if IsLocalSocketCaller(scheme) {
		t.Fatal("a request carrying proxy headers must not be treated as local")
	}

	// Without the trusted-proxy marker a direct TCP client cannot claim locality.
	untrusted := httptest.NewRequest(http.MethodGet, "/api/v1/health/ready", nil)
	untrusted.RemoteAddr = "203.0.113.9:44321"
	if IsLocalSocketCaller(untrusted) || IsLoopback(untrusted) {
		t.Fatal("an untrusted TCP client must never be local")
	}
}
