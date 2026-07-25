package sites

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/platform/identity"
	"github.com/nexa-panel/nexa-panel/internal/platform/module"
	siteoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/sites"
	"github.com/nexa-panel/nexa-panel/internal/platform/webhandler"

	"github.com/uptrace/bun"

	"github.com/nexa-panel/nexa-panel/internal/platform/jobs"
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
	StatusDeleting    Status = "deleting"
	StatusFailed      Status = "failed"
)

// Deployment modes. Standard is the panel-owned document root every site has
// today; deployer hands the release tree under {root}/app to an external
// deployer and points the vhost at its current release.
const (
	DeploymentModeStandard = "standard"
	DeploymentModeDeployer = "deployer"
)

type Site struct {
	ID            string                `json:"id"`
	Slug          string                `json:"slug"`
	DisplayName   string                `json:"displayName"`
	PrimaryDomain string                `json:"primaryDomain"`
	PHPVersion    string                `json:"phpVersion"`
	UnixUser      string                `json:"unixUser"`
	RootPath      string                `json:"rootPath"`
	SocketPath    string                `json:"socketPath"`
	Status        Status                `json:"status"`
	Settings      siteoperator.Settings `json:"settings"`
	// DeploymentMode is "standard" (the panel owns the document root) or
	// "deployer" (a release tree owns it); see validDeploymentMode.
	DeploymentMode string    `json:"deploymentMode"`
	LastJobID      *int64    `json:"lastJobId,omitempty"`
	Failure        string    `json:"failure,omitempty"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
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

// RouteSource and TLSProvider let a settings change on an already-active site
// re-assemble the *complete* node definition — the extra domains and the TLS
// certificate — instead of the bare primary-domain view the sites module holds
// on its own. They are satisfied by the domains and certificates modules and
// wired in after construction, mirroring how those modules already depend on the
// sites catalog. When unset (e.g. in tests), settingsJob simply plans the bare
// site, which is still safe because a bare re-render only drops customizations,
// never corrupts the vhost.
type RouteSource interface {
	Routing(ctx context.Context, siteID, includeID string) ([]siteoperator.Route, error)
}

type TLSProvider interface {
	TLSForSite(ctx context.Context, siteID string) (*siteoperator.TLS, []string, error)
}

type Module struct {
	database    *bun.DB
	jobs        *jobs.Module
	runtimes    RuntimeCatalog
	operator    siteoperator.Operator
	access      AccessPolicy
	routeSource RouteSource
	tls         TLSProvider
	// deployTeardown is optional; when unset a teardown simply removes the
	// sites module's own artifacts, which is the pre-deployer behaviour.
	deployTeardown DeployTeardown
	// sftp is optional; when set, an activation finishes by applying any SFTP
	// credentials that were staged when the site was created.
	sftp SftpProvisioner
	now  func() time.Time
}

// SftpProvisioner applies SFTP credentials staged at site creation, at the
// first moment they can work: right after an activation gives the site its
// system account. The sftp module satisfies it and is wired in after
// construction, like the other cross-module links here, because it depends on
// this module in the other direction.
type SftpProvisioner interface {
	ProvisionPendingCredentials(ctx context.Context, siteID string) (bool, error)
}

func (m *Module) SetSftpProvisioner(provisioner SftpProvisioner) { m.sftp = provisioner }

// DeployTeardown withdraws the node-side grants a deploy-side feature installed
// for a site — today the deployer layout's narrow PHP-FPM reload permission,
// which lives in /etc/sudoers.d and so outlives the site's own artifacts. The
// deploy module satisfies it and is wired in after construction, like the other
// cross-module links here, because it depends on this module in the other
// direction.
type DeployTeardown interface {
	TeardownSiteDeployment(ctx context.Context, siteID string) error
}

func (m *Module) SetDeployTeardown(teardown DeployTeardown) { m.deployTeardown = teardown }

func (m *Module) SetAccessPolicy(policy AccessPolicy) { m.access = policy }

func (m *Module) SetRouteSource(source RouteSource) { m.routeSource = source }

func (m *Module) SetTLSProvider(provider TLSProvider) { m.tls = provider }

type siteModel struct {
	bun.BaseModel `bun:"table:sites,alias:site"`
	ID            string `bun:",pk"`
	Slug          string
	DisplayName   string
	PrimaryDomain string
	PHPVersion    string
	// RetiredPHPVersion records the runtime the node is still serving with
	// after a version change was requested but before it was applied. The next
	// plan retires that version's pool; a successful apply clears the column.
	RetiredPHPVersion *string
	UnixUser          string
	RootPath          string
	SocketPath        string
	Status            string
	SettingsJSON      *string
	DeploymentMode    string
	LastJobID         *int64
	Failure           *string
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

type planModel struct {
	bun.BaseModel `bun:"table:site_plans,alias:site_plan"`
	SiteID        string `bun:",pk"`
	PlanJSON      string
	CreatedAt     time.Time
	ExpiresAt     time.Time
}

func New(_ context.Context, database *bun.DB, jobQueue *jobs.Module, runtimeCatalog RuntimeCatalog, operator siteoperator.Operator) (*Module, error) {
	if database == nil || jobQueue == nil || runtimeCatalog == nil || operator == nil {
		return nil, errors.New("sites database, jobs, runtime catalog, and node operator are required")
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
	// A teardown converges rather than half-applies: the node treats an artifact
	// that is already gone as the desired end state, the account and root purge
	// is idempotent, and the row delete is the last step. Retrying it after a
	// crash is therefore safe — and it has to be retried, because the fail policy
	// left the site in "deleting" forever, which siteDeletable rejects, making
	// the site permanently undeletable.
	if err := jobQueue.RegisterHandlerWithOptions("site.delete", m.deleteJob, jobs.HandlerOptions{RecoveryPolicy: jobs.RecoveryRetry}); err != nil {
		return nil, err
	}
	if err := jobQueue.RegisterHandler("site.settings", m.settingsJob); err != nil {
		return nil, err
	}
	if err := m.reconcileInterruptedTeardowns(context.Background()); err != nil {
		return nil, err
	}
	return m, nil
}

// reconcileInterruptedTeardowns releases sites left in the transient "deleting"
// status by a worker that died. The status is only ever correct while a teardown
// job is live, and siteDeletable rejects every DELETE while it is set, so a row
// that outlived its job used to be undeletable for good. The jobs module has
// already recovered interrupted work by the time a module is constructed, so any
// row with no queued or running job behind it is stranded and is returned to
// "failed" — a state a fresh delete may act on.
func (m *Module) reconcileInterruptedTeardowns(ctx context.Context) error {
	_, err := m.database.NewUpdate().Model((*siteModel)(nil)).
		Set("status = ?", StatusFailed).
		Set("failure = ?", "The site removal was interrupted before it finished; delete the site again to complete it.").
		Set("updated_at = ?", m.now().UTC()).
		Where("status = ?", StatusDeleting).
		Where("last_job_id IS NULL OR NOT EXISTS (SELECT 1 FROM jobs WHERE jobs.id = site.last_job_id AND jobs.state IN (?, ?))",
			jobs.StateQueued, jobs.StateRunning).
		Exec(ctx)
	return err
}

func (m *Module) Descriptor() module.Descriptor {
	return module.Descriptor{
		ID: "sites", Name: "Sites", Version: "0.1.0",
		Description:  "Managed web workloads with durable Nginx and PHP-FPM configuration planning.",
		Dependencies: []string{"identity", "jobs", "runtimes"}, EstimatedIdleBytes: 1024 * 1024,
	}
}

// Register binds every handler to the route and permission its operationId
// declares in the OpenAPI contract. Method, path, and required permission come
// from the embedded spec (internal/platform/httpapi/apispec), so this map is the
// whole routing table and a renamed or missing operation fails startup instead
// of drifting from the published contract.
func (m *Module) Register(registry module.Registry) error {
	return webhandler.Register(registry, map[string]http.HandlerFunc{
		"listSites":          m.listHTTP,
		"createSite":         m.createHTTP,
		"getSite":            m.getHTTP,
		"updateSiteSettings": m.updateSettingsHTTP,
		"getSitePlan":        m.planHTTP,
		"refreshSitePlan":    m.replanHTTP,
		"activateSite":       m.activateHTTP,
		"rollbackSite":       m.rollbackHTTP,
		"deleteSite":         m.deleteHTTP,
	})
}
