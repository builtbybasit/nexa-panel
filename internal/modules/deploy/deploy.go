// Package deploy is the control-panel feature module behind per-site
// deployment access. Its first surface is SSH: it owns the panel-managed key
// list for a site's own Unix account and drives the privileged deploy operator
// to install it.
//
// Its second surface is GitHub: a per-site deploy key, minted and kept on the
// node, plus a job that proves it can read a repository.
//
// Its third is the deployer layout: the switch between a panel-owned document
// root and a release tree, and an editor for the shared environment file that
// tree keeps between releases.
//
// Its fourth is the node itself: a job that verifies — and where it can
// installs — the tooling a deployment needs, and that reports, without ever
// changing, whether the firewall still lets an SSH client in.
//
// Every operation that carries key material runs synchronously on the request
// goroutine — never as a durable job — because a job payload is persisted and
// neither an authorized-key install nor a generated private key may leave a
// record outside the node. The GitHub access test is the one exception, and it
// is one because its payload is an identity and a repository name and nothing
// else. The module stores public key material and nothing else.
package deploy

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/modules/sites"
	"github.com/nexa-panel/nexa-panel/internal/platform/identity"
	"github.com/nexa-panel/nexa-panel/internal/platform/jobs"
	"github.com/nexa-panel/nexa-panel/internal/platform/module"
	deployoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/deploy"

	"github.com/uptrace/bun"
)

// SiteCatalog resolves a site by id; the sites module provides it. AccessPolicy
// scopes which sites a user may reach — both mirror the sftp and php modules.
type SiteCatalog interface {
	Get(ctx context.Context, id string) (sites.Site, error)
}

type AccessPolicy interface {
	SiteAccessible(ctx context.Context, user identity.User, siteID string) (bool, error)
}

// SFTPState reports the sftp module's per-site state. SSH access and the SFTP
// jail write competing sshd Match blocks for the same account, so the two are
// mutually exclusive and this is how the exclusion is checked before the node
// is touched at all.
type SFTPState interface {
	AccessEnabled(ctx context.Context, siteID string) (bool, error)
}

type Module struct {
	database *bun.DB
	operator deployoperator.Operator
	jobs     *jobs.Module
	sites    SiteCatalog
	access   AccessPolicy
	sftp     SFTPState
	// firewall is optional. It is set after construction rather than taken as a
	// constructor argument because the firewall operator is wired at the same
	// point this module is, and a nil inspector only costs the prepare job its
	// port-22 warning — the rest of the run is unaffected.
	firewall FirewallInspector
	now      func() time.Time
	// sshChanges serializes SSH state changes per site. The node is driven
	// outside the database transaction that records the change, so this —
	// rather than SQLite's write lock — is what stops two concurrent changes
	// from racing each other's key lists onto the node. The race it guards is
	// between two changes to the *same* site's key list; a single lock for the
	// whole panel also made an unrelated site's key edit wait behind a
	// multi-minute agent round trip, so the lock is keyed by site.
	sshChanges siteLocks
}

// siteLocks hands out one mutex per site id. Entries are never evicted: a panel
// has a bounded number of sites and a mutex is a few bytes, which is cheaper
// than reference-counting the removals.
type siteLocks struct {
	guard sync.Mutex
	locks map[string]*sync.Mutex
}

func (l *siteLocks) get(siteID string) *sync.Mutex {
	l.guard.Lock()
	defer l.guard.Unlock()
	if l.locks == nil {
		l.locks = make(map[string]*sync.Mutex)
	}
	lock, ok := l.locks[siteID]
	if !ok {
		lock = new(sync.Mutex)
		l.locks[siteID] = lock
	}
	return lock
}

func New(_ context.Context, database *bun.DB, operator deployoperator.Operator, queue *jobs.Module, catalog SiteCatalog, access AccessPolicy, sftp SFTPState) (*Module, error) {
	if database == nil || operator == nil || queue == nil || catalog == nil || access == nil || sftp == nil {
		return nil, errors.New("deploy database, operator, jobs, site catalog, access policy, and SFTP state are required")
	}
	m := &Module{database: database, operator: operator, jobs: queue, sites: catalog, access: access, sftp: sftp, now: time.Now}
	// The GitHub access test is the module's one durable job: it talks to a
	// third party twice and the page streams its progress. The handler is
	// registered here, before any submit, because the queue rejects a kind it
	// has never seen (jobs/repository.go:72). The default RecoveryFail policy
	// is kept even though the test mutates nothing — a run whose ownership was
	// lost is cheap to repeat by hand and should not replay itself.
	if err := queue.RegisterHandler("deploy.github_test", m.githubTestJob); err != nil {
		return nil, err
	}
	// The node preparation is the module's second job, and one for the same
	// reason: it runs apt, which is measured in minutes, and the page follows
	// its progress. RecoveryFail is kept here too — re-running a prepare by
	// hand is free, and an install that replayed itself unattended is not.
	if err := queue.RegisterHandler("deploy.prepare", m.prepareJob); err != nil {
		return nil, err
	}
	return m, nil
}

// UseFirewallState gives the module the node's firewall reader. The prepare job
// uses it to warn when port 22 is closed; it can only read, so no path through
// this module can open a port — that stays behind the MFA-gated firewall.write
// surface. Wiring it is optional and separate from New for the same reason the
// sftp module's UseSSHAccessState is: the two modules are constructed together
// and neither may depend on the other's constructor.
func (m *Module) UseFirewallState(inspector FirewallInspector) { m.firewall = inspector }

// AccessEnabled reports whether per-site SSH access is currently enabled. It is
// the mirror of the sftp module's accessor and exists for the same reason: the
// two features write competing sshd Match blocks for one account, so each has to
// be able to refuse while the other is on.
func (m *Module) AccessEnabled(ctx context.Context, siteID string) (bool, error) {
	row := new(sshAccessModel)
	err := m.database.NewSelect().Model(row).Where("site_id = ?", siteID).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return row.Enabled, nil
}

func (m *Module) Descriptor() module.Descriptor {
	return module.Descriptor{
		ID: "deploy", Name: "Deployment", Version: "0.1.0",
		Description:        "Per-site deployment access: managed SSH login keys for a site's own account.",
		Dependencies:       []string{"identity", "sites", "jobs"},
		EstimatedIdleBytes: 256 * 1024,
	}
}

// Register wires each deployment surface from its own *_http.go file, so a new
// surface is added by registering one more group rather than by editing a
// single shared route table.
func (m *Module) Register(registry module.Registry) error {
	if err := m.registerSSHHTTP(registry); err != nil {
		return err
	}
	if err := m.registerGitHubHTTP(registry); err != nil {
		return err
	}
	if err := m.registerLayoutHTTP(registry); err != nil {
		return err
	}
	return m.registerPrepareHTTP(registry)
}
