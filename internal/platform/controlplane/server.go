package controlplane

import (
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/platform/httpapi"
	"github.com/nexa-panel/nexa-panel/internal/platform/module"
	"github.com/nexa-panel/nexa-panel/internal/platform/secureid"
	"github.com/nexa-panel/nexa-panel/internal/platform/webui"
)

type Server struct {
	version        string
	modules        []module.Module
	mux            *http.ServeMux
	logger         *slog.Logger
	authentication Authentication
	authorization  Authorization
	patternsMu     sync.Mutex
	patterns       map[string]struct{}
	readiness      func(context.Context) error
	startedAt      time.Time
	requests       atomic.Uint64
	inFlight       atomic.Int64
	durationNanos  atomic.Uint64
	panics         atomic.Uint64
	lifecycleMu    sync.Mutex
	background     []module.Background
	started        bool
	closed         bool

	allowInsecureHTTP bool
}

type Authentication interface {
	Middleware(next http.Handler) http.Handler
}

type Authorization interface {
	Middleware(permission string, next http.Handler) http.Handler
}

type Option func(*Server)

func WithAuthentication(authentication Authentication) Option {
	return func(server *Server) {
		server.authentication = authentication
	}
}

func WithAuthorization(authorization Authorization) Option {
	return func(server *Server) {
		server.authorization = authorization
	}
}

// WithInsecureHTTPAllowed disables the secure-transport guard, permitting
// authenticated traffic to cross the network as cleartext HTTP. It exists only
// as an explicit, loudly-logged operator opt-in for deployments that terminate
// TLS elsewhere or knowingly accept the risk; it must never be the default.
func WithInsecureHTTPAllowed(allowed bool) Option {
	return func(server *Server) {
		server.allowInsecureHTTP = allowed
	}
}

func WithReadiness(check func(context.Context) error) Option {
	return func(server *Server) {
		if check != nil {
			server.readiness = check
		}
	}
}

func New(version string, modules []module.Module, logger *slog.Logger, options ...Option) (*Server, error) {
	ordered, err := module.ValidateAndSort(modules)
	if err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	server := &Server{
		version:        version,
		modules:        ordered,
		mux:            http.NewServeMux(),
		logger:         logger,
		authentication: unavailableAuthentication{},
		authorization:  unavailableAuthorization{},
		patterns:       make(map[string]struct{}),
		readiness:      func(context.Context) error { return nil },
		startedAt:      time.Now().UTC(),
	}
	for _, option := range options {
		option(server)
	}
	if server.allowInsecureHTTP {
		server.logger.Warn("insecure HTTP is allowed: authenticated traffic and the session cookie may cross the network in cleartext; front the panel with TLS instead")
	}

	if err := server.registerPlatformRoutes(); err != nil {
		return nil, err
	}
	for _, feature := range ordered {
		if err := feature.Register(server); err != nil {
			return nil, fmt.Errorf("register module %q: %w", feature.Descriptor().ID, err)
		}
		if background, ok := feature.(module.Background); ok {
			server.background = append(server.background, background)
		}
	}
	if frontend := webui.Handler(); frontend != nil {
		if err := server.Handle("/", frontend); err != nil {
			return nil, fmt.Errorf("register embedded frontend: %w", err)
		}
	}
	return server, nil
}

// Start begins every module-owned worker only after New has validated and
// registered the entire application. Calling Start more than once is safe.
func (s *Server) Start(ctx context.Context) {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.started || s.closed {
		return
	}
	for _, background := range s.background {
		background.Start(ctx)
	}
	s.started = true
}

// Close stops background modules in reverse dependency order. It is
// idempotent so deferred cleanup remains safe on listener and shutdown errors.
func (s *Server) Close() {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.closed {
		return
	}
	for index := len(s.background) - 1; index >= 0; index-- {
		s.background[index].Close()
	}
	s.closed = true
}

func (s *Server) Handler() http.Handler {
	return s.requestMetrics(s.requestLog(s.recoverPanic(s.requestID(s.securityHeaders(s.requireSecureTransport(s.mux))))))
}

// requireSecureTransport refuses to serve any request whose transport would
// expose authentication to a network eavesdropper: a non-loopback client on a
// connection that is neither direct TLS nor TLS-fronted (via the trusted proxy's
// X-Forwarded-Proto). The loopback/SSH-tunnel bootstrap workflow over plain HTTP
// on 127.0.0.1 stays allowed, as does any TLS-terminated deployment. Operators
// who knowingly run public cleartext HTTP opt in with WithInsecureHTTPAllowed.
func (s *Server) requireSecureTransport(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.allowInsecureHTTP || httpapi.IsHTTPS(r) || httpapi.IsLoopback(r) {
			next.ServeHTTP(w, r)
			return
		}
		writeJSON(w, http.StatusUpgradeRequired, map[string]string{
			"code":    "insecure_transport",
			"message": "This control panel refuses to serve authenticated traffic over unencrypted HTTP. Front it with TLS.",
		})
	})
}

func (s *Server) Handle(pattern string, handler http.Handler) error {
	if pattern == "" || handler == nil {
		return fmt.Errorf("route pattern and handler are required")
	}
	s.patternsMu.Lock()
	defer s.patternsMu.Unlock()
	if _, exists := s.patterns[pattern]; exists {
		return fmt.Errorf("route %q is already registered", pattern)
	}
	s.patterns[pattern] = struct{}{}
	s.mux.Handle(pattern, handler)
	return nil
}

func (s *Server) HandleAuthenticated(pattern string, handler http.Handler) error {
	return s.Handle(pattern, s.authentication.Middleware(handler))
}

