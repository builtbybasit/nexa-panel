package jobs

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"runtime/debug"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/platform/audit"
	"github.com/uptrace/bun"
)

func (m *Module) worker(ctx context.Context) {
	defer close(m.done)
	ticker := time.NewTicker(m.config.PollInterval)
	defer ticker.Stop()
	pruneTicker := time.NewTicker(m.config.PruneInterval)
	defer pruneTicker.Stop()
	// Sweep once on startup so a node that was down past the retention window
	// reclaims accumulated history immediately rather than waiting a full interval.
	m.pruneRetired(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-m.notify:
		case <-ticker.C:
		case <-pruneTicker.C:
			m.pruneRetired(ctx)
			continue
		}
		if err := m.recoverExpired(ctx); err != nil {
			if ctx.Err() != nil {
				return
			}
			m.logger.Error("recover expired job leases", "error", err)
			continue
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
		leaseToken := randomToken()
		leaseExpiresAt := now.Add(m.config.LeaseDuration)
		model.State = string(StateRunning)
		model.StartedAt = &now
		model.UpdatedAt = now
		model.Attempt++
		model.LeaseOwner = &m.workerID
		model.LeaseToken = &leaseToken
		model.LeaseExpiresAt = &leaseExpiresAt
		result, err := tx.NewUpdate().Model((*jobModel)(nil)).
			Set("state = ?", StateRunning).Set("started_at = ?", now).Set("updated_at = ?", now).
			Set("attempt = attempt + 1").Set("lease_owner = ?", m.workerID).
			Set("lease_token = ?", leaseToken).Set("lease_expires_at = ?", leaseExpiresAt).
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

// invokeHandler runs a job handler and converts any panic into a returned
// error so it flows through the same failure path as a normally returned error,
// keeping the single worker goroutine and the whole process alive.
func (m *Module) invokeHandler(ctx context.Context, handler Handler, model *jobModel, report func(int, string) error) (result any, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			m.panics.Add(1)
			m.logger.Error("job handler panic recovered", "job_id", model.ID, "kind", model.Kind, "error", recovered, "stack", string(debug.Stack()))
			err = fmt.Errorf("job handler panicked: %v", recovered)
		}
	}()
	return handler(ctx, json.RawMessage(model.RequestJSON), report)
}

func (m *Module) execute(workerContext context.Context, model jobModel) {
	m.handlersMu.RLock()
	registration, registered := m.handlers[model.Kind]
	m.handlersMu.RUnlock()
	if !registered || registration.handler == nil {
		m.finishFailed(context.WithoutCancel(workerContext), &model, errors.New("registered job handler is unavailable"))
		return
	}
	if model.LeaseToken == nil {
		m.logger.Error("claimed job has no lease token", "job_id", model.ID)
		return
	}

	executionContext, cancelExecution := context.WithCancel(workerContext)
	heartbeatDone := make(chan struct{})
	leaseLost := make(chan error, 1)
	go func() {
		defer close(heartbeatDone)
		if err := m.heartbeatLease(executionContext, model.ID, *model.LeaseToken); err != nil && executionContext.Err() == nil {
			leaseLost <- err
			cancelExecution()
		}
	}()

	progress := model.Progress
	report := func(next int, message string) error {
		if next < progress || next < 0 || next > 99 {
			return fmt.Errorf("job progress must move forward between 0 and 99")
		}
		if message == "" {
			return errors.New("job progress message is required")
		}
		if err := m.updateProgress(executionContext, model.ID, *model.LeaseToken, next, message); err != nil {
			return err
		}
		progress = next
		model.Progress = next
		return nil
	}

	result, err := m.invokeHandler(executionContext, registration.handler, &model, report)
	cancelExecution()
	<-heartbeatDone
	select {
	case leaseErr := <-leaseLost:
		m.logger.Error("job lease lost during execution", "job_id", model.ID, "error", leaseErr)
		return
	default:
	}
	if err != nil && errors.Is(err, context.Canceled) && workerContext.Err() != nil {
		// Leave the row running during shutdown. Its lease and persisted recovery
		// policy decide what happens after ownership expires.
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

func (m *Module) updateProgress(ctx context.Context, jobID int64, leaseToken string, progress int, message string) error {
	now := m.now().UTC()
	return m.database.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		result, err := tx.NewUpdate().Model((*jobModel)(nil)).
			Set("progress = ?", progress).Set("updated_at = ?", now).
			Set("lease_expires_at = ?", now.Add(m.config.LeaseDuration)).
			Where("id = ?", jobID).Where("state = ?", StateRunning).Where("lease_token = ?", leaseToken).Exec(ctx)
		if err != nil {
			return err
		}
		if changed, err := result.RowsAffected(); err != nil || changed != 1 {
			return ErrLeaseLost
		}
		event := &eventModel{JobID: jobID, State: string(StateRunning), Progress: progress, Message: message, OccurredAt: now}
		_, err = tx.NewInsert().Model(event).Exec(ctx)
		return err
	})
}

