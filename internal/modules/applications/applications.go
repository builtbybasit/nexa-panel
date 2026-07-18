// Package applications is the control-plane feature module behind the
// Applications page. It renders the installable catalog joined with observed
// package state, and orchestrates install/remove as durable plan/apply jobs
// through the privileged packages operator. phpMyAdmin and pgAdmin are surfaced
// read-only from the admin-tools module rather than installed here.
package applications

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/platform/jobs"
	"github.com/nexa-panel/nexa-panel/internal/platform/module"
	packagesoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/packages"
	"github.com/nexa-panel/nexa-panel/internal/platform/persistence"
	"github.com/uptrace/bun"
)

type Module struct {
	database   *bun.DB
	jobs       *jobs.Module
	operator   packagesoperator.Operator
	adminTools AdminToolsReader
	now        func() time.Time
}

func New(ctx context.Context, database *bun.DB, queue *jobs.Module, operator packagesoperator.Operator, adminTools AdminToolsReader) (*Module, error) {
	if database == nil || queue == nil || operator == nil {
		return nil, errors.New("applications state, jobs, and operator are required")
	}
	if err := persistence.Migrate(ctx, database, "applications", []string{schema}); err != nil {
		return nil, err
	}
	m := &Module{database: database, jobs: queue, operator: operator, adminTools: adminTools, now: time.Now}
	if err := queue.RegisterHandler("applications.plan", m.planJob); err != nil {
		return nil, err
	}
	if err := queue.RegisterHandler("applications.apply", m.applyJob); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *Module) Descriptor() module.Descriptor {
	return module.Descriptor{
		ID: "applications", Name: "Applications", Version: "0.1.0",
		Description:          "Install and manage PHP, PostgreSQL, Node.js, Composer, and database web clients.",
		Dependencies:         []string{"identity", "jobs", "admin-tools"},
		RequiredCapabilities: []string{"apt"},
		EstimatedIdleBytes:   512 * 1024,
	}
}

func (m *Module) Register(registry module.Registry) error { return m.registerHTTP(registry) }

// List returns the full catalog with live status. Managed apps reflect observed
// package state unless a workflow (plan/apply) is in progress; web clients are
// appended read-only from the admin-tools module.
func (m *Module) List(ctx context.Context) ([]Application, error) {
	installed, err := m.operator.Discover(ctx)
	if err != nil {
		return nil, err
	}
	installedByName := make(map[string]packagesoperator.InstalledPackage, len(installed))
	for _, item := range installed {
		installedByName[item.Name] = item
	}
	catalog, err := m.operator.Catalog(ctx)
	if err != nil {
		return nil, err
	}
	now := m.now().UTC()
	apps := []Application{}
	for _, entry := range catalog {
		id := catalogID(entry.App, entry.Version)
		model, err := m.getOrCreate(ctx, id, entry, now)
		if err != nil {
			return nil, err
		}
		// Join on the operator's identity, not the first package name: MySQL and
		// MariaDB ship one package name across every series, so a name-based join
		// would report all of a vendor's versions installed at once.
		observed, ok := installedByName[entry.Identity]
		isInstalled := ok && observed.Installed
		app := Application{
			ID: id, App: entry.App, Version: entry.Version, Label: entry.Label,
			Summary: entry.Summary, Category: entry.Category, Managed: true,
			Status:    displayStatus(Status(model.Status), isInstalled),
			LastJobID: model.LastJobID, Failure: pointerString(model.Failure),
		}
		if isInstalled {
			app.InstalledVersion = observed.Version
		}
		apps = append(apps, app)
	}
	if m.adminTools != nil {
		if tools, err := m.adminTools.List(ctx); err == nil {
			for _, tool := range tools {
				apps = append(apps, webClientApplication(tool))
			}
		}
	}
	return apps, nil
}

// displayStatus reconciles the persisted workflow status with observed install
// state: in-flight workflow states win; otherwise installed state decides.
func displayStatus(status Status, installed bool) string {
	switch status {
	case StatusPlanning, StatusPlanReady, StatusInstalling, StatusRemoving, StatusFailed:
		return string(status)
	default:
		if installed {
			return string(StatusInstalled)
		}
		return string(StatusAvailable)
	}
}

