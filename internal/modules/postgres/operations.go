package postgres

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/nexa-panel/nexa-panel/internal/platform/jobs"
	postgresoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/postgres"
)

func (m *Module) StoredPlan(ctx context.Context, resourceType, resourceID string) (StoredPlan, error) {
	model := new(planModel)
	if err := m.database.NewSelect().Model(model).Where("resource_type = ?", resourceType).Where("resource_id = ?", resourceID).OrderExpr("created_at DESC").Limit(1).Scan(ctx); err != nil {
		return StoredPlan{}, err
	}
	return model.toStoredPlan()
}

func (m *Module) ApplyPlan(ctx context.Context, resourceType, resourceID string, actor *string) (jobs.Job, error) {
	plan, err := m.StoredPlan(ctx, resourceType, resourceID)
	if err != nil {
		return jobs.Job{}, errors.New("a reviewed PostgreSQL plan is not ready")
	}
	if m.now().UTC().After(plan.ExpiresAt) {
		return jobs.Job{}, errors.New("PostgreSQL plan has expired and must be refreshed")
	}
	status, err := m.resourceStatus(ctx, resourceType, resourceID)
	if err != nil || status != StatusPlanReady {
		return jobs.Job{}, errors.New("only a plan-ready PostgreSQL resource can be applied")
	}
	job, err := m.jobs.SubmitTitled(ctx, "postgresql.apply", "Apply PostgreSQL "+resourceType, map[string]string{"planId": plan.ID}, actor)
	if err != nil {
		return jobs.Job{}, err
	}
	applyStatus := StatusApplying
	if plan.Operation == postgresoperator.ActionCreateBackup {
		applyStatus = StatusBackingUp
	} else if plan.Operation == postgresoperator.ActionRestoreBackup {
		applyStatus = StatusRestoring
	}
	_, err = m.attachJob(ctx, resourceType, resourceID, job.ID, applyStatus)
	return job, err
}

func (m *Module) submitPlan(ctx context.Context, resourceType, resourceID string, action postgresoperator.Action, actor *string) (jobs.Job, error) {
	return m.jobs.SubmitTitled(ctx, "postgresql.plan", "Plan PostgreSQL "+resourceType, map[string]string{"resourceType": resourceType, "resourceId": resourceID, "action": string(action)}, actor)
}

func (m *Module) planJob(ctx context.Context, raw json.RawMessage, report func(int, string) error) (any, error) {
	var request struct {
		ResourceType string `json:"resourceType"`
		ResourceID   string `json:"resourceId"`
		Action       string `json:"action"`
	}
	if json.Unmarshal(raw, &request) != nil || request.ResourceID == "" {
		return nil, errors.New("invalid PostgreSQL planning request")
	}
	if err := report(20, "Loading managed PostgreSQL desired state."); err != nil {
		return nil, err
	}
	change, err := m.changeFor(ctx, request.ResourceType, request.ResourceID, postgresoperator.Action(request.Action))
	if err != nil {
		m.failResource(context.WithoutCancel(ctx), request.ResourceType, request.ResourceID, err)
		return nil, err
	}
	if err := report(50, "Requesting an agent-signed PostgreSQL plan."); err != nil {
		return nil, err
	}
	agentPlan, err := m.operator.Plan(ctx, change)
	if err != nil {
		m.failResource(context.WithoutCancel(ctx), request.ResourceType, request.ResourceID, err)
		return nil, err
	}
	encoded, _ := json.Marshal(agentPlan)
	now := m.now().UTC()
	stored := &planModel{ID: randomResourceID("plan"), ResourceType: request.ResourceType, ResourceID: request.ResourceID, Operation: request.Action, PlanJSON: string(encoded), CreatedAt: now, ExpiresAt: agentPlan.ExpiresAt}
	if _, err := m.database.NewInsert().Model(stored).Exec(ctx); err != nil {
		return nil, err
	}
	if _, err := m.setResourceStatus(ctx, request.ResourceType, request.ResourceID, StatusPlanReady, nil); err != nil {
		return nil, err
	}
	if err := report(95, "PostgreSQL plan is ready for approval."); err != nil {
		return nil, err
	}
	return map[string]any{"planId": stored.ID, "resourceType": request.ResourceType, "resourceId": request.ResourceID, "expiresAt": agentPlan.ExpiresAt}, nil
}
