// Package sftp is the control-plane feature module behind per-site SFTP access.
// It toggles OpenSSH-jailed SFTP for a site and rotates the account password,
// driving the privileged SFTP operator synchronously — synchronously, not as a
// durable job, precisely because a job payload is persisted and the generated
// password must never be written anywhere but the node's /etc/shadow. The module
// stores only whether SFTP is enabled and when the password was last set.
package sftp

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/modules/sites"
	"github.com/nexa-panel/nexa-panel/internal/platform/identity"
	"github.com/nexa-panel/nexa-panel/internal/platform/module"
	sftpoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/sftp"

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

type Module struct {
	database *bun.DB
	operator sftpoperator.Operator
	sites    SiteCatalog
	access   AccessPolicy
	now      func() time.Time
}

type accessModel struct {
	bun.BaseModel `bun:"table:sftp_access,alias:sftp_access"`
	SiteID        string `bun:",pk"`
	Enabled       bool
	Username      string
	PasswordSetAt *time.Time
	UpdatedAt     time.Time
}

// Access is the per-site SFTP state returned to the UI. The password itself is
// never stored, so it is never part of this shape — only whether one is set.
type Access struct {
	SiteID        string     `json:"siteId"`
	Enabled       bool       `json:"enabled"`
	Username      string     `json:"username"`
	Host          string     `json:"host"`
	Port          int        `json:"port"`
	PasswordSetAt *time.Time `json:"passwordSetAt,omitempty"`
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

func (m *Module) Register(registry module.Registry) error {
	routes := []struct {
		pattern    string
		permission string
		handler    http.Handler
	}{
		{"GET /api/v1/sites/{id}/sftp", "sites.read", http.HandlerFunc(m.statusHTTP)},
		{"POST /api/v1/sites/{id}/sftp/enable", "operations.apply", http.HandlerFunc(m.enableHTTP)},
		{"POST /api/v1/sites/{id}/sftp/disable", "operations.apply", http.HandlerFunc(m.disableHTTP)},
		{"POST /api/v1/sites/{id}/sftp/reset-password", "operations.apply", http.HandlerFunc(m.resetPasswordHTTP)},
	}
	for _, route := range routes {
		if err := registry.HandleAuthorized(route.pattern, route.permission, route.handler); err != nil {
			return err
		}
	}
	return nil
}
