package certificates

import (
	"context"
	"errors"
	"net"
	"net/http"
	"time"

	"github.com/uptrace/bun"

	"github.com/nexa-panel/nexa-panel/internal/modules/domains"
	"github.com/nexa-panel/nexa-panel/internal/modules/sites"
	"github.com/nexa-panel/nexa-panel/internal/platform/jobs"
	"github.com/nexa-panel/nexa-panel/internal/platform/module"
	certificateoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/certificates"
	siteoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/sites"
	"github.com/nexa-panel/nexa-panel/internal/platform/webhandler"
)

type Status string

const (
	StatusPlanning  Status = "planning"
	StatusPlanReady Status = "plan_ready"
	StatusIssuing   Status = "issuing"
	StatusActive    Status = "active"
	StatusRenewing  Status = "renewing"
	StatusRevoking  Status = "revoking"
	StatusRevoked   Status = "revoked"
	StatusFailed    Status = "failed"
)

type Certificate struct {
	ID              string     `json:"id"`
	SiteID          string     `json:"siteId"`
	PrimaryDomain   string     `json:"primaryDomain"`
	Email           string     `json:"email"`
	Status          Status     `json:"status"`
	Domains         []string   `json:"domains"`
	CertificatePath string     `json:"certificatePath,omitempty"`
	IssuedAt        *time.Time `json:"issuedAt,omitempty"`
	ExpiresAt       *time.Time `json:"expiresAt,omitempty"`
	ExpiringSoon    bool       `json:"expiringSoon"`
	LastJobID       *int64     `json:"lastJobId,omitempty"`
	Failure         string     `json:"failure,omitempty"`
	CreatedAt       time.Time  `json:"createdAt"`
	UpdatedAt       time.Time  `json:"updatedAt"`
}

type CreateRequest struct {
	SiteID string `json:"siteId"`
	Email  string `json:"email"`
}

type DomainCatalog interface {
	List(context.Context, string) ([]domains.Domain, error)
	Routing(context.Context, string, string) ([]siteoperator.Route, error)
}

type SiteCatalog interface {
	Get(context.Context, string) (sites.Site, error)
	// Definition assembles the full operator site — identity plus persisted
	// per-site Settings — so re-applying routing after a certificate change keeps
	// the site's settings intact. The caller supplies the routes and TLS it owns.
	Definition(ctx context.Context, siteID string, routes []siteoperator.Route, tls *siteoperator.TLS, tlsDomains []string) (siteoperator.Site, error)
}

type Resolver interface {
	LookupHost(context.Context, string) ([]string, error)
}

type StoredPlan struct {
	Operation string                   `json:"operation"`
	AgentPlan certificateoperator.Plan `json:"agentPlan"`
	DNS       map[string][]string      `json:"dns"`
}

type Module struct {
	database     *bun.DB
	jobs         *jobs.Module
	sites        SiteCatalog
	domains      DomainCatalog
	certificates certificateoperator.Operator
	siteOperator siteoperator.Operator
	resolver     Resolver
	now          func() time.Time
}

type certificateModel struct {
	bun.BaseModel   `bun:"table:certificates,alias:certificate"`
	ID              string `bun:",pk"`
	SiteID          string
	PrimaryDomain   string
	Email           string
	Status          string
	DomainsJSON     string
	CertificatePath *string
	PrivateKeyPath  *string
	IssuedAt        *time.Time
	ExpiresAt       *time.Time
	LastJobID       *int64
	Failure         *string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type planModel struct {
	bun.BaseModel `bun:"table:certificate_plans,alias:certificate_plan"`
	CertificateID string `bun:",pk"`
	Operation     string
	PlanJSON      string
	CreatedAt     time.Time
	ExpiresAt     time.Time
}

func New(_ context.Context, database *bun.DB, queue *jobs.Module, siteCatalog SiteCatalog, domainCatalog DomainCatalog, certOperator certificateoperator.Operator, siteOperator siteoperator.Operator, resolver Resolver) (*Module, error) {
	if database == nil || queue == nil || siteCatalog == nil || domainCatalog == nil || certOperator == nil || siteOperator == nil {
		return nil, errors.New("certificate dependencies are required")
	}
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	m := &Module{database: database, jobs: queue, sites: siteCatalog, domains: domainCatalog, certificates: certOperator, siteOperator: siteOperator, resolver: resolver, now: time.Now}
	if err := queue.RegisterHandler("certificate.plan", m.planJob); err != nil {
		return nil, err
	}
	if err := queue.RegisterHandler("certificate.execute", m.executeJob); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *Module) Descriptor() module.Descriptor {
	return module.Descriptor{ID: "certificates", Name: "Certificates", Version: "0.1.0", Description: "Let's Encrypt HTTP-01 issuance, renewal, revocation, and expiry monitoring.", Dependencies: []string{"identity", "jobs", "sites", "domains"}, EstimatedIdleBytes: 512 * 1024}
}

func (m *Module) Register(registry module.Registry) error {
	return webhandler.Register(registry, map[string]http.HandlerFunc{
		"listCertificates":            m.listHTTP,
		"createCertificate":           m.createHTTP,
		"getCertificatePlan":          m.planHTTP,
		"prepareCertificateOperation": m.prepareHTTP,
		"applyCertificateOperation":   m.applyHTTP,
	})
}