func (m *Module) finishSucceeded(ctx context.Context, model *jobModel, resultJSON string) error {
	now := m.now().UTC()
	err := m.database.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		result, err := tx.NewUpdate().Model((*jobModel)(nil)).
			Set("state = ?", StateSucceeded).Set("progress = 100").Set("result_json = ?", resultJSON).
			Set("updated_at = ?", now).Set("completed_at = ?", now).
			Set("lease_owner = NULL").Set("lease_token = NULL").Set("lease_expires_at = NULL").
			Where("id = ?", model.ID).Where("state = ?", StateRunning).Where("lease_token = ?", *model.LeaseToken).Exec(ctx)
		if err != nil {
			return err
		}
		if changed, err := result.RowsAffected(); err != nil || changed != 1 {
			return ErrLeaseLost
		}
		event := &eventModel{JobID: model.ID, State: string(StateSucceeded), Progress: 100, Message: "Job completed.", OccurredAt: now}
		_, err = tx.NewInsert().Model(event).Exec(ctx)
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
		result, err := tx.NewUpdate().Model((*jobModel)(nil)).
			Set("state = ?", StateFailed).Set("failure = ?", message).
			Set("updated_at = ?", now).Set("completed_at = ?", now).
			Set("lease_owner = NULL").Set("lease_token = NULL").Set("lease_expires_at = NULL").
			Where("id = ?", model.ID).Where("state = ?", StateRunning).Where("lease_token = ?", *model.LeaseToken).Exec(ctx)
		if err != nil {
			return err
		}
		if changed, err := result.RowsAffected(); err != nil || changed != 1 {
			return ErrLeaseLost
		}
		event := &eventModel{JobID: model.ID, State: string(StateFailed), Progress: model.Progress, Message: "Job failed.", OccurredAt: now}
		_, err = tx.NewInsert().Model(event).Exec(ctx)
		return err
	})
	if err != nil {
		m.logger.Error("fail durable job", "job_id", model.ID, "error", err)
		return
	}
	m.recordAudit(ctx, audit.Entry{ActorUserID: model.ActorUserID, Action: "job.failed", Subject: fmt.Sprintf("job:%d", model.ID), Metadata: map[string]any{"kind": model.Kind, "failure": message}})
}

var ErrLeaseLost = errors.New("job worker lease is no longer owned")

func (m *Module) heartbeatLease(ctx context.Context, jobID int64, leaseToken string) error {
	interval := m.config.LeaseDuration / 3
	if interval <= 0 {
		interval = time.Millisecond
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			now := m.now().UTC()
			result, err := m.database.NewUpdate().Model((*jobModel)(nil)).
				Set("updated_at = ?", now).Set("lease_expires_at = ?", now.Add(m.config.LeaseDuration)).
				Where("id = ?", jobID).Where("state = ?", StateRunning).Where("lease_token = ?", leaseToken).Exec(ctx)
			if err != nil {
				return err
			}
			if changed, err := result.RowsAffected(); err != nil || changed != 1 {
				return ErrLeaseLost
			}
		}
	}
}

