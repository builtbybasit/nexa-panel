package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/uptrace/bun"

	"github.com/nexa-panel/nexa-panel/internal/platform/audit"
	"github.com/nexa-panel/nexa-panel/internal/platform/module"
	"github.com/nexa-panel/nexa-panel/internal/platform/persistence"
	"github.com/nexa-panel/nexa-panel/internal/platform/secureid"
)

const schema = `
	CREATE TABLE jobs (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		kind TEXT NOT NULL,
		title TEXT NOT NULL DEFAULT '',
		state TEXT NOT NULL,
		progress INTEGER NOT NULL DEFAULT 0,
		actor_user_id TEXT,
		request_json TEXT NOT NULL DEFAULT '{}',
		result_json TEXT,
		failure TEXT,
		created_at TIMESTAMP NOT NULL,
		updated_at TIMESTAMP NOT NULL,
		started_at TIMESTAMP,
		completed_at TIMESTAMP
	);
	CREATE INDEX jobs_state_id_idx ON jobs (state, id);
	CREATE TABLE job_events (
		sequence INTEGER PRIMARY KEY AUTOINCREMENT,
		job_id INTEGER NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
		state TEXT NOT NULL,
		progress INTEGER NOT NULL,
		message TEXT NOT NULL,
		occurred_at TIMESTAMP NOT NULL
	);
	CREATE INDEX job_events_job_sequence_idx ON job_events (job_id, sequence);
`

// lifecycleSchema adds the ownership data needed to make recovery deliberate.
// Running work is protected by a renewable lease; an expired lease is either
// retried or failed according to the handler's persisted policy. Idempotency
// keys make submissions safe to repeat across timer and HTTP retries.
const lifecycleSchema = `
	ALTER TABLE jobs ADD COLUMN recovery_policy TEXT NOT NULL DEFAULT 'fail';
	ALTER TABLE jobs ADD COLUMN idempotency_key TEXT;
	ALTER TABLE jobs ADD COLUMN scope_site_ids TEXT NOT NULL DEFAULT '[]';
	ALTER TABLE jobs ADD COLUMN attempt INTEGER NOT NULL DEFAULT 0;
	ALTER TABLE jobs ADD COLUMN lease_owner TEXT;
	ALTER TABLE jobs ADD COLUMN lease_token TEXT;
	ALTER TABLE jobs ADD COLUMN lease_expires_at TIMESTAMP;
	CREATE UNIQUE INDEX jobs_kind_idempotency_idx
		ON jobs (kind, idempotency_key) WHERE idempotency_key IS NOT NULL;
	CREATE INDEX jobs_running_lease_idx ON jobs (state, lease_expires_at);
`

type State string

const (
	StateQueued    State = "queued"
	StateRunning   State = "running"
	StateSucceeded State = "succeeded"
	StateFailed    State = "failed"
)

type RecoveryPolicy string

const (
	// RecoveryFail is the safe default for host-mutating work: once ownership is
	// lost, the result is uncertain and must be reconciled rather than replayed.
	RecoveryFail RecoveryPolicy = "fail"
	// RecoveryRetry is reserved for handlers whose side effects are idempotent.
	RecoveryRetry RecoveryPolicy = "retry"
)

func (p RecoveryPolicy) valid() bool { return p == RecoveryFail || p == RecoveryRetry }

func (s State) Terminal() bool {
	return s == StateSucceeded || s == StateFailed
}

type Job struct {
	ID          int64           `json:"id"`
	Kind        string          `json:"kind"`
	Title       string          `json:"title,omitempty"`
	State       State           `json:"state"`
	Progress    int             `json:"progress"`
	ActorUserID *string         `json:"actorUserId,omitempty"`
	SiteIDs     []string        `json:"siteIds,omitempty"`
	Attempt     int             `json:"attempt"`
	Request     json.RawMessage `json:"request"`
	Result      json.RawMessage `json:"result,omitempty"`
	Failure     string          `json:"failure,omitempty"`
	CreatedAt   time.Time       `json:"createdAt"`
	UpdatedAt   time.Time       `json:"updatedAt"`
	StartedAt   *time.Time      `json:"startedAt,omitempty"`
	CompletedAt *time.Time      `json:"completedAt,omitempty"`
}

type Event struct {
	Sequence   int64     `json:"sequence"`
	JobID      int64     `json:"jobId"`
	State      State     `json:"state"`
	Progress   int       `json:"progress"`
	Message    string    `json:"message"`
	OccurredAt time.Time `json:"occurredAt"`
}

type Handler func(ctx context.Context, request json.RawMessage, report func(progress int, message string) error) (any, error)

type HandlerOptions struct {
	RecoveryPolicy RecoveryPolicy
}

type handlerRegistration struct {
	handler        Handler
	recoveryPolicy RecoveryPolicy
}

type Config struct {
	PollInterval  time.Duration
	LeaseDuration time.Duration
}

type Module struct {
	database *bun.DB
	audit    audit.Recorder
	logger   *slog.Logger
	now      func() time.Time
	config   Config

	handlersMu sync.RWMutex
	handlers   map[string]handlerRegistration
	workerID   string
	siteAccess SiteAccessPolicy
	notify     chan struct{}
	cancel     context.CancelFunc
	done       chan struct{}
	startOnce  sync.Once
	closeOnce  sync.Once
}

