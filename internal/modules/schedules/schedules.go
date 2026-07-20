package schedules

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/modules/sites"
	"github.com/nexa-panel/nexa-panel/internal/platform/identity"
	"github.com/nexa-panel/nexa-panel/internal/platform/jobs"
	"github.com/nexa-panel/nexa-panel/internal/platform/module"
	scheduleoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/schedules"
	"github.com/nexa-panel/nexa-panel/internal/platform/persistence"

	"github.com/uptrace/bun"
)

const schema = `
	CREATE TABLE scheduled_tasks (
		id TEXT PRIMARY KEY,
		site_id TEXT NOT NULL,
		name TEXT NOT NULL,
		cron_expression TEXT NOT NULL,
		command TEXT NOT NULL,
		timeout_seconds INTEGER NOT NULL,
		enabled INTEGER NOT NULL,
		status TEXT NOT NULL,
		pending_removal INTEGER NOT NULL DEFAULT 0,
		last_job_id INTEGER REFERENCES jobs(id),
		failure TEXT,
		created_at TIMESTAMP NOT NULL,
		updated_at TIMESTAMP NOT NULL
	);
	CREATE INDEX scheduled_tasks_site_idx ON scheduled_tasks (site_id, name);
	CREATE TABLE scheduled_task_plans (
		task_id TEXT PRIMARY KEY REFERENCES scheduled_tasks(id) ON DELETE CASCADE,
		plan_json TEXT NOT NULL,
		created_at TIMESTAMP NOT NULL,
		expires_at TIMESTAMP NOT NULL
	);
`

type Status string

const (
	StatusDraft       Status = "draft"
	StatusPlanning    Status = "planning"
	StatusPlanReady   Status = "plan_ready"
	StatusActivating  Status = "activating"
	StatusActive      Status = "active"
	StatusRollingBack Status = "rolling_back"
	StatusRolledBack  Status = "rolled_back"
	StatusFailed      Status = "failed"
)

type Task struct {
	ID             string    `json:"id"`
	SiteID         string    `json:"siteId"`
	Name           string    `json:"name"`
	CronExpression string    `json:"cronExpression"`
	Command        string    `json:"command"`
	TimeoutSeconds int       `json:"timeoutSeconds"`
	Enabled        bool      `json:"enabled"`
	Status         Status    `json:"status"`
	PendingRemoval bool      `json:"pendingRemoval"`
	LastJobID      *int64    `json:"lastJobId,omitempty"`
	Failure        string    `json:"failure,omitempty"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

type TaskRequest struct {
	Name           string `json:"name"`
	CronExpression string `json:"cronExpression"`
	Command        string `json:"command"`
	TimeoutSeconds int    `json:"timeoutSeconds"`
	Enabled        bool   `json:"enabled"`
}

type SiteCatalog interface {
	Get(ctx context.Context, id string) (sites.Site, error)
}

// AccessPolicy mirrors the sites module scoping; the identity module provides
// the concrete implementation.
type AccessPolicy interface {
	SiteAccessible(ctx context.Context, user identity.User, siteID string) (bool, error)
}

type Module struct {
	database *bun.DB
	jobs     *jobs.Module
	sites    SiteCatalog
	access   AccessPolicy
	operator scheduleoperator.Operator
	now      func() time.Time
}

type taskModel struct {
	bun.BaseModel  `bun:"table:scheduled_tasks,alias:task"`
	ID             string `bun:",pk"`
	SiteID         string
	Name           string
	CronExpression string
	Command        string
	TimeoutSeconds int
	Enabled        bool
	Status         string
	PendingRemoval bool
	LastJobID      *int64
	Failure        *string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type planModel struct {
	bun.BaseModel `bun:"table:scheduled_task_plans,alias:task_plan"`
	TaskID        string `bun:",pk"`
	PlanJSON      string
	CreatedAt     time.Time
	ExpiresAt     time.Time
}

func New(ctx context.Context, database *bun.DB, jobQueue *jobs.Module, catalog SiteCatalog, access AccessPolicy, operator scheduleoperator.Operator) (*Module, error) {
	if database == nil || jobQueue == nil || catalog == nil || access == nil || operator == nil {
		return nil, errors.New("schedules database, jobs, site catalog, access policy, and operator are required")
	}
	if err := persistence.Migrate(ctx, database, "schedules", []string{schema}); err != nil {
		return nil, err
	}
	m := &Module{database: database, jobs: jobQueue, sites: catalog, access: access, operator: operator, now: time.Now}
	if err := jobQueue.RegisterHandler("schedule.plan", m.planJob); err != nil {
		return nil, err
	}
	if err := jobQueue.RegisterHandler("schedule.apply", m.applyJob); err != nil {
		return nil, err
	}
	if err := jobQueue.RegisterHandler("schedule.rollback", m.rollbackJob); err != nil {
		return nil, err
	}
	if err := jobQueue.RegisterHandler("schedule.run", m.runJob); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *Module) Descriptor() module.Descriptor {
	return module.Descriptor{
		ID: "schedules", Name: "Scheduled tasks", Version: "0.1.0",
		Description:  "Cron tasks that run as site accounts through signed agent plans with bounded output capture.",
		Dependencies: []string{"identity", "jobs", "sites"}, EstimatedIdleBytes: 256 * 1024,
	}
}

func (m *Module) Register(registry module.Registry) error {
	routes := []struct {
		pattern    string
		permission string
		handler    http.Handler
	}{
		{"GET /api/v1/sites/{id}/tasks", "schedules.read", http.HandlerFunc(m.listHTTP)},
		{"POST /api/v1/sites/{id}/tasks", "schedules.write", http.HandlerFunc(m.createHTTP)},
		{"GET /api/v1/sites/{id}/tasks/{taskId}", "schedules.read", http.HandlerFunc(m.getHTTP)},
		{"PUT /api/v1/sites/{id}/tasks/{taskId}", "schedules.write", http.HandlerFunc(m.updateHTTP)},
		{"DELETE /api/v1/sites/{id}/tasks/{taskId}", "schedules.write", http.HandlerFunc(m.deleteHTTP)},
		{"POST /api/v1/sites/{id}/tasks/{taskId}/apply", "operations.apply", http.HandlerFunc(m.applyHTTP)},
		{"POST /api/v1/sites/{id}/tasks/{taskId}/rollback", "operations.apply", http.HandlerFunc(m.rollbackHTTP)},
		{"POST /api/v1/sites/{id}/tasks/{taskId}/run", "operations.apply", http.HandlerFunc(m.runHTTP)},
		{"GET /api/v1/sites/{id}/tasks/{taskId}/runs", "schedules.read", http.HandlerFunc(m.runsHTTP)},
	}
	for _, route := range routes {
		if err := registry.HandleAuthorized(route.pattern, route.permission, route.handler); err != nil {
			return err
		}
	}
	return nil
}
