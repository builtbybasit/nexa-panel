// Package sftp is the control-plane feature module behind per-site SFTP access.
// It toggles OpenSSH-jailed SFTP for a site and rotates the account password,
// driving the privileged SFTP operator synchronously — synchronously, not as a
// durable job, precisely because a job payload is persisted and the generated
// password must never be written anywhere but the node's /etc/shadow. The module
// stores only whether SFTP is enabled and when the password was last set.
package sftp

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/modules/sites"
	"github.com/nexa-panel/nexa-panel/internal/platform/identity"
	"github.com/nexa-panel/nexa-panel/internal/platform/module"
	sftpoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/sftp"
	"github.com/nexa-panel/nexa-panel/internal/platform/webhandler"

	"github.com/uptrace/bun"
)

// SiteCatalog resolves a site by id; the sites module provides it. AccessPolicy
// scopes which sites a user may reach — both mirror the files and php modules.
type SiteCatalog interface {
	Get(ctx context.Context, id string) (sites.Site, error)
}

type AccessPolicy interface {
	SiteAccessible(ctx context.Context, user identity.User, siteID string) (bool, error)
}

// SSHAccessState reports the deploy module's per-site SSH login state. The SFTP
// jail and an SSH login write competing sshd Match blocks for the same account —
// the access block sorts first and wins every keyword — so enabling SFTP while
// SSH access is on has to be refused before the node is touched.
type SSHAccessState interface {
	AccessEnabled(ctx context.Context, siteID string) (bool, error)
}

type Module struct {
	database *bun.DB
	operator sftpoperator.Operator
	sites    SiteCatalog
	access   AccessPolicy
	ssh      SSHAccessState
	now      func() time.Time
}

type accessModel struct {
	bun.BaseModel `bun:"table:sftp_access,alias:sftp_access"`
	SiteID        string `bun:",pk"`
	Enabled       bool
	Username      string
	PasswordSetAt *time.Time
	// PendingHash is the bcrypt hash of credentials chosen at site creation,
	// held only until the site's first activation installs them into
	// /etc/shadow and enables the jail. The plaintext is never stored.
	PendingHash *string
	UpdatedAt   time.Time
}

// Access is the per-site SFTP state returned to the UI. The password itself is
// never stored, so it is never part of this shape — only whether one is set.
// PendingActivation reports staged credentials waiting for the site to activate.
type Access struct {
	SiteID            string     `json:"siteId"`
	Enabled           bool       `json:"enabled"`
	Username          string     `json:"username"`
	Host              string     `json:"host"`
	Port              int        `json:"port"`
	PasswordSetAt     *time.Time `json:"passwordSetAt,omitempty"`
	PendingActivation bool       `json:"pendingActivation,omitempty"`
}

func New(_ context.Context, database *bun.DB, operator sftpoperator.Operator, catalog SiteCatalog, access AccessPolicy) (*Module, error) {
	if database == nil || operator == nil || catalog == nil || access == nil {
		return nil, errors.New("sftp database, operator, site catalog, and access policy are required")
	}
	return &Module{database: database, operator: operator, sites: catalog, access: access, now: time.Now}, nil
}

func (m *Module) Descriptor() module.Descriptor {
	return module.Descriptor{
		ID: "sftp", Name: "SFTP", Version: "0.1.0",
		Description:        "Optional OpenSSH-jailed SFTP access per site, with generated credentials.",
		Dependencies:       []string{"identity", "sites"},
		EstimatedIdleBytes: 256 * 1024,
	}
}

// UseSSHAccessState wires the other half of the SSH/SFTP mutual exclusion. It is
// a setter rather than a constructor argument because the deploy module already
// takes this module as a dependency: the two guard each other, so one of the two
// links has to be made once both exist. Until it is set, enabling SFTP is
// refused outright — an unguarded enable can silently void the jail it claims.
func (m *Module) UseSSHAccessState(state SSHAccessState) { m.ssh = state }

// AccessEnabled reports whether per-site SFTP is currently enabled. It exists
// so the deploy module can refuse to install a conflicting sshd Match block.
func (m *Module) AccessEnabled(ctx context.Context, siteID string) (bool, error) {
	row := new(accessModel)
	err := m.database.NewSelect().Model(row).Where("site_id = ?", siteID).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return row.Enabled, nil
}

// Register binds every handler to the route and permission its operationId
// declares in the OpenAPI contract. Method, path, and required permission come
// from the embedded spec (internal/platform/httpapi/apispec), so this map is the
// whole routing table and a renamed or missing operation fails startup instead
// of drifting from the published contract.
func (m *Module) Register(registry module.Registry) error {
	return webhandler.Register(registry, map[string]http.HandlerFunc{
		"getSiteSftpAccess":        m.statusHTTP,
		"enableSiteSftpAccess":     m.enableHTTP,
		"disableSiteSftpAccess":    m.disableHTTP,
		"resetSiteSftpPassword":    m.resetPasswordHTTP,
		"stageSiteSftpCredentials": m.stageCredentialsHTTP,
	})
}
