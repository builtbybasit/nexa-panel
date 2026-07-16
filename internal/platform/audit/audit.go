package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/platform/module"
	"github.com/nexa-panel/nexa-panel/internal/platform/persistence"
	"github.com/uptrace/bun"
)

const schema = `
	CREATE TABLE audit_events (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		occurred_at TIMESTAMP NOT NULL,
		actor_user_id TEXT,
		action TEXT NOT NULL,
		subject TEXT NOT NULL,
		remote_address TEXT NOT NULL DEFAULT '',
		metadata TEXT NOT NULL DEFAULT '{}'
	);
	CREATE INDEX audit_events_occurred_at_idx ON audit_events (occurred_at DESC);
`

type Event struct {
	ID            int64          `json:"id"`
	OccurredAt    time.Time      `json:"occurredAt"`
	ActorUserID   *string        `json:"actorUserId,omitempty"`
	Action        string         `json:"action"`
	Subject       string         `json:"subject"`
	RemoteAddress string         `json:"remoteAddress,omitempty"`
	Metadata      map[string]any `json:"metadata"`
}

type Entry struct {
	ActorUserID   *string
	Action        string
	Subject       string
	RemoteAddress string
	Metadata      map[string]any
}

type Recorder interface {
	Record(ctx context.Context, entry Entry) error
}

type Module struct {
	database *bun.DB
	now      func() time.Time
}

type eventModel struct {
	bun.BaseModel `bun:"table:audit_events,alias:audit_event"`
	ID            int64     `bun:",pk,autoincrement"`
	OccurredAt    time.Time `bun:",notnull"`
	ActorUserID   *string
	Action        string `bun:",notnull"`
	Subject       string `bun:",notnull"`
	RemoteAddress string `bun:",notnull"`
	Metadata      string `bun:",notnull"`
}

func New(ctx context.Context, database *bun.DB) (*Module, error) {
	if database == nil {
		return nil, errors.New("audit database is required")
	}
	if err := persistence.Migrate(ctx, database, "audit", []string{schema}); err != nil {
		return nil, err
	}
	return &Module{database: database, now: time.Now}, nil
}

func (m *Module) Descriptor() module.Descriptor {
	return module.Descriptor{
		ID:                 "audit",
		Name:               "Audit Log",
		Version:            "0.1.0",
		Description:        "Append-only record of security and control-plane activity.",
		EstimatedIdleBytes: 512 * 1024,
	}
}

func (m *Module) Register(registry module.Registry) error {
	return registry.HandleAuthorized("GET /api/v1/audit/events", "audit.read", http.HandlerFunc(m.listHTTP))
}

func (m *Module) Record(ctx context.Context, entry Entry) error {
	if entry.Action == "" || entry.Subject == "" {
		return errors.New("audit action and subject are required")
	}
	metadata := entry.Metadata
	if metadata == nil {
		metadata = map[string]any{}
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return fmt.Errorf("encode audit metadata: %w", err)
	}
	model := &eventModel{
		OccurredAt: m.now().UTC(), ActorUserID: entry.ActorUserID, Action: entry.Action,
		Subject: entry.Subject, RemoteAddress: entry.RemoteAddress, Metadata: string(encoded),
	}
	_, err = m.database.NewInsert().Model(model).Exec(ctx)
	if err != nil {
		return fmt.Errorf("record audit event: %w", err)
	}
	return nil
}

func (m *Module) List(ctx context.Context, limit int) ([]Event, error) {
	if limit < 1 || limit > 200 {
		limit = 100
	}
	models := make([]eventModel, 0, limit)
	err := m.database.NewSelect().Model(&models).OrderExpr("id DESC").Limit(limit).Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("list audit events: %w", err)
	}

	events := make([]Event, 0, len(models))
	for _, model := range models {
		event := Event{
			ID: model.ID, OccurredAt: model.OccurredAt, ActorUserID: model.ActorUserID,
			Action: model.Action, Subject: model.Subject, RemoteAddress: model.RemoteAddress,
		}
		if err := json.Unmarshal([]byte(model.Metadata), &event.Metadata); err != nil {
			return nil, fmt.Errorf("decode audit metadata: %w", err)
		}
		events = append(events, event)
	}
	return events, nil
}

func (m *Module) listHTTP(w http.ResponseWriter, r *http.Request) {
	limit := 100
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 200 {
			writeError(w, http.StatusBadRequest, "invalid_limit", "Limit must be between 1 and 200.")
			return
		}
		limit = parsed
	}
	events, err := m.List(r.Context(), limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "audit_unavailable", "Audit events could not be loaded.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": events})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]string{"code": code, "message": message})
}
