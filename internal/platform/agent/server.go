package agent

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	admintooloperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/admintools"
	backupoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/backups"
	certificateoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/certificates"
	deployoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/deploy"
	filesoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/files"
	firewalloperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/firewall"
	logsoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/logs"
	mysqloperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/mysql"
	packagesoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/packages"
	phpoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/php"
	postgresoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/postgres"
	scheduleoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/schedules"
	selfupdateoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/selfupdate"
	servicesoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/services"
	sftpoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/sftp"
	siteoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/sites"
	"github.com/nexa-panel/nexa-panel/internal/platform/unixsocket"
)

type Server struct {
	socketPath   string
	version      string
	token        string
	sites        siteoperator.Operator
	sftp         sftpoperator.Operator
	certificates certificateoperator.Operator
	postgres     postgresoperator.Operator
	mysql        mysqloperator.Operator
	adminTools   admintooloperator.Operator
	packages     packagesoperator.Operator
	php          phpoperator.Operator
	files        filesoperator.Operator
	logs         logsoperator.Operator
	schedules    scheduleoperator.Operator
	backups      backupoperator.Operator
	services     servicesoperator.Operator
	firewall     firewalloperator.Operator
	deploy       deployoperator.Operator
	selfUpdate   selfupdateoperator.Operator
	logger       *slog.Logger

	startedAt     time.Time
	requests      atomic.Uint64
	inFlight      atomic.Int64
	durationNanos atomic.Uint64
	panics        atomic.Uint64
}

type Option func(*Server)

func New(socketPath, version, token string, logger *slog.Logger, options ...Option) *Server {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	server := &Server{socketPath: socketPath, version: version, token: token, logger: logger, startedAt: time.Now().UTC()}
	for _, option := range options {
		option(server)
	}
	return server
}

