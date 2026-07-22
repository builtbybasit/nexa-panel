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