// RequestChange queues a plan job for an install or remove of a catalog app.
func (m *Module) RequestChange(ctx context.Context, id string, action packagesoperator.Action, actor *string) (Application, jobs.Job, error) {
	entry, ok, err := m.catalogByID(ctx, id)
	if err != nil {
		return Application{}, jobs.Job{}, err
	}
	if !ok {
		return Application{}, jobs.Job{}, errors.New("application is not installable from this page")
	}
	if action != packagesoperator.ActionInstall && action != packagesoperator.ActionRemove {
		return Application{}, jobs.Job{}, errors.New("application action must be install or remove")
	}
	now := m.now().UTC()
	if _, err := m.getOrCreate(ctx, id, entry, now); err != nil {
		return Application{}, jobs.Job{}, err
	}
	verb := "Install"
	if action == packagesoperator.ActionRemove {
		verb = "Remove"
	}
	job, err := m.jobs.SubmitTitled(ctx, "applications.plan", verb+" "+entry.Label, map[string]string{"id": id, "action": string(action)}, actor)
	if err != nil {
		return Application{}, jobs.Job{}, err
	}
	_, err = m.database.NewUpdate().Model((*applicationModel)(nil)).
		Set("status = ?", StatusPlanning).Set("last_job_id = ?", job.ID).
		Set("failure = NULL").Set("updated_at = ?", now).Where("id = ?", id).Exec(ctx)
	if err != nil {
		return Application{}, jobs.Job{}, err
	}
	app := Application{ID: id, App: entry.App, Version: entry.Version, Label: entry.Label, Summary: entry.Summary, Category: entry.Category, Managed: true, Status: string(StatusPlanning), LastJobID: &job.ID}
	return app, job, nil
}

// StoredPlan returns the most recent persisted plan for an application.
func (m *Module) StoredPlan(ctx context.Context, id string) (StoredPlan, error) {
	model := new(planModel)
	if err := m.database.NewSelect().Model(model).Where("application_id = ?", id).OrderExpr("created_at DESC").Limit(1).Scan(ctx); err != nil {
		return StoredPlan{}, err
	}
	return model.toStoredPlan()
}

// ApplyPlan validates plan freshness and status, then queues an apply job.
func (m *Module) ApplyPlan(ctx context.Context, id string, actor *string) (jobs.Job, error) {
	plan, err := m.StoredPlan(ctx, id)
	if err != nil {
		return jobs.Job{}, errors.New("a reviewed application plan is not ready")
	}
	if m.now().UTC().After(plan.ExpiresAt) {
		return jobs.Job{}, errors.New("application plan expired")
	}
	model, err := m.get(ctx, id)
	if err != nil || Status(model.Status) != StatusPlanReady {
		return jobs.Job{}, errors.New("only a plan-ready application can be applied")
	}
	verb := "Install"
	if plan.Operation == packagesoperator.ActionRemove {
		verb = "Remove"
	}
	job, err := m.jobs.SubmitTitled(ctx, "applications.apply", verb+" "+id, map[string]string{"planId": plan.ID}, actor)
	if err != nil {
		return jobs.Job{}, err
	}
	status := StatusInstalling
	if plan.Operation == packagesoperator.ActionRemove {
		status = StatusRemoving
	}
	_, err = m.database.NewUpdate().Model((*applicationModel)(nil)).
		Set("status = ?", status).Set("last_job_id = ?", job.ID).
		Set("updated_at = ?", m.now().UTC()).Where("id = ?", id).Exec(ctx)
	return job, err
}

func (m *Module) get(ctx context.Context, id string) (applicationModel, error) {
	var model applicationModel
	err := m.database.NewSelect().Model(&model).Where("id = ?", id).Scan(ctx)
	return model, err
}

