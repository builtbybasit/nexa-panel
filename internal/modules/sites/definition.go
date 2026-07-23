package sites

import (
	"context"
	"encoding/json"

	siteoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/sites"
)

// Definition assembles the complete operator-facing site definition: the site's
// stable identity (slug, paths, PHP version), its persisted per-site Settings,
// and the caller-supplied routing and TLS. It is the single place a full
// siteoperator.Site is built, so every operator.Plan caller — the sites module
// itself, the domains module, and the certificates module — threads Settings
// identically. That is what keeps a re-render byte-exact regardless of which
// module triggered it: routes, TLS, and settings are always merged, never one at
// the expense of the others.
//
// Callers with no routing/TLS of their own (the sites module's own planning and
// teardown) pass nil; callers that own routing (domains, certificates) pass the
// freshly loaded routes and certificate. The persisted PasswordHash is carried
// through unblanked because the operator needs it to render auth_basic_user_file
// byte-identically on every re-plan.
func (m *Module) Definition(ctx context.Context, siteID string, routes []siteoperator.Route, tls *siteoperator.TLS, tlsDomains []string) (siteoperator.Site, error) {
	model := new(siteModel)
	if err := m.database.NewSelect().Model(model).Where("id = ?", siteID).Scan(ctx); err != nil {
		return siteoperator.Site{}, err
	}
	settings, err := decodeSettings(model.SettingsJSON)
	if err != nil {
		return siteoperator.Site{}, err
	}
	return siteoperator.Site{
		ID: model.ID, Slug: model.Slug, PrimaryDomain: model.PrimaryDomain, PHPVersion: model.PHPVersion,
		UnixUser: model.UnixUser, RootPath: model.RootPath, SocketPath: model.SocketPath,
		Routes: routes, TLS: tls, TLSDomains: tlsDomains, Settings: settings,
		DeploymentMode: deploymentMode(model.DeploymentMode),
	}, nil
}

// loadSettings returns the persisted settings with the password hash intact, for
// server-side use where the credential must be preserved (unlike toSite, which
// blanks it for the API surface).
func (m *Module) loadSettings(ctx context.Context, siteID string) (siteoperator.Settings, error) {
	model := new(siteModel)
	if err := m.database.NewSelect().Model(model).Column("settings_json").Where("id = ?", siteID).Scan(ctx); err != nil {
		return siteoperator.Settings{}, err
	}
	return decodeSettings(model.SettingsJSON)
}

// decodeSettings turns a stored settings_json column into operator Settings. A
// NULL/empty column (every pre-existing site) decodes to the zero value, which
// the renderer treats as the hardened baseline — so no backfill is required.
func decodeSettings(raw *string) (siteoperator.Settings, error) {
	if raw == nil || *raw == "" {
		return siteoperator.Settings{}, nil
	}
	var settings siteoperator.Settings
	if err := json.Unmarshal([]byte(*raw), &settings); err != nil {
		return siteoperator.Settings{}, err
	}
	return settings, nil
}