func (m *Module) recoverExpired(ctx context.Context) error {
	interrupted := make([]jobModel, 0)
	now := m.now().UTC()
	if err := m.database.NewSelect().Model(&interrupted).Where("state = ?", StateRunning).
		Where("lease_expires_at IS NULL OR lease_expires_at <= ?", now).Scan(ctx); err != nil {
		return fmt.Errorf("find interrupted jobs: %w", err)
	}
	for index := range interrupted {
		job := &interrupted[index]
		err := m.database.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
			if RecoveryPolicy(job.RecoveryPolicy) == RecoveryRetry {
				result, err := tx.NewUpdate().Model((*jobModel)(nil)).Set("state = ?", StateQueued).
					Set("progress = 0").Set("updated_at = ?", now).Set("started_at = NULL").
					Set("lease_owner = NULL").Set("lease_token = NULL").Set("lease_expires_at = NULL").
					Where("id = ?", job.ID).Where("state = ?", StateRunning).
					Where("lease_expires_at IS NULL OR lease_expires_at <= ?", now).Exec(ctx)
				if err != nil {
					return err
				}
				if changed, err := result.RowsAffected(); err != nil || changed != 1 {
					return nil
				}
				event := &eventModel{JobID: job.ID, State: string(StateQueued), Progress: 0, Message: "Job lease expired; retrying idempotent work.", OccurredAt: now}
				_, err = tx.NewInsert().Model(event).Exec(ctx)
				return err
			}

			failure := "Job interrupted after its worker lease expired; reconcile the target before retrying."
			result, err := tx.NewUpdate().Model((*jobModel)(nil)).Set("state = ?", StateFailed).
				Set("failure = ?", failure).Set("updated_at = ?", now).Set("completed_at = ?", now).
				Set("lease_owner = NULL").Set("lease_token = NULL").Set("lease_expires_at = NULL").
				Where("id = ?", job.ID).Where("state = ?", StateRunning).
				Where("lease_expires_at IS NULL OR lease_expires_at <= ?", now).Exec(ctx)
			if err != nil {
				return err
			}
			if changed, err := result.RowsAffected(); err != nil || changed != 1 {
				return nil
			}
			event := &eventModel{JobID: job.ID, State: string(StateFailed), Progress: job.Progress, Message: "Job failed after its worker lease expired.", OccurredAt: now}
			_, err = tx.NewInsert().Model(event).Exec(ctx)
			return err
		})
		if err != nil {
			return fmt.Errorf("recover job %d: %w", job.ID, err)
		}
	}
	return nil
}

// pruneRetired deletes terminal jobs whose completion is older than the
// retention window, along with their events. Queued and running rows are never
// eligible, so in-flight work and its live event stream are untouched. Events
// are removed explicitly in the same transaction rather than relying on the
// ON DELETE CASCADE foreign key, so the sweep is correct even if a connection
// has foreign-key enforcement off. Failures are logged, not fatal: pruning is
// background hygiene and the next sweep retries.
func (m *Module) pruneRetired(ctx context.Context) {
	if m.config.RetentionPeriod <= 0 {
		return
	}
	cutoff := m.now().UTC().Add(-m.config.RetentionPeriod)
	terminal := []string{string(StateSucceeded), string(StateFailed)}
	var removed int64
	err := m.database.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		doomed := tx.NewSelect().Model((*jobModel)(nil)).Column("id").
			Where("state IN (?)", bun.List(terminal)).
			Where("completed_at IS NOT NULL AND completed_at < ?", cutoff)
		if _, err := tx.NewDelete().Model((*eventModel)(nil)).
			Where("job_id IN (?)", doomed).Exec(ctx); err != nil {
			return err
		}
		result, err := tx.NewDelete().Model((*jobModel)(nil)).
			Where("state IN (?)", bun.List(terminal)).
			Where("completed_at IS NOT NULL AND completed_at < ?", cutoff).Exec(ctx)
		if err != nil {
			return err
		}
		removed, err = result.RowsAffected()
		return err
	})
	if err != nil {
		if ctx.Err() == nil {
			m.logger.Error("prune retired jobs", "error", err)
		}
		return
	}
	if removed > 0 {
		m.logger.Info("pruned retired jobs", "removed", removed, "olderThan", cutoff)
	}
}

func (m *Module) wake() {
	select {
	case m.notify <- struct{}{}:
	default:
	}
}

// recordAudit writes the generic queue bookkeeping entry. Job lifecycle events
// are high-volume and are not the security record of a change — the modules
// record their own targeted entry — so they stay best-effort.
func (m *Module) recordAudit(ctx context.Context, entry audit.Entry) {
	m.Audit().RecordBestEffort(ctx, entry)
}
