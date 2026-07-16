package jobs

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/platform/audit"
	"github.com/uptrace/bun"
)

func (m *Module) worker(ctx context.Context) {
	defer close(m.done)
	ticker := time.NewTicker(m.config.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-m.notify:
		case <-ticker.C:
		}
		for {
			job, err := m.claim(ctx)
			if errors.Is(err, sql.ErrNoRows) {
				break
			}
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				m.logger.Error("claim durable job", "error", err)
				break
			}
			m.execute(ctx, job)
		}
	}
}

func (m *Module) claim(ctx context.Context) (jobModel, error) {
	var model jobModel
	err := m.database.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		if err := tx.NewSelect().Model(&model).Where("state = ?", StateQueued).
			OrderExpr("id ASC").Limit(1).Scan(ctx); err != nil {
			return err
		}
		now := m.now().UTC()
		model.State = string(StateRunning)
		model.StartedAt = &now
		model.UpdatedAt = now
		result, err := tx.NewUpdate().Model((*jobModel)(nil)).
			Set("state = ?", StateRunning).Set("started_at = ?", now).Set("updated_at = ?", now).
			Where("id = ?", model.ID).Where("state = ?", StateQueued).Exec(ctx)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if changed != 1 {
			return sql.ErrNoRows
		}
		event := &eventModel{JobID: model.ID, State: string(StateRunning), Progress: model.Progress, Message: "Job started.", OccurredAt: now}
		_, err = tx.NewInsert().Model(event).Exec(ctx)
		return err
	})
	return model, err
}

func (m *Module) execute(workerContext context.Context, model jobModel) {
	m.handlersMu.RLock()
	handler := m.handlers[model.Kind]
	m.handlersMu.RUnlock()
	if handler == nil {
		m.finishFailed(context.WithoutCancel(workerContext), &model, errors.New("registered job handler is unavailable"))
		return
	}

	progress := model.Progress
	report := func(next int, message string) error {
		if next < progress || next < 0 || next > 99 {
			return fmt.Errorf("job progress must move forward between 0 and 99")
		}
		if message == "" {
			return errors.New("job progress message is required")
		}
		if err := m.updateProgress(workerContext, model.ID, next, message); err != nil {
			return err
		}
		progress = next
		model.Progress = next
		return nil
	}

	result, err := handler(workerContext, json.RawMessage(model.RequestJSON), report)
	if err != nil && errors.Is(err, context.Canceled) && workerContext.Err() != nil {
		// Leave the row running during shutdown. New() deterministically recovers
		// interrupted work to queued state on the next process start.
		return
	}
	finishContext, cancel := context.WithTimeout(context.WithoutCancel(workerContext), 5*time.Second)
	defer cancel()
	if err != nil {
		m.finishFailed(finishContext, &model, err)
		return
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		m.finishFailed(finishContext, &model, fmt.Errorf("encode job result: %w", err))
		return
	}
	if err := m.finishSucceeded(finishContext, &model, string(encoded)); err != nil {
		m.logger.Error("finish durable job", "job_id", model.ID, "error", err)
	}
}

func (m *Module) updateProgress(ctx context.Context, jobID int64, progress int, message string) error {
	now := m.now().UTC()
	return m.database.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		if _, err := tx.NewUpdate().Model((*jobModel)(nil)).
			Set("progress = ?", progress).Set("updated_at = ?", now).
			Where("id = ?", jobID).Where("state = ?", StateRunning).Exec(ctx); err != nil {
			return err
		}
		event := &eventModel{JobID: jobID, State: string(StateRunning), Progress: progress, Message: message, OccurredAt: now}
		_, err := tx.NewInsert().Model(event).Exec(ctx)
		return err
	})
}

func (m *Module) finishSucceeded(ctx context.Context, model *jobModel, resultJSON string) error {
	now := m.now().UTC()
	err := m.database.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		if _, err := tx.NewUpdate().Model((*jobModel)(nil)).
			Set("state = ?", StateSucceeded).Set("progress = 100").Set("result_json = ?", resultJSON).
			Set("updated_at = ?", now).Set("completed_at = ?", now).Where("id = ?", model.ID).Exec(ctx); err != nil {
			return err
		}
		event := &eventModel{JobID: model.ID, State: string(StateSucceeded), Progress: 100, Message: "Job completed.", OccurredAt: now}
		_, err := tx.NewInsert().Model(event).Exec(ctx)
		return err
	})
	if err != nil {
		return err
	}
	m.recordAudit(ctx, audit.Entry{ActorUserID: model.ActorUserID, Action: "job.succeeded", Subject: fmt.Sprintf("job:%d", model.ID), Metadata: map[string]any{"kind": model.Kind}})
	return nil
}

func (m *Module) finishFailed(ctx context.Context, model *jobModel, failure error) {
	now := m.now().UTC()
	message := failure.Error()
	if len(message) > 500 {
		message = message[:500]
	}
	err := m.database.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		if _, err := tx.NewUpdate().Model((*jobModel)(nil)).
			Set("state = ?", StateFailed).Set("failure = ?", message).
			Set("updated_at = ?", now).Set("completed_at = ?", now).Where("id = ?", model.ID).Exec(ctx); err != nil {
			return err
		}
		event := &eventModel{JobID: model.ID, State: string(StateFailed), Progress: model.Progress, Message: "Job failed.", OccurredAt: now}
		_, err := tx.NewInsert().Model(event).Exec(ctx)
		return err
	})
	if err != nil {
		m.logger.Error("fail durable job", "job_id", model.ID, "error", err)
		return
	}
	m.recordAudit(ctx, audit.Entry{ActorUserID: model.ActorUserID, Action: "job.failed", Subject: fmt.Sprintf("job:%d", model.ID), Metadata: map[string]any{"kind": model.Kind, "failure": message}})
}

func (m *Module) recoverInterrupted(ctx context.Context) error {
	interrupted := make([]jobModel, 0)
	if err := m.database.NewSelect().Model(&interrupted).Where("state = ?", StateRunning).Scan(ctx); err != nil {
		return fmt.Errorf("find interrupted jobs: %w", err)
	}
	for index := range interrupted {
		job := &interrupted[index]
		now := m.now().UTC()
		err := m.database.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
			if _, err := tx.NewUpdate().Model((*jobModel)(nil)).Set("state = ?", StateQueued).
				Set("progress = 0").Set("updated_at = ?", now).Set("started_at = NULL").
				Where("id = ?", job.ID).Exec(ctx); err != nil {
				return err
			}
			event := &eventModel{JobID: job.ID, State: string(StateQueued), Progress: 0, Message: "Job recovered after control-plane restart.", OccurredAt: now}
			_, err := tx.NewInsert().Model(event).Exec(ctx)
			return err
		})
		if err != nil {
			return fmt.Errorf("recover job %d: %w", job.ID, err)
		}
	}
	return nil
}

func (m *Module) wake() {
	select {
	case m.notify <- struct{}{}:
	default:
	}
}

func (m *Module) recordAudit(ctx context.Context, entry audit.Entry) {
	if err := m.audit.Record(ctx, entry); err != nil {
		m.logger.Error("record job audit event", "action", entry.Action, "error", err)
	}
}