func (s *Server) Serve(ctx context.Context) error {
	if strings.TrimSpace(s.token) == "" {
		return errors.New("agent credential is required")
	}
	listener, cleanup, err := unixsocket.Listen(s.socketPath, 0o750, 0o660)
	if err != nil {
		return err
	}
	defer cleanup()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", s.healthHTTP)
	// Registered on the mux so it inherits the bearer-token auth; the socket is
	// only reachable by the unprivileged API process, so there is no separate
	// local-surface gate as on the control plane's /metrics.
	mux.HandleFunc("GET /v1/metrics", s.metricsHTTP)
	if s.sites != nil {
		mux.HandleFunc("POST /v1/sites/plan", s.sitePlanHTTP)
		mux.HandleFunc("POST /v1/sites/teardown-plan", s.siteTeardownPlanHTTP)
		mux.HandleFunc("POST /v1/sites/apply", s.siteApplyHTTP)
		mux.HandleFunc("POST /v1/sites/rollback", s.siteRollbackHTTP)
	}
	if s.sftp != nil {
		mux.HandleFunc("POST /v1/sftp/apply", s.sftpApplyHTTP)
	}
	if s.certificates != nil {
		mux.HandleFunc("POST /v1/certificates/plan", s.certificatePlanHTTP)
		mux.HandleFunc("POST /v1/certificates/execute", s.certificateExecuteHTTP)
	}
	if s.postgres != nil {
		mux.HandleFunc("GET /v1/postgresql/instances", s.postgresDiscoverHTTP)
		mux.HandleFunc("GET /v1/postgresql/sizes", s.postgresSizesHTTP)
		mux.HandleFunc("POST /v1/postgresql/plan", s.postgresPlanHTTP)
		mux.HandleFunc("POST /v1/postgresql/apply", s.postgresApplyHTTP)
	}
	if s.mysql != nil {
		mux.HandleFunc("GET /v1/mysql-family/engine", s.mysqlDiscoverHTTP)
		mux.HandleFunc("GET /v1/mysql-family/sizes", s.mysqlSizesHTTP)
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
	if s.php != nil {
		mux.HandleFunc("GET /v1/php/versions", s.phpVersionsHTTP)
		mux.HandleFunc("GET /v1/php/extensions", s.phpExtensionsHTTP)
		mux.HandleFunc("GET /v1/php/settings", s.phpSettingsHTTP)
		mux.HandleFunc("POST /v1/php/sites/settings", s.phpSiteSettingsHTTP)
		mux.HandleFunc("POST /v1/php/plan", s.phpPlanHTTP)
		mux.HandleFunc("POST /v1/php/apply", s.phpApplyHTTP)
	}
	if s.files != nil {
		mux.HandleFunc("POST /v1/files/list", s.filesListHTTP)
		mux.HandleFunc("POST /v1/files/stat", s.filesStatHTTP)
		mux.HandleFunc("POST /v1/files/read", s.filesReadHTTP)
		mux.HandleFunc("POST /v1/files/write", s.filesWriteHTTP)
		mux.HandleFunc("POST /v1/files/mkdir", s.filesMkdirHTTP)
		mux.HandleFunc("POST /v1/files/move", s.filesMoveHTTP)
		mux.HandleFunc("POST /v1/files/chmod", s.filesChmodHTTP)
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
	if s.backups != nil {
		mux.HandleFunc("POST /v1/backups/accounts/test", s.backupTestAccountHTTP)
		mux.HandleFunc("POST /v1/backups/run", s.backupRunHTTP)
		mux.HandleFunc("POST /v1/backups/system/run", s.backupRunSystemHTTP)
		mux.HandleFunc("POST /v1/backups/restore", s.backupRestoreHTTP)
		mux.HandleFunc("POST /v1/backups/copies/delete", s.backupDeleteCopyHTTP)
		mux.HandleFunc("POST /v1/backups/schedules/install", s.backupInstallScheduleHTTP)
		mux.HandleFunc("POST /v1/backups/schedules/remove", s.backupRemoveScheduleHTTP)
	}
	if s.services != nil {
		mux.HandleFunc("GET /v1/services", s.servicesDiscoverHTTP)
		mux.HandleFunc("POST /v1/services/plan", s.servicesPlanHTTP)
		mux.HandleFunc("POST /v1/services/apply", s.servicesApplyHTTP)
	}
	if s.firewall != nil {
		mux.HandleFunc("GET /v1/firewall/status", s.firewallStatusHTTP)
		mux.HandleFunc("POST /v1/firewall/apply", s.firewallApplyHTTP)
	}
	if s.deploy != nil {
		mux.HandleFunc("POST /v1/deploy/ssh/apply", s.deploySSHApplyHTTP)
		mux.HandleFunc("POST /v1/deploy/ssh/keys/generate", s.deploySSHGenerateKeyHTTP)
		mux.HandleFunc("POST /v1/deploy/key/ensure", s.deployKeyEnsureHTTP)
		mux.HandleFunc("POST /v1/deploy/github/test", s.deployGitHubTestHTTP)
		mux.HandleFunc("POST /v1/deploy/env/read", s.deployEnvReadHTTP)
		mux.HandleFunc("POST /v1/deploy/env/write", s.deployEnvWriteHTTP)
		mux.HandleFunc("POST /v1/deploy/prepare", s.deployPrepareHTTP)
		mux.HandleFunc("POST /v1/deploy/fpm-reload/apply", s.deployFPMReloadApplyHTTP)
	}
	if s.selfUpdate != nil {
		mux.HandleFunc("GET /v1/self-update/latest", s.selfUpdateLatestHTTP)
		mux.HandleFunc("POST /v1/self-update/apply", s.selfUpdateApplyHTTP)
		mux.HandleFunc("POST /v1/self-update/rollback", s.selfUpdateRollbackHTTP)
	}
	httpServer := &http.Server{
		// Auth stays innermost so authentication failures are still logged,
		// counted, and panic-guarded; metrics is outermost so it counts every
		// request including rejected ones.
		Handler:           s.observeMetrics(s.logRequests(s.recoverPanic(s.authenticate(mux)))),
		ReadHeaderTimeout: 2 * time.Second,
		ReadTimeout:       15 * time.Second,
		// Package installs and verified database/backup restores legitimately
		// exceed a few minutes. Operator clients cap them at 30 minutes, so the
		// server allows a small shutdown/error-reporting margin beyond that cap.
		WriteTimeout:   35 * time.Minute,
		MaxHeaderBytes: 16 * 1024,
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

// recoverPanic turns a handler panic into a clean JSON 500 with a stack logged
// via slog, and bumps the panic counter — mirroring the control plane so the
// root agent process is no less observable than the API. The 500 is only
// written when the response is still uncommitted, so a partially-streamed
// download or log tail is not corrupted with a trailing error body.
func (s *Server) recoverPanic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				s.panics.Add(1)
				s.logger.Error("panic recovered", "request_id", r.Header.Get("X-Request-ID"), "error", recovered, "stack", string(debug.Stack()))
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

// logRequests emits one structured line per request (method, path, status,
// bytes, duration, and any forwarded request id). The response observer it
// builds is what recoverPanic type-asserts to decide whether a response was
// already committed, so this middleware must sit outside recoverPanic.
func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		observed := newResponseObserver(w)
		defer func() {
			s.logger.Info(
				"agent request",
				"request_id", r.Header.Get("X-Request-ID"),
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

func (s *Server) observeMetrics(next http.Handler) http.Handler {
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

func (s *Server) metricsHTTP(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	uptime := time.Since(s.startedAt).Seconds()
	duration := float64(s.durationNanos.Load()) / float64(time.Second)
	_, _ = fmt.Fprintf(
		w,
		"# TYPE nexa_agent_build_info gauge\n"+
			"nexa_agent_build_info{version=%s} 1\n"+
			"# TYPE nexa_agent_process_uptime_seconds gauge\n"+
			"nexa_agent_process_uptime_seconds %.6f\n"+
			"# TYPE nexa_agent_http_requests_total counter\n"+
			"nexa_agent_http_requests_total %d\n"+
			"# TYPE nexa_agent_http_requests_in_flight gauge\n"+
			"nexa_agent_http_requests_in_flight %d\n"+
			"# TYPE nexa_agent_http_request_duration_seconds_total counter\n"+
			"nexa_agent_http_request_duration_seconds_total %.6f\n"+
			"# TYPE nexa_agent_http_panics_total counter\n"+
			"nexa_agent_http_panics_total %d\n",
		strconv.Quote(s.version), uptime, s.requests.Load(), s.inFlight.Load(), duration, s.panics.Load(),
	)
}
