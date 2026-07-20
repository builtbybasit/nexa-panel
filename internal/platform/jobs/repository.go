package jobs

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/uptrace/bun"

	"github.com/nexa-panel/nexa-panel/internal/platform/audit"
)

var ErrIdempotencyConflict = errors.New("idempotency key was already used with a different request")

type SubmitOptions struct {
	ActorUserID    *string
	SiteIDs        []string
	IdempotencyKey string
}

// EnqueueOptions are explicit because an enqueue-only process has no handler
// registry from which to infer the recovery policy.
type EnqueueOptions struct {
	SubmitOptions
	RecoveryPolicy RecoveryPolicy
}

// Enqueuer is the narrow queue-writing interface used by systemd helpers. It
// applies queue migrations and inserts events, but never recovers or executes
// running work.
type Enqueuer struct {
	database *bun.DB
	now      func() time.Time
}

func NewEnqueuer(ctx context.Context, database *bun.DB) (*Enqueuer, error) {
	if database == nil {
		return nil, errors.New("jobs database is required")
	}
	if err := migrate(ctx, database); err != nil {
		return nil, err
	}
	return &Enqueuer{database: database, now: time.Now}, nil
}

func (e *Enqueuer) EnqueueTitled(ctx context.Context, kind, title string, request any, options EnqueueOptions) (Job, error) {
	if !options.RecoveryPolicy.valid() {
		return Job{}, errors.New("job recovery policy must be fail or retry")
	}
	job, _, err := persistSubmission(ctx, e.database, e.now, kind, title, request, options.SubmitOptions, options.RecoveryPolicy)
	return job, err
}

// Submit queues a job with no display title; the UI falls back to the formatted
// kind. Prefer SubmitTitled so the Jobs list reads "Install nginx", not "Applications · Apply".
func (m *Module) Submit(ctx context.Context, kind string, request any, actorUserID *string) (Job, error) {
	return m.SubmitTitled(ctx, kind, "", request, actorUserID)
}

// SubmitTitled queues a job with a human-readable title (e.g. "Activate portal.example.com")
// shown in the Jobs list and progress views in place of the bare kind.
func (m *Module) SubmitTitled(ctx context.Context, kind, title string, request any, actorUserID *string) (Job, error) {
	return m.SubmitTitledWithOptions(ctx, kind, title, request, SubmitOptions{ActorUserID: actorUserID})
}

func (m *Module) SubmitTitledWithOptions(ctx context.Context, kind, title string, request any, options SubmitOptions) (Job, error) {
	m.handlersMu.RLock()
	registration, supported := m.handlers[kind]
	m.handlersMu.RUnlock()
	if !supported {
		return Job{}, fmt.Errorf("unsupported job kind %q", kind)
	}
	job, created, err := persistSubmission(ctx, m.database, m.now, kind, title, request, options, registration.recoveryPolicy)
	if err != nil {
		return Job{}, err
	}
	if created {
		m.wake()
		m.recordAudit(ctx, audit.Entry{
			ActorUserID: options.ActorUserID,
			Action:      "job.submitted",
			Subject:     fmt.Sprintf("job:%d", job.ID),
			Metadata:    map[string]any{"kind": kind},
		})
	}
	return job, nil
}

func persistSubmission(
	ctx context.Context,
	database *bun.DB,
	nowFunc func() time.Time,
	kind, title string,
	request any,
	options SubmitOptions,
	recoveryPolicy RecoveryPolicy,
) (Job, bool, error) {
	kind = strings.TrimSpace(kind)
	if kind == "" {
		return Job{}, false, errors.New("job kind is required")
	}
	if !recoveryPolicy.valid() {
		return Job{}, false, errors.New("job recovery policy must be fail or retry")
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		return Job{}, false, fmt.Errorf("encode job request: %w", err)
	}
	siteIDs, err := normalizeSiteIDs(options.SiteIDs)
	if err != nil {
		return Job{}, false, err
	}
	encodedSiteIDs, err := json.Marshal(siteIDs)
	if err != nil {
		return Job{}, false, fmt.Errorf("encode job site scope: %w", err)
	}
	key, err := normalizeIdempotencyKey(options.IdempotencyKey)
	if err != nil {
		return Job{}, false, err
	}
	now := nowFunc().UTC()
	model := &jobModel{
		Kind: kind, Title: strings.TrimSpace(title), State: string(StateQueued), Progress: 0,
		ActorUserID: options.ActorUserID, RequestJSON: string(encoded), RecoveryPolicy: string(recoveryPolicy),
		IdempotencyKey: key, ScopeSiteIDs: string(encodedSiteIDs), CreatedAt: now, UpdatedAt: now,
	}
	created := false
	err = database.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		if key != nil {
			existing := new(jobModel)
			err := tx.NewSelect().Model(existing).Where("kind = ?", kind).Where("idempotency_key = ?", *key).Scan(ctx)
			if err == nil {
				if existing.RequestJSON != string(encoded) || existing.ScopeSiteIDs != string(encodedSiteIDs) ||
					!sameActor(existing.ActorUserID, options.ActorUserID) {
					return ErrIdempotencyConflict
				}
				*model = *existing
				return nil
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
		}
		if _, err := tx.NewInsert().Model(model).Exec(ctx); err != nil {
			return err
		}
		event := &eventModel{JobID: model.ID, State: string(StateQueued), Progress: 0, Message: "Job queued.", OccurredAt: now}
		if _, err := tx.NewInsert().Model(event).Exec(ctx); err != nil {
			return err
		}
		created = true
		return nil
	})
	if err != nil {
		// A concurrent submitter can win the unique index after our initial read.
		// Resolve that race to the same idempotent result instead of surfacing a
		// storage constraint as a 500.
		if key != nil {
			existing := new(jobModel)
			if lookupErr := database.NewSelect().Model(existing).
				Where("kind = ?", kind).Where("idempotency_key = ?", *key).Scan(ctx); lookupErr == nil {
				if existing.RequestJSON != string(encoded) || existing.ScopeSiteIDs != string(encodedSiteIDs) ||
					!sameActor(existing.ActorUserID, options.ActorUserID) {
					return Job{}, false, fmt.Errorf("submit job: %w", ErrIdempotencyConflict)
				}
				return existing.toJob(), false, nil
			}
		}
		return Job{}, false, fmt.Errorf("submit job: %w", err)
	}
	return model.toJob(), created, nil
}

