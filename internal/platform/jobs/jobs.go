package jobs

import (
	"github.com/nexa-panel/nexa-panel/internal/platform/audit"

	"github.com/uptrace/bun"
	"io"

	"context"
	"time"

	"fmt"

	"net/http"

	"strings"

	"github.com/nexa-panel/nexa-panel/internal/platform/module"
	"github.com/nexa-panel/nexa-panel/internal/platform/persistence"
	"log/slog"

	"sync"

	"encoding/json"
	"errors"
)

const schema = `
	CREATE TABLE jobs (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		kind TEXT NOT NULL,
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

type State string

const (
	StateQueued    State = "queued"
	StateRunning   State = "running"
	StateSucceeded State = "succeeded"
	StateFailed    State = "failed"
)

func (s State) Terminal() bool {
	return s == StateSucceeded || s == StateFailed
}

type Job struct {
	ID          int64           `json:"id"`
	Kind        string          `json:"kind"`
	State       State           `json:"state"`
	Progress    int             `json:"progress"`
	ActorUserID *string         `json:"actorUserId,omitempty"`
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

type Config struct {
	PollInterval time.Duration
}

type Module struct {
	database *bun.DB
	audit    audit.Recorder
	logger   *slog.Logger
	now      func() time.Time
	config   Config

	handlersMu sync.RWMutex
	handlers   map[string]Handler
	notify     chan struct{}
	cancel     context.CancelFunc
	done       chan struct{}
	startOnce  sync.Once
	closeOnce  sync.Once
}

type jobModel struct {
	bun.BaseModel `bun:"table:jobs,alias:job"`
	ID            int64 `bun:",pk,autoincrement"`
	Kind          string
	State         string
	Progress      int
	ActorUserID   *string
	RequestJSON   string
	ResultJSON    *string
	Failure       *string
	CreatedAt     time.Time
	UpdatedAt     time.Time
	StartedAt     *time.Time
	CompletedAt   *time.Time
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
	return NewWithConfig(ctx, database, recorder, logger, Config{PollInterval: time.Second})
}

func NewWithConfig(ctx context.Context, database *bun.DB, recorder audit.Recorder, logger *slog.Logger, config Config) (*Module, error) {
	if database == nil || recorder == nil {
		return nil, errors.New("jobs database and audit recorder are required")
	}
	if config.PollInterval <= 0 {
		return nil, errors.New("job poll interval must be positive")
	}
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if err := persistence.Migrate(ctx, database, "jobs", []string{schema}); err != nil {
		return nil, err
	}
	module := &Module{
		database: database, audit: recorder, logger: logger, now: time.Now, config: config,
		handlers: make(map[string]Handler), notify: make(chan struct{}, 1), done: make(chan struct{}),
	}
	if err := module.recoverInterrupted(ctx); err != nil {
		return nil, err
	}
	if err := module.RegisterHandler("platform.diagnostics", diagnosticsHandler); err != nil {
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
	if strings.TrimSpace(kind) == "" || handler == nil {
		return errors.New("job kind and handler are required")
	}
	m.handlersMu.Lock()
	defer m.handlersMu.Unlock()
	if _, exists := m.handlers[kind]; exists {
		return fmt.Errorf("job handler %q is already registered", kind)
	}
	m.handlers[kind] = handler
	return nil
}

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
