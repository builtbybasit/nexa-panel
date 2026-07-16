package schedules

import (
	"context"
	"errors"

	"encoding/json"
	"fmt"

	"net/http"

	"github.com/nexa-panel/nexa-panel/internal/platform/identity"
	scheduleoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/schedules"
	"github.com/uptrace/bun"
)

// runTimeoutCapSeconds bounds a manual run regardless of the task's own
// timeout: the single serial job worker must never be held for hours.
const runTimeoutCapSeconds = 900

// Job payloads embed the Scope the HTTP handler already validated: job
// execution has no requesting user, so all authorization happens up front.
type planPayload struct {
	Task    scheduleoperator.Task `json:"task"`
	Removal bool                  `json:"removal"`
}

type runPayload struct {
	Task scheduleoperator.Task `json:"task"`
}

// submitPlanMutation queues an apply or rollback of the persisted signed plan
// and moves the task into the transitional status.
func (m *Module) submitPlanMutation(w http.ResponseWriter, r *http.Request, kind string, status Status, task Task, plan scheduleoperator.Plan, user identity.User) {
	job, err := m.jobs.Submit(r.Context(), kind, plan, &user.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "job_submission_failed", "The schedule operation could not be queued.")
		return
	}
	now := m.now().UTC()
	if _, err := m.database.NewUpdate().Model((*taskModel)(nil)).Set("status = ?", status).Set("last_job_id = ?", job.ID).Set("updated_at = ?", now).Where("id = ?", task.ID).Exec(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "schedule_update_failed", "The queued operation could not be attached to the task.")
		return
	}
	w.Header().Set("Location", fmt.Sprintf("/api/v1/jobs/%d", job.ID))
	writeJSON(w, http.StatusAccepted, job)
}

func (m *Module) planJob(ctx context.Context, request json.RawMessage, report func(int, string) error) (any, error) {
	var payload planPayload
	if err := json.Unmarshal(request, &payload); err != nil || payload.Task.ID == "" {
		return nil, errors.New("invalid persisted schedule planning request")
	}
	if err := report(25, "Rendering the wrapper script and cron entry."); err != nil {
		return nil, err
	}
	plan, err := m.operator.Plan(ctx, payload.Task, payload.Removal)
	if err != nil {
		m.markFailed(context.WithoutCancel(ctx), payload.Task.ID, err)
		return nil, err
	}
	encoded, err := json.Marshal(plan)
	if err != nil {
		m.markFailed(context.WithoutCancel(ctx), payload.Task.ID, err)
		return nil, err
	}
	if err := report(70, "Persisting the signed schedule plan."); err != nil {
		return nil, err
	}
	now := m.now().UTC()
	planRow := &planModel{TaskID: payload.Task.ID, PlanJSON: string(encoded), CreatedAt: now, ExpiresAt: plan.ExpiresAt}
	err = m.database.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		if _, err := tx.NewInsert().Model(planRow).On("CONFLICT (task_id) DO UPDATE").
			Set("plan_json = EXCLUDED.plan_json").Set("created_at = EXCLUDED.created_at").Set("expires_at = EXCLUDED.expires_at").Exec(ctx); err != nil {
			return err
		}
		_, err := tx.NewUpdate().Model((*taskModel)(nil)).Set("status = ?", StatusPlanReady).
			Set("failure = NULL").Set("updated_at = ?", now).Where("id = ?", payload.Task.ID).Exec(ctx)
		return err
	})
	if err != nil {
		m.markFailed(context.WithoutCancel(ctx), payload.Task.ID, err)
		return nil, err
	}
	if err := report(95, "The schedule plan is ready for review."); err != nil {
		return nil, err
	}
	return map[string]any{"taskId": payload.Task.ID, "removal": payload.Removal, "artifactCount": len(plan.Artifacts), "expiresAt": plan.ExpiresAt}, nil
}

func (m *Module) applyJob(ctx context.Context, request json.RawMessage, report func(int, string) error) (any, error) {
	var plan scheduleoperator.Plan
	if err := json.Unmarshal(request, &plan); err != nil || plan.Task.ID == "" {
		return nil, errors.New("invalid persisted schedule apply plan")
	}
	if err := report(25, "Checking the signed schedule plan against the node state."); err != nil {
		return nil, err
	}
	observation, err := m.operator.Apply(ctx, plan)
	if err != nil {
		m.markFailed(context.WithoutCancel(ctx), plan.Task.ID, err)
		return nil, err
	}
	if err := report(85, "The wrapper script and cron entry match the plan."); err != nil {
		return nil, err
	}
	model := new(taskModel)
	if err := m.database.NewSelect().Model(model).Where("id = ?", plan.Task.ID).Scan(ctx); err != nil {
		return nil, err
	}
	if model.PendingRemoval {
		// A successful removal apply retires the task entirely.
		if _, err := m.database.NewDelete().Model((*planModel)(nil)).Where("task_id = ?", plan.Task.ID).Exec(ctx); err != nil {
			return nil, err
		}
		if _, err := m.database.NewDelete().Model((*taskModel)(nil)).Where("id = ?", plan.Task.ID).Exec(ctx); err != nil {
			return nil, err
		}
		return observation, nil
	}
	if _, err := m.database.NewUpdate().Model((*taskModel)(nil)).Set("status = ?", StatusActive).Set("failure = NULL").Set("updated_at = ?", m.now().UTC()).Where("id = ?", plan.Task.ID).Exec(ctx); err != nil {
		return nil, err
	}
	return observation, nil
}

func (m *Module) rollbackJob(ctx context.Context, request json.RawMessage, report func(int, string) error) (any, error) {
	var plan scheduleoperator.Plan
	if err := json.Unmarshal(request, &plan); err != nil || plan.Task.ID == "" {
		return nil, errors.New("invalid persisted schedule rollback plan")
	}
	if err := report(30, "Checking rollback drift and captured state."); err != nil {
		return nil, err
	}
	observation, err := m.operator.Rollback(ctx, plan)
	if err != nil {
		m.markFailed(context.WithoutCancel(ctx), plan.Task.ID, err)
		return nil, err
	}
	if err := report(90, "The previous wrapper script and cron entry are restored."); err != nil {
		return nil, err
	}
	now := m.now().UTC()
	if _, err := m.database.NewUpdate().Model((*taskModel)(nil)).Set("status = ?", StatusRolledBack).Set("failure = NULL").Set("updated_at = ?", now).Where("id = ?", plan.Task.ID).Exec(ctx); err != nil {
		return nil, err
	}
	return observation, nil
}

func (m *Module) runJob(ctx context.Context, request json.RawMessage, report func(int, string) error) (any, error) {
	var payload runPayload
	if err := json.Unmarshal(request, &payload); err != nil || payload.Task.ID == "" {
		return nil, errors.New("invalid persisted schedule run request")
	}
	// The wrapper enforces the task's own timeout; the worker additionally
	// caps a manual run so the serial queue cannot stall for a day.
	if payload.Task.TimeoutSeconds > runTimeoutCapSeconds {
		payload.Task.TimeoutSeconds = runTimeoutCapSeconds
	}
	if err := report(20, "Running the task as the site account."); err != nil {
		return nil, err
	}
	result, err := m.operator.Run(ctx, payload.Task)
	if err != nil {
		return nil, err
	}
	if err := report(90, "The run finished and its output tail was captured."); err != nil {
		return nil, err
	}
	return result, nil
}