func sameActor(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func normalizeIdempotencyKey(raw string) (*string, error) {
	key := strings.TrimSpace(raw)
	if key == "" {
		return nil, nil
	}
	if len(key) > 200 || strings.IndexFunc(key, unicode.IsControl) >= 0 {
		return nil, errors.New("job idempotency key must contain at most 200 printable characters")
	}
	return &key, nil
}

func normalizeSiteIDs(values []string) ([]string, error) {
	if len(values) > 200 {
		return nil, errors.New("job site scope cannot contain more than 200 sites")
	}
	unique := make(map[string]struct{}, len(values))
	for _, raw := range values {
		value := strings.TrimSpace(raw)
		if value == "" || len(value) > 128 || strings.IndexFunc(value, unicode.IsControl) >= 0 {
			return nil, errors.New("job site scope contains an invalid site ID")
		}
		unique[value] = struct{}{}
	}
	result := make([]string, 0, len(unique))
	for value := range unique {
		result = append(result, value)
	}
	sort.Strings(result)
	return result, nil
}

func (m *Module) Get(ctx context.Context, id int64) (Job, error) {
	model := new(jobModel)
	if err := m.database.NewSelect().Model(model).Where("id = ?", id).Scan(ctx); err != nil {
		return Job{}, err
	}
	return model.toJob(), nil
}

func (m *Module) List(ctx context.Context, limit int) ([]Job, error) {
	if limit < 1 || limit > 200 {
		limit = 50
	}
	models := make([]jobModel, 0, limit)
	if err := m.database.NewSelect().Model(&models).OrderExpr("id DESC").Limit(limit).Scan(ctx); err != nil {
		return nil, err
	}
	jobs := make([]Job, 0, len(models))
	for index := range models {
		jobs = append(jobs, models[index].toJob())
	}
	return jobs, nil
}

func (m *Module) EventsAfter(ctx context.Context, jobID, sequence int64) ([]Event, error) {
	models := make([]eventModel, 0)
	if err := m.database.NewSelect().Model(&models).
		Where("job_id = ?", jobID).Where("sequence > ?", sequence).OrderExpr("sequence ASC").Scan(ctx); err != nil {
		return nil, err
	}
	events := make([]Event, 0, len(models))
	for _, model := range models {
		events = append(events, Event{
			Sequence: model.Sequence, JobID: model.JobID, State: State(model.State),
			Progress: model.Progress, Message: model.Message, OccurredAt: model.OccurredAt,
		})
	}
	return events, nil
}

func (model *jobModel) toJob() Job {
	job := Job{
		ID: model.ID, Kind: model.Kind, Title: model.Title, State: State(model.State), Progress: model.Progress,
		ActorUserID: model.ActorUserID, Attempt: model.Attempt, Request: json.RawMessage(model.RequestJSON),
		CreatedAt: model.CreatedAt, UpdatedAt: model.UpdatedAt,
		StartedAt: model.StartedAt, CompletedAt: model.CompletedAt,
	}
	_ = json.Unmarshal([]byte(model.ScopeSiteIDs), &job.SiteIDs)
	if model.ResultJSON != nil {
		job.Result = json.RawMessage(*model.ResultJSON)
	}
	if model.Failure != nil {
		job.Failure = *model.Failure
	}
	return job
}
