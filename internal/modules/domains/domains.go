package domains

import (
	"context"
	"errors"
	"net"
	"net/http"
	"regexp"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/modules/sites"
	"github.com/nexa-panel/nexa-panel/internal/platform/jobs"
	"github.com/nexa-panel/nexa-panel/internal/platform/module"
	siteoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/sites"

	"github.com/uptrace/bun"
)

var hostnamePattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+$`)

type Kind string

const (
	KindPrimary   Kind = "primary"
	KindSubdomain Kind = "subdomain"
	KindAlias     Kind = "alias"
	KindRedirect  Kind = "redirect"
)

type Status string

const (
	StatusPlanning   Status = "planning"
	StatusPlanReady  Status = "plan_ready"
	StatusActivating Status = "activating"
	StatusActive     Status = "active"
	StatusFailed     Status = "failed"
)

type Domain struct {
	ID                string    `json:"id"`
	SiteID            string    `json:"siteId"`
	Hostname          string    `json:"hostname"`
	Kind              Kind      `json:"kind"`
	RedirectTarget    string    `json:"redirectTarget,omitempty"`
	Status            Status    `json:"status"`
	ResolvedAddresses []string  `json:"resolvedAddresses"`
	LastJobID         *int64    `json:"lastJobId,omitempty"`
	Failure           string    `json:"failure,omitempty"`
	CreatedAt         time.Time `json:"createdAt"`
	UpdatedAt         time.Time `json:"updatedAt"`
}

type CreateRequest struct {
	SiteID         string `json:"siteId"`
	Hostname       string `json:"hostname"`
	Kind           Kind   `json:"kind"`
	RedirectTarget string `json:"redirectTarget,omitempty"`
}

type SiteCatalog interface {
	Get(context.Context, string) (sites.Site, error)
}

type Resolver interface {
	LookupHost(context.Context, string) ([]string, error)
}

type TLSProvider interface {
	TLSForSite(context.Context, string) (*siteoperator.TLS, []string, error)
}

type Plan struct {
	DomainID          string            `json:"domainId"`
	NodePlan          siteoperator.Plan `json:"nodePlan"`
	ResolvedAddresses []string          `json:"resolvedAddresses"`
	Warnings          []string          `json:"warnings"`
}

type Module struct {
	database *bun.DB
	jobs     *jobs.Module
	sites    SiteCatalog
	operator siteoperator.Operator
	resolver Resolver
	tls      TLSProvider
	now      func() time.Time
}

func (m *Module) SetTLSProvider(provider TLSProvider) { m.tls = provider }

type domainModel struct {
	bun.BaseModel     `bun:"table:domains,alias:domain"`
	ID                string `bun:",pk"`
	SiteID            string
	Hostname          string
	Kind              string
	RedirectTarget    *string
	Status            string
	ResolvedAddresses string
	LastJobID         *int64
	Failure           *string
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

type planModel struct {
	bun.BaseModel `bun:"table:domain_plans,alias:domain_plan"`
	DomainID      string `bun:",pk"`
	PlanJSON      string
	CreatedAt     time.Time
	ExpiresAt     time.Time
}

func New(ctx context.Context, database *bun.DB, queue *jobs.Module, siteCatalog SiteCatalog, operator siteoperator.Operator, resolver Resolver) (*Module, error) {
	if database == nil || queue == nil || siteCatalog == nil || operator == nil {
		return nil, errors.New("domains database, jobs, sites, and node operator are required")
	}
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	m := &Module{database: database, jobs: queue, sites: siteCatalog, operator: operator, resolver: resolver, now: time.Now}
	if err := m.ensurePrimaryDomains(ctx); err != nil {
		return nil, err
	}
	if err := queue.RegisterHandler("domain.plan", m.planJob); err != nil {
		return nil, err
	}
	if err := queue.RegisterHandler("domain.activate", m.activateJob); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *Module) Descriptor() module.Descriptor {
	return module.Descriptor{ID: "domains", Name: "Domains", Version: "0.1.0", Description: "Primary domains, subdomains, aliases, redirects, and DNS preflight.", Dependencies: []string{"identity", "jobs", "sites"}, EstimatedIdleBytes: 512 * 1024}
}

func (m *Module) Register(registry module.Registry) error {
	routes := []struct {
		pattern, permission string
		handler             http.Handler
	}{
		{"GET /api/v1/domains", "domains.read", http.HandlerFunc(m.listHTTP)},
		{"POST /api/v1/domains", "domains.write", http.HandlerFunc(m.createHTTP)},
		{"GET /api/v1/domains/{id}/plan", "domains.read", http.HandlerFunc(m.planHTTP)},
		{"POST /api/v1/domains/{id}/activate", "operations.apply", http.HandlerFunc(m.activateHTTP)},
	}
	for _, route := range routes {
		if err := registry.HandleAuthorized(route.pattern, route.permission, route.handler); err != nil {
			return err
		}
	}
	return nil
}