func (m *Module) getOrCreate(ctx context.Context, id string, entry packagesoperator.CatalogEntry, now time.Time) (applicationModel, error) {
	model, err := m.get(ctx, id)
	if err == nil {
		return model, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return applicationModel{}, err
	}
	model = applicationModel{ID: id, App: entry.App, Version: entry.Version, Status: string(StatusAvailable), CreatedAt: now, UpdatedAt: now}
	if _, insertErr := m.database.NewInsert().Model(&model).Exec(ctx); insertErr != nil {
		// A concurrent request may have created the row; re-read it.
		if reread, e := m.get(ctx, id); e == nil {
			return reread, nil
		}
		return applicationModel{}, insertErr
	}
	return model, nil
}

func (m *Module) fail(ctx context.Context, id string, failure error) {
	message := failure.Error()
	if len(message) > 300 {
		message = message[:300]
	}
	_, _ = m.database.NewUpdate().Model((*applicationModel)(nil)).
		Set("status = ?", StatusFailed).Set("failure = ?", message).
		Set("updated_at = ?", m.now().UTC()).Where("id = ?", id).Exec(ctx)
}

func randomID() string {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		panic(err)
	}
	return hex.EncodeToString(value)
}

func (m *Module) planJob(ctx context.Context, raw json.RawMessage, report func(int, string) error) (any, error) {
	var request struct {
		ID     string `json:"id"`
		Action string `json:"action"`
	}
	if json.Unmarshal(raw, &request) != nil || request.ID == "" {
		return nil, errors.New("invalid applications planning request")
	}
	entry, ok, err := m.catalogByID(ctx, request.ID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errors.New("unknown application")
	}
	_ = report(20, "Reading installed package state.")
	plan, err := m.operator.Plan(ctx, packagesoperator.Change{Action: packagesoperator.Action(request.Action), App: entry.App, Version: entry.Version})
	if err != nil {
		m.fail(context.WithoutCancel(ctx), request.ID, err)
		return nil, err
	}
	encoded, _ := json.Marshal(plan)
	stored := planModel{ID: randomID(), ApplicationID: request.ID, Operation: request.Action, PlanJSON: string(encoded), CreatedAt: m.now().UTC(), ExpiresAt: plan.ExpiresAt}
	if _, err := m.database.NewInsert().Model(&stored).Exec(ctx); err != nil {
		return nil, err
	}
	if _, err := m.database.NewUpdate().Model((*applicationModel)(nil)).Set("status = ?", StatusPlanReady).Set("updated_at = ?", m.now().UTC()).Where("id = ?", request.ID).Exec(ctx); err != nil {
		return nil, err
	}
	_ = report(95, "Application plan is ready for approval.")
	return stored.toStoredPlan()
}

func (m *Module) applyJob(ctx context.Context, raw json.RawMessage, report func(int, string) error) (any, error) {
	var request struct {
		PlanID string `json:"planId"`
	}
	if json.Unmarshal(raw, &request) != nil || request.PlanID == "" {
		return nil, errors.New("invalid applications apply request")
	}
	model := new(planModel)
	if err := m.database.NewSelect().Model(model).Where("id = ?", request.PlanID).Scan(ctx); err != nil {
		return nil, err
	}
	stored, err := model.toStoredPlan()
	if err != nil {
		return nil, err
	}
	_ = report(25, "Applying the signed package plan.")
	observation, err := m.operator.Apply(ctx, stored.AgentPlan)
	if err != nil {
		m.fail(context.WithoutCancel(ctx), stored.ApplicationID, err)
		return nil, err
	}
	status := StatusInstalled
	installedVersion := &observation.Version
	if stored.Operation == packagesoperator.ActionRemove {
		status = StatusAvailable
		installedVersion = nil
	}
	if _, err := m.database.NewUpdate().Model((*applicationModel)(nil)).
		Set("status = ?", status).Set("installed_version = ?", installedVersion).
		Set("failure = NULL").Set("updated_at = ?", m.now().UTC()).
		Where("id = ?", stored.ApplicationID).Exec(ctx); err != nil {
		return nil, err
	}
	_ = report(95, "Application state is verified.")
	return observation, nil
}