func (s *Server) HandleAuthorized(pattern, permission string, handler http.Handler) error {
	return s.Handle(pattern, s.authentication.Middleware(s.authorization.Middleware(permission, handler)))
}

func (s *Server) registerPlatformRoutes() error {
	if err := s.Handle("GET /api/v1/health/live", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "version": s.version})
	})); err != nil {
		return err
	}
	if err := s.Handle("GET /api/v1/health/ready", http.HandlerFunc(s.readyHTTP)); err != nil {
		return err
	}
	if err := s.Handle("GET /metrics", s.localOnly(http.HandlerFunc(s.metricsHTTP))); err != nil {
		return err
	}
	return s.HandleAuthorized("GET /api/v1/modules", "system.read", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		descriptors := make([]module.Descriptor, 0, len(s.modules))
		for _, feature := range s.modules {
			descriptors = append(descriptors, feature.Descriptor())
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": descriptors})
	}))
}

func (s *Server) readyHTTP(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.readiness(ctx); err != nil {
		httpapi.WriteError(w, http.StatusServiceUnavailable, "dependency_unavailable", "A required local dependency is unavailable.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready", "version": s.version})
}

// localOnly restricts a handler to the local surface: a loopback client, or a
// loopback client fronted by the trusted proxy (nginx forwards 127.0.0.1 in
// X-Forwarded-For for a same-host Prometheus scrape, so IsLoopback resolves the
// real client). This stops relying solely on nginx to keep /metrics private
// while still permitting unauthenticated localhost scraping — authorization or
// session gating is deliberately avoided here because it would break that
// scrape. A remote caller gets 404, not 401/403, so it cannot even confirm the
// route exists.
func (s *Server) localOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !httpapi.IsLoopback(r) {
			writeJSON(w, http.StatusNotFound, map[string]string{
				"code":    "not_found",
				"message": "The requested resource was not found.",
			})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) metricsHTTP(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	uptime := time.Since(s.startedAt).Seconds()
	duration := float64(s.durationNanos.Load()) / float64(time.Second)
	_, _ = fmt.Fprintf(
		w,
		"# TYPE nexa_build_info gauge\n"+
			"nexa_build_info{version=%s} 1\n"+
			"# TYPE nexa_process_uptime_seconds gauge\n"+
			"nexa_process_uptime_seconds %.6f\n"+
			"# TYPE nexa_http_requests_total counter\n"+
			"nexa_http_requests_total %d\n"+
			"# TYPE nexa_http_requests_in_flight gauge\n"+
			"nexa_http_requests_in_flight %d\n"+
			"# TYPE nexa_http_request_duration_seconds_total counter\n"+
			"nexa_http_request_duration_seconds_total %.6f\n"+
			"# TYPE nexa_http_panics_total counter\n"+
			"nexa_http_panics_total %d\n",
		strconv.Quote(s.version), uptime, s.requests.Load(), s.inFlight.Load(), duration, s.panics.Load(),
	)
}

type unavailableAuthentication struct{}

func (unavailableAuthentication) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"code":    "authentication_unavailable",
			"message": "Authentication is not configured.",
		})
	})
}

type unavailableAuthorization struct{}

func (unavailableAuthorization) Middleware(_ string, _ http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"code": "authorization_unavailable", "message": "Authorization is not configured.",
		})
	})
}

func (s *Server) requestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		observed := newResponseObserver(w)
		defer func() {
			s.logger.Info(
				"http request",
				"request_id", observed.Header().Get("X-Request-ID"),
				"method", r.Method,
				"path", r.URL.Path,
				"status", observed.StatusCode(),
				"bytes", observed.BytesWritten(),
				"duration", time.Since(started),
			)
		}()
		next.ServeHTTP(observed, r)
	})
}

type requestIDContextKey struct{}

func (s *Server) requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := trustedRequestID(r)
		if requestID == "" {
			requestID = secureid.Hex(16)
		}
		w.Header().Set("X-Request-ID", requestID)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDContextKey{}, requestID)))
	})
}

func trustedRequestID(r *http.Request) string {
	if !httpapi.IsTrustedProxy(r.Context()) {
		return ""
	}
	identifier := r.Header.Get("X-Request-ID")
	if len(identifier) != 32 {
		return ""
	}
	if _, err := hex.DecodeString(identifier); err != nil {
		return ""
	}
	return identifier
}

func (s *Server) requestMetrics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		s.requests.Add(1)
		s.inFlight.Add(1)
		defer func() {
			s.inFlight.Add(-1)
			s.durationNanos.Add(uint64(time.Since(started)))
		}()
		next.ServeHTTP(w, r)
	})
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Third-party database tools own their response policy. Applying the panel
		// SPA's CSP to their proxied HTML would block their scripts and styles.
		if !strings.HasPrefix(r.URL.Path, "/tools/") {
			w.Header().Set("Content-Security-Policy", "default-src 'self'; base-uri 'self'; object-src 'none'; frame-ancestors 'none'; form-action 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'")
		}
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Permissions-Policy", "camera=(), geolocation=(), microphone=(), payment=(), usb=()")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		if httpapi.IsHTTPS(r) {
			w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) recoverPanic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				s.panics.Add(1)
				s.logger.Error("panic recovered", "request_id", w.Header().Get("X-Request-ID"), "error", recovered, "stack", string(debug.Stack()))
				if observed, ok := w.(*responseObserver); !ok || !observed.Committed() {
					writeJSON(w, http.StatusInternalServerError, map[string]any{
						"code":    "internal_error",
						"message": "An internal error occurred.",
					})
				}
			}
		}()
		next.ServeHTTP(w, r)
	})
}

var writeJSON = httpapi.WriteJSON
