package jobs

import (
	"context"
	"encoding/json"
	"github.com/nexa-panel/nexa-panel/internal/platform/audit"
	"github.com/uptrace/bun"

	"fmt"
)

func (m *Module) Submit(ctx context.Context, kind string, request any, actorUserID *string) (Job, error) {
	m.handlersMu.RLock()
	_, supported := m.handlers[kind]
	m.handlersMu.RUnlock()
	if !supported {
		return Job{}, fmt.Errorf("unsupported job kind %q", kind)
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		return Job{}, fmt.Errorf("encode job request: %w", err)
	}
	now := m.now().UTC()
	model := &jobModel{
		Kind: kind, State: string(StateQueued), Progress: 0, ActorUserID: actorUserID,
		RequestJSON: string(encoded), CreatedAt: now, UpdatedAt: now,
	}
	err = m.database.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		if _, err := tx.NewInsert().Model(model).Exec(ctx); err != nil {
			return err
		}
		event := &eventModel{JobID: model.ID, State: string(StateQueued), Progress: 0, Message: "Job queued.", OccurredAt: now}
		_, err := tx.NewInsert().Model(event).Exec(ctx)
		return err
	})
	if err != nil {
		return Job{}, fmt.Errorf("submit job: %w", err)
	}
	m.wake()
	job := model.toJob()
	m.recordAudit(ctx, audit.Entry{ActorUserID: actorUserID, Action: "job.submitted", Subject: fmt.Sprintf("job:%d", job.ID), Metadata: map[string]any{"kind": kind}})
	return job, nil
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
		ID: model.ID, Kind: model.Kind, State: State(model.State), Progress: model.Progress,
		ActorUserID: model.ActorUserID, Request: json.RawMessage(model.RequestJSON),
		CreatedAt: model.CreatedAt, UpdatedAt: model.UpdatedAt,
		StartedAt: model.StartedAt, CompletedAt: model.CompletedAt,
	}
	if model.ResultJSON != nil {
		job.Result = json.RawMessage(*model.ResultJSON)
	}
	if model.Failure != nil {
		job.Failure = *model.Failure
	}
	return job
}
