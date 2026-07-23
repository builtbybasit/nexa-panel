// Package httpapi owns the small, security-sensitive HTTP conventions shared
// by control-plane modules: bounded strict JSON, one error envelope, and trusted
// reverse-proxy metadata. Feature modules keep business behavior and do not each
// reimplement transport policy.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
)

const DefaultRequestLimit = int64(16 * 1024)

var (
	ErrInvalidJSON  = errors.New("Request body must be valid JSON.")
	ErrTrailingJSON = errors.New("Request body must contain one JSON object.")
)

func DecodeJSON(w http.ResponseWriter, r *http.Request, destination any) error {
	return DecodeJSONLimit(w, r, destination, DefaultRequestLimit)
}

func DecodeJSONLimit(w http.ResponseWriter, r *http.Request, destination any, limit int64) error {
	if limit <= 0 {
		limit = DefaultRequestLimit
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return ErrInvalidJSON
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return ErrTrailingJSON
	}
	return nil
}

func WriteJSON(w http.ResponseWriter, status int, value any) {
	payload, err := json.Marshal(value)
	if err != nil {
		payload = []byte(`{"code":"response_encoding_failed","message":"The response could not be encoded."}`)
		status = http.StatusInternalServerError
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(append(payload, '\n'))
}

func WriteError(w http.ResponseWriter, status int, code, message string) {
	WriteJSON(w, status, map[string]string{"code": code, "message": message})
}

type trustedProxyContextKey struct{}

// WithTrustedProxy marks a connection that arrived through the API's
// permission-restricted Unix socket. Forwarded headers are ignored everywhere
// else, so a direct development TCP client cannot spoof its peer address or TLS.
func WithTrustedProxy(ctx context.Context) context.Context {
	return context.WithValue(ctx, trustedProxyContextKey{}, true)
}

func IsTrustedProxy(ctx context.Context) bool {
	trusted, _ := ctx.Value(trustedProxyContextKey{}).(bool)
	return trusted
}

func RemoteAddress(r *http.Request) string {
	if IsTrustedProxy(r.Context()) {
		if address := firstForwardedAddress(r.Header.Get("X-Forwarded-For")); address != "" {
			return address
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}

func IsHTTPS(r *http.Request) bool {
	return r.TLS != nil || (IsTrustedProxy(r.Context()) && strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https"))
}

// IsLoopback reports whether the real client is a loopback address. It reuses
// the trusted-proxy-aware RemoteAddress, so a direct TCP peer is judged by its
// actual socket address while a request through the trusted Unix socket is
// judged by the forwarded client address nginx supplies. A non-loopback or
// unparseable address is never treated as loopback.
func IsLoopback(r *http.Request) bool {
	if IsLocalSocketCaller(r) {
		return true
	}
	ip := net.ParseIP(RemoteAddress(r))
	return ip != nil && ip.IsLoopback()
}

// IsLocalSocketCaller reports whether the request came from a process on this
// host that opened the API socket itself, rather than from a client nginx
// forwarded. A Unix socket has no network peer, so RemoteAddr is "@" and parses
// as no IP at all — which used to make the seed helper's readiness probe fail
// the TLS gate with 426 on every published install, rolling back a healthy
// panel. The packaged proxy always sets X-Forwarded-For and X-Forwarded-Proto
// (packaging/nginx/nexa-panel-proxy.conf.template), so their total absence is
// what separates a local caller from a forwarded one; a remote client can never
// clear headers nginx adds after it.
func IsLocalSocketCaller(r *http.Request) bool {
	return IsTrustedProxy(r.Context()) &&
		r.Header.Get("X-Forwarded-For") == "" &&
		r.Header.Get("X-Forwarded-Proto") == ""
}

func firstForwardedAddress(value string) string {
	first, _, _ := strings.Cut(value, ",")
	first = strings.TrimSpace(first)
	if parsed := net.ParseIP(first); parsed != nil {
		return parsed.String()
	}
	return ""
}
