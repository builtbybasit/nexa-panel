package controlplane

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"sync"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/platform/module"
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

func New(version string, modules []module.Module, logger *slog.Logger, options ...Option) (*Server, error) {
	ordered, err := module.ValidateAndSort(modules)
	if err != nil {
		return nil, err
	}
	server := &Server{
		version:        version,
		modules:        ordered,
		mux:            http.NewServeMux(),
		logger:         logger,
		authentication: unavailableAuthentication{},
		authorization:  unavailableAuthorization{},
		patterns:       make(map[string]struct{}),
	}
	for _, option := range options {
		option(server)
	}

	if err := server.registerPlatformRoutes(); err != nil {
		return nil, err
	}
	for _, feature := range ordered {
		if err := feature.Register(server); err != nil {
			return nil, fmt.Errorf("register module %q: %w", feature.Descriptor().ID, err)
		}
	}
	if frontend := webui.Handler(); frontend != nil {
		if err := server.Handle("/", frontend); err != nil {
			return nil, fmt.Errorf("register embedded frontend: %w", err)
		}
	}
	return server, nil
}

func (s *Server) Handler() http.Handler {
	return s.recoverPanic(s.requestLog(s.securityHeaders(s.mux)))
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
	return s.HandleAuthorized("GET /api/v1/modules", "system.read", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		descriptors := make([]module.Descriptor, 0, len(s.modules))
		for _, feature := range s.modules {
			descriptors = append(descriptors, feature.Descriptor())
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": descriptors})
	}))
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
		next.ServeHTTP(w, r)
		s.logger.Info("http request", "method", r.Method, "path", r.URL.Path, "duration", time.Since(started))
	})
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; frame-ancestors 'none'")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) recoverPanic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				s.logger.Error("panic recovered", "error", recovered, "stack", string(debug.Stack()))
				writeJSON(w, http.StatusInternalServerError, map[string]any{
					"code":    "internal_error",
					"message": "An internal error occurred.",
				})
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