type jobModel struct {
	bun.BaseModel  `bun:"table:jobs,alias:job"`
	ID             int64 `bun:",pk,autoincrement"`
	Kind           string
	Title          string
	State          string
	Progress       int
	ActorUserID    *string `bun:"actor_user_id"`
	RequestJSON    string
	ResultJSON     *string
	Failure        *string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	StartedAt      *time.Time
	CompletedAt    *time.Time
	RecoveryPolicy string
	IdempotencyKey *string
	ScopeSiteIDs   string `bun:"scope_site_ids"`
	Attempt        int
	LeaseOwner     *string
	LeaseToken     *string
	LeaseExpiresAt *time.Time
}

type eventModel struct {
	bun.BaseModel `bun:"table:job_events,alias:job_event"`
	Sequence      int64 `bun:",pk,autoincrement"`
	JobID         int64
	State         string
	Progress      int
	Message       string
	OccurredAt    time.Time
}

func New(ctx context.Context, database *bun.DB, recorder audit.Recorder, logger *slog.Logger) (*Module, error) {
	return NewWithConfig(ctx, database, recorder, logger, Config{
		PollInterval:  time.Second,
		LeaseDuration: 30 * time.Second,
	})
}

func NewWithConfig(ctx context.Context, database *bun.DB, recorder audit.Recorder, logger *slog.Logger, config Config) (*Module, error) {
	if database == nil || recorder == nil {
		return nil, errors.New("jobs database and audit recorder are required")
	}
	if config.PollInterval <= 0 {
		return nil, errors.New("job poll interval must be positive")
	}
	if config.LeaseDuration == 0 {
		config.LeaseDuration = 30 * time.Second
	}
	if config.LeaseDuration <= 0 {
		return nil, errors.New("job lease duration must be positive")
	}
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if err := migrate(ctx, database); err != nil {
		return nil, err
	}
	module := &Module{
		database: database, audit: recorder, logger: logger, now: time.Now, config: config,
		handlers: make(map[string]handlerRegistration), workerID: randomToken(),
		notify: make(chan struct{}, 1), done: make(chan struct{}),
	}
	if err := module.recoverExpired(ctx); err != nil {
		return nil, err
	}
	if err := module.RegisterHandlerWithOptions("platform.diagnostics", diagnosticsHandler, HandlerOptions{RecoveryPolicy: RecoveryRetry}); err != nil {
		return nil, err
	}
	return module, nil
}

func (m *Module) Descriptor() module.Descriptor {
	return module.Descriptor{
		ID: "jobs", Name: "Jobs", Version: "0.1.0",
		Description:  "Durable background operations with restart recovery and progress events.",
		Dependencies: []string{"audit", "identity"}, EstimatedIdleBytes: 2 * 1024 * 1024,
	}
}

func (m *Module) Register(registry module.Registry) error {
	routes := []struct {
		pattern    string
		permission string
		handler    http.Handler
	}{
		{"GET /api/v1/jobs", "jobs.read", http.HandlerFunc(m.listHTTP)},
		{"GET /api/v1/jobs/{id}", "jobs.read", http.HandlerFunc(m.getHTTP)},
		{"GET /api/v1/jobs/{id}/events", "jobs.read", http.HandlerFunc(m.eventsHTTP)},
		{"POST /api/v1/jobs/diagnostics", "operations.apply", http.HandlerFunc(m.diagnosticsHTTP)},
	}
	for _, route := range routes {
		if err := registry.HandleAuthorized(route.pattern, route.permission, route.handler); err != nil {
			return err
		}
	}
	return nil
}

func (m *Module) RegisterHandler(kind string, handler Handler) error {
	return m.RegisterHandlerWithOptions(kind, handler, HandlerOptions{RecoveryPolicy: RecoveryFail})
}

func (m *Module) RegisterHandlerWithOptions(kind string, handler Handler, options HandlerOptions) error {
	if strings.TrimSpace(kind) == "" || handler == nil {
		return errors.New("job kind and handler are required")
	}
	if !options.RecoveryPolicy.valid() {
		return errors.New("job recovery policy must be fail or retry")
	}
	m.handlersMu.Lock()
	defer m.handlersMu.Unlock()
	if _, exists := m.handlers[kind]; exists {
		return fmt.Errorf("job handler %q is already registered", kind)
	}
	m.handlers[kind] = handlerRegistration{handler: handler, recoveryPolicy: options.RecoveryPolicy}
	return nil
}

func migrate(ctx context.Context, database *bun.DB) error {
	return persistence.Migrate(ctx, database, "jobs", []string{schema, lifecycleSchema})
}

func randomToken() string { return secureid.Hex(16) }

func (m *Module) Start(parent context.Context) {
	m.startOnce.Do(func() {
		ctx, cancel := context.WithCancel(parent)
		m.cancel = cancel
		go m.worker(ctx)
		m.wake()
	})
}

func (m *Module) Close() {
	m.closeOnce.Do(func() {
		if m.cancel == nil {
			close(m.done)
			return
		}
		m.cancel()
		<-m.done
	})
}
