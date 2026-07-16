package admintools

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/platform/audit"
	"github.com/nexa-panel/nexa-panel/internal/platform/jobs"
	"github.com/nexa-panel/nexa-panel/internal/platform/module"
	admintooloperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/admintools"
	"github.com/nexa-panel/nexa-panel/internal/platform/persistence"
	"github.com/nexa-panel/nexa-panel/internal/platform/secrets"
	"github.com/uptrace/bun"
)

type Module struct {
	database *bun.DB
	jobs     *jobs.Module
	operator admintooloperator.Operator
	now      func() time.Time
	cipher   secrets.Cipher
	resolver CredentialResolver
	audit    audit.Recorder
}

type Credential struct {
	Host     string
	Port     int
	Database string
	Username string
	Secret   []byte
}
type CredentialResolver func(context.Context, string, string, string) (Credential, error)
type Option func(*Module)

func WithLaunchGateway(cipher secrets.Cipher, resolver CredentialResolver, recorder audit.Recorder) Option {
	return func(module *Module) { module.cipher = cipher; module.resolver = resolver; module.audit = recorder }
}

func New(ctx context.Context, database *bun.DB, queue *jobs.Module, operator admintooloperator.Operator, options ...Option) (*Module, error) {
	if database == nil || queue == nil || operator == nil {
		return nil, errors.New("admin tool state, jobs, and operator are required")
	}
	if err := persistence.Migrate(ctx, database, "admin_tools", []string{schema, launchSchema}); err != nil {
		return nil, err
	}
	m := &Module{database: database, jobs: queue, operator: operator, now: time.Now}
	for _, option := range options {
		option(m)
	}
	if err := queue.RegisterHandler("admin-tools.plan", m.planJob); err != nil {
		return nil, err
	}
	if err := queue.RegisterHandler("admin-tools.apply", m.applyJob); err != nil {
		return nil, err
	}
	return m, nil
}
func (m *Module) Descriptor() module.Descriptor {
	return module.Descriptor{ID: "admin-tools", Name: "Database Admin Tools", Version: "0.1.0", Description: "Podman-isolated phpMyAdmin and pgAdmin with explicit resource ceilings.", Dependencies: []string{"audit", "identity", "jobs"}, RequiredCapabilities: []string{"podman", "quadlet"}, EstimatedIdleBytes: 512 * 1024}
}
func (m *Module) Register(registry module.Registry) error { return m.registerHTTP(registry) }

