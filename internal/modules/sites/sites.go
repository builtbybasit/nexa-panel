package sites

import (
	"context"
	"github.com/nexa-panel/nexa-panel/internal/platform/module"

	"github.com/nexa-panel/nexa-panel/internal/platform/identity"
	siteoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/sites"

	"github.com/uptrace/bun"

	"github.com/nexa-panel/nexa-panel/internal/platform/persistence"
	"regexp"

	"time"

	"net/http"

	"errors"
	"github.com/nexa-panel/nexa-panel/internal/platform/jobs"
)

const schema = `
	CREATE TABLE sites (
		id TEXT PRIMARY KEY,
		slug TEXT NOT NULL UNIQUE,
		display_name TEXT NOT NULL,
		primary_domain TEXT NOT NULL UNIQUE,
		php_version TEXT NOT NULL,
		unix_user TEXT NOT NULL UNIQUE,
		root_path TEXT NOT NULL UNIQUE,
		socket_path TEXT NOT NULL UNIQUE,
		status TEXT NOT NULL,
		last_job_id INTEGER REFERENCES jobs(id),
		failure TEXT,
		created_at TIMESTAMP NOT NULL,
		updated_at TIMESTAMP NOT NULL
	);
	CREATE INDEX sites_status_slug_idx ON sites (status, slug);
	CREATE TABLE site_plans (
		site_id TEXT PRIMARY KEY REFERENCES sites(id) ON DELETE CASCADE,
		plan_json TEXT NOT NULL,
		created_at TIMESTAMP NOT NULL,
		expires_at TIMESTAMP NOT NULL
	);
`

var (
	slugPattern   = regexp.MustCompile(`^[a-z][a-z0-9-]{1,31}$`)
	domainPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+$`)
)

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

type Site struct {
	ID            string    `json:"id"`
	Slug          string    `json:"slug"`
	DisplayName   string    `json:"displayName"`
	PrimaryDomain string    `json:"primaryDomain"`
	PHPVersion    string    `json:"phpVersion"`
	UnixUser      string    `json:"unixUser"`
	RootPath      string    `json:"rootPath"`
	SocketPath    string    `json:"socketPath"`
	Status        Status    `json:"status"`
	LastJobID     *int64    `json:"lastJobId,omitempty"`
	Failure       string    `json:"failure,omitempty"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

type CreateRequest struct {
	Slug          string `json:"slug"`
	DisplayName   string `json:"displayName"`
	PrimaryDomain string `json:"primaryDomain"`
	PHPVersion    string `json:"phpVersion"`
}

type RuntimeCatalog interface {
	Allowed(ctx context.Context, version string) (bool, error)
}

// AccessPolicy scopes what the requesting user may see; the identity module
// provides the concrete implementation via SetAccessPolicy.
type AccessPolicy interface {
	SiteAccessible(ctx context.Context, user identity.User, siteID string) (bool, error)
	AccessibleSiteIDs(ctx context.Context, user identity.User) (bool, []string, error)
}

type Module struct {
	database *bun.DB
	jobs     *jobs.Module
	runtimes RuntimeCatalog
	operator siteoperator.Operator
	access   AccessPolicy
	now      func() time.Time
}

func (m *Module) SetAccessPolicy(policy AccessPolicy) { m.access = policy }

type siteModel struct {
	bun.BaseModel `bun:"table:sites,alias:site"`
	ID            string `bun:",pk"`
	Slug          string
	DisplayName   string
	PrimaryDomain string
	PHPVersion    string
	UnixUser      string
	RootPath      string
	SocketPath    string
	Status        string
	LastJobID     *int64
	Failure       *string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

type planModel struct {
	bun.BaseModel `bun:"table:site_plans,alias:site_plan"`
	SiteID        string `bun:",pk"`
	PlanJSON      string
	CreatedAt     time.Time
	ExpiresAt     time.Time
}

func New(ctx context.Context, database *bun.DB, jobQueue *jobs.Module, runtimeCatalog RuntimeCatalog, operator siteoperator.Operator) (*Module, error) {
	if database == nil || jobQueue == nil || runtimeCatalog == nil || operator == nil {
		return nil, errors.New("sites database, jobs, runtime catalog, and node operator are required")
	}
	if err := persistence.Migrate(ctx, database, "sites", []string{schema}); err != nil {
		return nil, err
	}
	m := &Module{database: database, jobs: jobQueue, runtimes: runtimeCatalog, operator: operator, now: time.Now}
	if err := jobQueue.RegisterHandler("site.plan", m.planJob); err != nil {
		return nil, err
	}
	if err := jobQueue.RegisterHandler("site.activate", m.activateJob); err != nil {
		return nil, err
	}
	if err := jobQueue.RegisterHandler("site.rollback", m.rollbackJob); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *Module) Descriptor() module.Descriptor {
	return module.Descriptor{
		ID: "sites", Name: "Sites", Version: "0.1.0",
		Description:  "Managed web workloads with durable Nginx and PHP-FPM configuration planning.",
		Dependencies: []string{"identity", "jobs", "runtimes"}, EstimatedIdleBytes: 1024 * 1024,
	}
}

func (m *Module) Register(registry module.Registry) error {
	routes := []struct {
		pattern    string
		permission string
		handler    http.Handler
	}{
		{"GET /api/v1/sites", "sites.read", http.HandlerFunc(m.listHTTP)},
		{"POST /api/v1/sites", "sites.write", http.HandlerFunc(m.createHTTP)},
		{"GET /api/v1/sites/{id}", "sites.read", http.HandlerFunc(m.getHTTP)},
		{"GET /api/v1/sites/{id}/plan", "sites.read", http.HandlerFunc(m.planHTTP)},
		{"POST /api/v1/sites/{id}/plan", "sites.write", http.HandlerFunc(m.replanHTTP)},
		{"POST /api/v1/sites/{id}/activate", "operations.apply", http.HandlerFunc(m.activateHTTP)},
		{"POST /api/v1/sites/{id}/rollback", "operations.apply", http.HandlerFunc(m.rollbackHTTP)},
	}
	for _, route := range routes {
		if err := registry.HandleAuthorized(route.pattern, route.permission, route.handler); err != nil {
			return err
		}
	}
	return nil
}
