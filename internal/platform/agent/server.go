package agent

import (
	"log/slog"
	"strings"

	"errors"
	"os"

	mysqloperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/mysql"
	"net/http"

	admintooloperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/admintools"

	packagesoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/packages"

	"crypto/subtle"
	postgresoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/postgres"

	filesoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/files"

	logsoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/logs"

	scheduleoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/schedules"

	"fmt"
	siteoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/sites"

	"net"
	"time"

	nodeoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/nodes"

	"context"
	certificateoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/certificates"

	"path/filepath"
)

type Server struct {
	socketPath   string
	version      string
	token        string
	operator     nodeoperator.Operator
	sites        siteoperator.Operator
	certificates certificateoperator.Operator
	postgres     postgresoperator.Operator
	mysql        mysqloperator.Operator
	adminTools   admintooloperator.Operator
	packages     packagesoperator.Operator
	files        filesoperator.Operator
	logs         logsoperator.Operator
	schedules    scheduleoperator.Operator
	logger       *slog.Logger
}

type Option func(*Server)

func New(socketPath, version, token string, operator nodeoperator.Operator, logger *slog.Logger, options ...Option) *Server {
	server := &Server{socketPath: socketPath, version: version, token: token, operator: operator, logger: logger}
	for _, option := range options {
		option(server)
	}
	return server
}

func (s *Server) Serve(ctx context.Context) error {
	if err := os.MkdirAll(filepath.Dir(s.socketPath), 0o750); err != nil {
		return fmt.Errorf("create socket directory: %w", err)
	}
	if err := os.Remove(s.socketPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove stale socket: %w", err)
	}

	listener, err := net.Listen("unix", s.socketPath)
	if err != nil {
		return fmt.Errorf("listen on agent socket: %w", err)
	}
	defer listener.Close()
	defer os.Remove(s.socketPath)
	if err := os.Chmod(s.socketPath, 0o660); err != nil {
		return fmt.Errorf("set socket permissions: %w", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", s.healthHTTP)
	mux.HandleFunc("POST /v1/node/probe/plan", s.planHTTP)
	mux.HandleFunc("POST /v1/node/probe/apply", s.applyHTTP)
	mux.HandleFunc("GET /v1/node/probe/observe", s.observeHTTP)
	mux.HandleFunc("POST /v1/node/probe/rollback", s.rollbackHTTP)
	if s.sites != nil {
		mux.HandleFunc("POST /v1/sites/plan", s.sitePlanHTTP)
		mux.HandleFunc("POST /v1/sites/apply", s.siteApplyHTTP)
		mux.HandleFunc("POST /v1/sites/rollback", s.siteRollbackHTTP)
	}
	if s.certificates != nil {
		mux.HandleFunc("POST /v1/certificates/plan", s.certificatePlanHTTP)
		mux.HandleFunc("POST /v1/certificates/execute", s.certificateExecuteHTTP)
	}
	if s.postgres != nil {
		mux.HandleFunc("GET /v1/postgresql/instances", s.postgresDiscoverHTTP)
		mux.HandleFunc("POST /v1/postgresql/plan", s.postgresPlanHTTP)
		mux.HandleFunc("POST /v1/postgresql/apply", s.postgresApplyHTTP)
	}
	if s.mysql != nil {
		mux.HandleFunc("GET /v1/mysql-family/engine", s.mysqlDiscoverHTTP)
		mux.HandleFunc("POST /v1/mysql-family/plan", s.mysqlPlanHTTP)
		mux.HandleFunc("POST /v1/mysql-family/apply", s.mysqlApplyHTTP)
	}
	if s.adminTools != nil {
		mux.HandleFunc("GET /v1/admin-tools", s.adminToolsDiscoverHTTP)
		mux.HandleFunc("POST /v1/admin-tools/plan", s.adminToolsPlanHTTP)
		mux.HandleFunc("POST /v1/admin-tools/apply", s.adminToolsApplyHTTP)
	}
	if s.packages != nil {
		mux.HandleFunc("GET /v1/packages/available", s.packagesCatalogHTTP)
		mux.HandleFunc("GET /v1/packages/installed", s.packagesDiscoverHTTP)
		mux.HandleFunc("POST /v1/packages/plan", s.packagesPlanHTTP)
		mux.HandleFunc("POST /v1/packages/apply", s.packagesApplyHTTP)
	}
	if s.files != nil {
		mux.HandleFunc("POST /v1/files/list", s.filesListHTTP)
		mux.HandleFunc("POST /v1/files/stat", s.filesStatHTTP)
		mux.HandleFunc("POST /v1/files/read", s.filesReadHTTP)
		mux.HandleFunc("POST /v1/files/write", s.filesWriteHTTP)
		mux.HandleFunc("POST /v1/files/mkdir", s.filesMkdirHTTP)
		mux.HandleFunc("POST /v1/files/move", s.filesMoveHTTP)
		mux.HandleFunc("POST /v1/files/copy", s.filesCopyHTTP)
		mux.HandleFunc("POST /v1/files/delete", s.filesDeleteHTTP)
		mux.HandleFunc("POST /v1/files/archive", s.filesArchiveHTTP)
		mux.HandleFunc("POST /v1/files/extract", s.filesExtractHTTP)
		mux.HandleFunc("POST /v1/files/size", s.filesSizeHTTP)
		mux.HandleFunc("POST /v1/files/uploads", s.filesUploadBeginHTTP)
		mux.HandleFunc("PUT /v1/files/uploads/{id}", s.filesUploadChunkHTTP)
		mux.HandleFunc("POST /v1/files/uploads/{id}/commit", s.filesUploadCommitHTTP)
		mux.HandleFunc("DELETE /v1/files/uploads/{id}", s.filesUploadAbortHTTP)
		mux.HandleFunc("GET /v1/files/download", s.filesDownloadHTTP)
	}
	if s.logs != nil {
		mux.HandleFunc("POST /v1/logs/list", s.logsListHTTP)
		mux.HandleFunc("POST /v1/logs/read", s.logsReadHTTP)
		mux.HandleFunc("GET /v1/logs/download", s.logsDownloadHTTP)
	}
	if s.schedules != nil {
		mux.HandleFunc("POST /v1/schedules/plan", s.schedulePlanHTTP)
		mux.HandleFunc("POST /v1/schedules/apply", s.scheduleApplyHTTP)
		mux.HandleFunc("POST /v1/schedules/rollback", s.scheduleRollbackHTTP)
		mux.HandleFunc("POST /v1/schedules/run", s.scheduleRunHTTP)
		mux.HandleFunc("POST /v1/schedules/runs", s.scheduleRunsHTTP)
	}
	httpServer := &http.Server{
		Handler:           s.authenticate(mux),
		ReadHeaderTimeout: 2 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      6 * time.Minute,
		MaxHeaderBytes:    16 * 1024,
	}

	errCh := make(chan error, 1)
	go func() {
		s.logger.Info("agent listening", "socket", s.socketPath)
		errCh <- httpServer.Serve(listener)
	}()

	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return httpServer.Shutdown(shutdownCtx)
	}
}

func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		provided := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if len(provided) != len(s.token) || subtle.ConstantTimeCompare([]byte(provided), []byte(s.token)) != 1 {
			writeError(w, http.StatusUnauthorized, "agent_authentication_failed", "A valid agent credential is required.")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) healthHTTP(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "version": s.version})
}