func (m *Module) Sync(ctx context.Context) ([]Tool, error) {
	observed, err := m.operator.Discover(ctx)
	if err != nil {
		return nil, err
	}
	now := m.now().UTC()
	for _, item := range observed {
		model := toolModel{Kind: string(item.Kind), Image: item.Image, ContainerName: item.ContainerName, Port: item.Port, MemoryMB: item.MemoryMB, PIDsLimit: item.PIDsLimit, Status: item.Status, SystemdUnit: item.SystemdUnit, OnDemand: item.OnDemand, CreatedAt: now, UpdatedAt: now}
		_, err = m.database.NewInsert().Model(&model).On("CONFLICT (kind) DO UPDATE").Set("image = EXCLUDED.image").Set("container_name = EXCLUDED.container_name").Set("port = EXCLUDED.port").Set("memory_mb = EXCLUDED.memory_mb").Set("pids_limit = EXCLUDED.pids_limit").Set("status = EXCLUDED.status").Set("systemd_unit = EXCLUDED.systemd_unit").Set("on_demand = EXCLUDED.on_demand").Set("updated_at = EXCLUDED.updated_at").Exec(ctx)
		if err != nil {
			return nil, err
		}
	}
	return m.List(ctx)
}
func (m *Module) List(ctx context.Context) ([]Tool, error) {
	models := []toolModel{}
	if err := m.database.NewSelect().Model(&models).OrderExpr("kind ASC").Scan(ctx); err != nil {
		return nil, err
	}
	items := make([]Tool, 0, len(models))
	for _, model := range models {
		items = append(items, model.toTool())
	}
	return items, nil
}
func (m *Module) RequestChange(ctx context.Context, kind admintooloperator.Kind, action admintooloperator.Action, actor *string) (Tool, jobs.Job, error) {
	if kind != admintooloperator.PHPMyAdmin && kind != admintooloperator.PGAdmin {
		return Tool{}, jobs.Job{}, errors.New("admin tool is unsupported")
	}
	if action != admintooloperator.ActionDeploy && action != admintooloperator.ActionStart && action != admintooloperator.ActionStop {
		return Tool{}, jobs.Job{}, errors.New("admin tool action is unsupported")
	}
	if _, err := m.Sync(ctx); err != nil {
		return Tool{}, jobs.Job{}, err
	}
	model, err := m.get(ctx, kind)
	if err != nil {
		return Tool{}, jobs.Job{}, err
	}
	job, err := m.jobs.Submit(ctx, "admin-tools.plan", map[string]string{"kind": string(kind), "action": string(action)}, actor)
	if err != nil {
		return model.toTool(), jobs.Job{}, err
	}
	_, err = m.database.NewUpdate().Model((*toolModel)(nil)).Set("status = ?", StatusPlanning).Set("last_job_id = ?", job.ID).Set("failure = NULL").Set("updated_at = ?", m.now().UTC()).Where("kind = ?", kind).Exec(ctx)
	model.Status = string(StatusPlanning)
	model.LastJobID = &job.ID
	return model.toTool(), job, err
}
func (m *Module) StoredPlan(ctx context.Context, kind admintooloperator.Kind) (StoredPlan, error) {
	model := new(planModel)
	if err := m.database.NewSelect().Model(model).Where("tool_kind = ?", kind).OrderExpr("created_at DESC").Limit(1).Scan(ctx); err != nil {
		return StoredPlan{}, err
	}
	return model.toStoredPlan()
}
func (m *Module) ApplyPlan(ctx context.Context, kind admintooloperator.Kind, actor *string) (jobs.Job, error) {
	plan, err := m.StoredPlan(ctx, kind)
	if err != nil {
		return jobs.Job{}, errors.New("a reviewed admin tool plan is not ready")
	}
	if m.now().UTC().After(plan.ExpiresAt) {
		return jobs.Job{}, errors.New("admin tool plan expired")
	}
	model, err := m.get(ctx, kind)
	if err != nil || Status(model.Status) != StatusPlanReady {
		return jobs.Job{}, errors.New("only a plan-ready admin tool can be applied")
	}
	job, err := m.jobs.Submit(ctx, "admin-tools.apply", map[string]string{"planId": plan.ID}, actor)
	if err != nil {
		return jobs.Job{}, err
	}
	_, err = m.database.NewUpdate().Model((*toolModel)(nil)).Set("status = ?", StatusApplying).Set("last_job_id = ?", job.ID).Set("updated_at = ?", m.now().UTC()).Where("kind = ?", kind).Exec(ctx)
	return job, err
}
func (m *Module) planJob(ctx context.Context, raw json.RawMessage, report func(int, string) error) (any, error) {
	var request struct {
		Kind   string `json:"kind"`
		Action string `json:"action"`
	}
	if json.Unmarshal(raw, &request) != nil {
		return nil, errors.New("invalid admin tool planning request")
	}
	model, err := m.get(ctx, admintooloperator.Kind(request.Kind))
	if err != nil {
		return nil, err
	}
	_ = report(25, "Inspecting the Podman tool service.")
	plan, err := m.operator.Plan(ctx, admintooloperator.Change{Action: admintooloperator.Action(request.Action), Tool: model.toTool().Tool})
	if err != nil {
		m.fail(context.WithoutCancel(ctx), model.Kind, err)
		return nil, err
	}
	encoded, _ := json.Marshal(plan)
	stored := planModel{ID: randomID(), ToolKind: model.Kind, Operation: request.Action, PlanJSON: string(encoded), CreatedAt: m.now().UTC(), ExpiresAt: plan.ExpiresAt}
	if _, err = m.database.NewInsert().Model(&stored).Exec(ctx); err != nil {
		return nil, err
	}
	_, err = m.database.NewUpdate().Model((*toolModel)(nil)).Set("status = ?", StatusPlanReady).Set("updated_at = ?", m.now().UTC()).Where("kind = ?", model.Kind).Exec(ctx)
	if err != nil {
		return nil, err
	}
	_ = report(95, "Admin tool plan is ready for approval.")
	return stored.toStoredPlan()
}
func (m *Module) applyJob(ctx context.Context, raw json.RawMessage, report func(int, string) error) (any, error) {
	var request struct {
		PlanID string `json:"planId"`
	}
	if json.Unmarshal(raw, &request) != nil {
		return nil, errors.New("invalid admin tool apply request")
	}
	model := new(planModel)
	if err := m.database.NewSelect().Model(model).Where("id = ?", request.PlanID).Scan(ctx); err != nil {
		return nil, err
	}
	stored, err := model.toStoredPlan()
	if err != nil {
		return nil, err
	}
	_ = report(25, "Applying the signed Podman tool plan.")
	observation, err := m.operator.Apply(ctx, admintooloperator.Execution{Plan: stored.AgentPlan})
	if err != nil {
		m.fail(context.WithoutCancel(ctx), model.ToolKind, err)
		return nil, err
	}
	status := StatusActive
	if stored.Operation == admintooloperator.ActionStop {
		status = StatusStopped
	}
	_, err = m.database.NewUpdate().Model((*toolModel)(nil)).Set("image = ?", observation.Tool.Image).Set("port = ?", observation.Tool.Port).Set("memory_mb = ?", observation.Tool.MemoryMB).Set("pids_limit = ?", observation.Tool.PIDsLimit).Set("status = ?", status).Set("failure = NULL").Set("updated_at = ?", m.now().UTC()).Where("kind = ?", model.ToolKind).Exec(ctx)
	if err != nil {
		return nil, err
	}
	_ = report(95, "Admin tool service state is verified.")
	return observation, nil
}
func (m *Module) get(ctx context.Context, kind admintooloperator.Kind) (toolModel, error) {
	var model toolModel
	err := m.database.NewSelect().Model(&model).Where("kind = ?", kind).Scan(ctx)
	return model, err
}
func (m *Module) fail(ctx context.Context, kind string, failure error) {
	message := failure.Error()
	if len(message) > 300 {
		message = message[:300]
	}
	_, _ = m.database.NewUpdate().Model((*toolModel)(nil)).Set("status = ?", StatusFailed).Set("failure = ?", message).Set("updated_at = ?", m.now().UTC()).Where("kind = ?", kind).Exec(ctx)
}
func randomID() string {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		panic(err)
	}
	return hex.EncodeToString(value)
}
