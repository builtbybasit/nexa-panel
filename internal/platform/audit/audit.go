package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/platform/httpapi"
	"github.com/nexa-panel/nexa-panel/internal/platform/module"
	"github.com/uptrace/bun"
)

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
	// chainMu serialises the tail-hash read and the insert that follows it, so
	// two concurrent Record calls cannot both chain from the same predecessor and
	// fork the chain. The transaction alone would already serialise them under
	// persistence.Open's single-connection pool, but the mutex keeps the
	// invariant independent of that pool setting; the control plane is one
	// process, so an in-process lock is sufficient.
	chainMu sync.Mutex
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
	PrevHash      string `bun:",notnull"`
	Hash          string `bun:",notnull"`
}

// New builds the audit module. Its schema is applied centrally by
// persistence.RunMigrations before the module is constructed.
func New(_ context.Context, database *bun.DB) (*Module, error) {
	if database == nil {
		return nil, errors.New("audit database is required")
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
	routes := []struct {
		pattern, permission string
		handler             http.Handler
	}{
		{"GET /api/v1/audit/events", "audit.read", http.HandlerFunc(m.listHTTP)},
		{"GET /api/v1/audit/verify", "audit.read", http.HandlerFunc(m.verifyHTTP)},
	}
	for _, route := range routes {
		if err := registry.HandleAuthorized(route.pattern, route.permission, route.handler); err != nil {
			return err
		}
	}
	return nil
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
	// SQLite stores this column with microsecond precision, so hashing the
	// nanosecond-precision clock reading would digest a timestamp the row can
	// never carry: Verify would then recompute a different hash for every row and
	// report an untouched log as tampered with. Truncating before the hash keeps
	// the digested value and the stored value identical. macOS reads the clock at
	// microsecond granularity already, which is why this only ever surfaced on a
	// Linux node.
	model := &eventModel{
		OccurredAt: m.now().UTC().Truncate(time.Microsecond), ActorUserID: entry.ActorUserID, Action: entry.Action,
		Subject: entry.Subject, RemoteAddress: entry.RemoteAddress, Metadata: string(encoded),
	}
	m.chainMu.Lock()
	defer m.chainMu.Unlock()
	err = m.database.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		// RunInTx opens a DEFERRED transaction, so reading first would take a WAL
		// read snapshot and the insert that follows would upgrade to a write — and
		// SQLite fails that upgrade with SQLITE_BUSY_SNAPSHOT the instant another
		// process has committed in between, without ever consulting busy_timeout.
		// Touching the chain state first makes this a write transaction up front,
		// where a competing writer is waited out by busy_timeout instead.
		if _, err := tx.NewUpdate().Model((*chainStateModel)(nil)).
			Set("watermark = watermark").Where("id = ?", 1).Exec(ctx); err != nil {
			return fmt.Errorf("claim the audit chain for writing: %w", err)
		}
		watermark, err := chainWatermark(ctx, tx)
		if err != nil {
			return fmt.Errorf("read audit chain start: %w", err)
		}
		previous, err := tailHash(ctx, tx, watermark)
		if err != nil {
			return err
		}
		model.PrevHash = previous
		model.Hash = chainHash(previous, model)
		_, err = tx.NewInsert().Model(model).Exec(ctx)
		return err
	})
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

func (m *Module) verifyHTTP(w http.ResponseWriter, r *http.Request) {
	result, err := m.Verify(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "audit_unavailable", "The audit chain could not be verified.")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

var (
	writeJSON  = httpapi.WriteJSON
	writeError = httpapi.WriteError
)