func (s *Server) planHTTP(w http.ResponseWriter, r *http.Request) {
	var change nodeoperator.Change
	if err := decodeJSON(w, r, &change); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	plan, err := s.operator.Plan(r.Context(), change)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "plan_failed", err.Error())
		return
	}
	plan.Signature = s.signPlan(plan)
	writeJSON(w, http.StatusOK, plan)
}

func (s *Server) applyHTTP(w http.ResponseWriter, r *http.Request) {
	var plan nodeoperator.Plan
	if err := decodeJSON(w, r, &plan); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if !s.verifyPlan(plan) {
		writeError(w, http.StatusUnprocessableEntity, "invalid_plan_signature", "The operation plan was not issued by this agent.")
		return
	}
	observation, err := s.operator.Apply(r.Context(), plan)
	if err != nil {
		writeError(w, http.StatusConflict, "apply_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, observation)
}

func (s *Server) observeHTTP(w http.ResponseWriter, r *http.Request) {
	observation, err := s.operator.Observe(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "observation_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, observation)
}

func (s *Server) rollbackHTTP(w http.ResponseWriter, r *http.Request) {
	var plan nodeoperator.Plan
	if err := decodeJSON(w, r, &plan); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if !s.verifyPlan(plan) {
		writeError(w, http.StatusUnprocessableEntity, "invalid_plan_signature", "The operation plan was not issued by this agent.")
		return
	}
	observation, err := s.operator.Rollback(r.Context(), plan)
	if err != nil {
		writeError(w, http.StatusConflict, "rollback_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, observation)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]string{"code": code, "message": message})
}
