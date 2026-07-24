package deploy

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"path/filepath"

	"github.com/nexa-panel/nexa-panel/internal/modules/sites"
	"github.com/nexa-panel/nexa-panel/internal/platform/httpapi"
	"github.com/nexa-panel/nexa-panel/internal/platform/identity"
	deployoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/deploy"

	"github.com/uptrace/bun"
)

const sshPort = 22

// refusal is a deliberate, reportable "no": the module decides the status and
// machine code once, and the handlers stay a translation layer. Anything that
// is not a refusal is an internal failure and is reported as one.
type refusal struct {
	status  int
	code    string
	message string
}

func (e *refusal) Error() string { return e.message }

func refuse(status int, code, message string) error {
	return &refusal{status: status, code: code, message: message}
}

// resolveSite loads a site after confirming the caller may reach it. A caller
// without access gets the same not-found signal as a missing site, so scoped
// roles cannot probe which sites exist.
func (m *Module) resolveSite(w http.ResponseWriter, r *http.Request) (sites.Site, bool) {
	user, ok := identity.UserFromContext(r.Context())
	if !ok {
		httpapi.WriteError(w, http.StatusUnauthorized, "authentication_required", "Sign in to continue.")
		return sites.Site{}, false
	}
	accessible, err := m.access.SiteAccessible(r.Context(), user, r.PathValue("id"))
	if err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, "site_unavailable", "The site could not be loaded.")
		return sites.Site{}, false
	}
	if !accessible {
		httpapi.WriteError(w, http.StatusNotFound, "site_not_found", "The requested site does not exist.")
		return sites.Site{}, false
	}
	site, err := m.sites.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		httpapi.WriteError(w, http.StatusNotFound, "site_not_found", "The requested site does not exist.")
		return sites.Site{}, false
	}
	return site, true
}

// currentAccess reads the persisted SSH state for a site, defaulting to a
// disabled record when the site has never had SSH access touched.
func (m *Module) currentAccess(ctx context.Context, database bun.IDB, site sites.Site) (SSHAccess, error) {
	row := new(sshAccessModel)
	err := database.NewSelect().Model(row).Where("site_id = ?", site.ID).Scan(ctx)
	access := SSHAccess{
		SiteID: site.ID, Enabled: false, Username: site.UnixUser,
		Host: site.PrimaryDomain, Port: sshPort, Shell: nologinShell,
		AuthorizedKeysPath: authorizedKeysPath(site),
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return SSHAccess{}, err
	}
	if err == nil {
		access.Enabled = row.Enabled
		access.Shell = row.Shell
		access.EnabledAt = row.EnabledAt
	}
	rows, err := m.keyRows(ctx, database, site.ID)
	if err != nil {
		return SSHAccess{}, err
	}
	access.Keys = presentKeys(rows)
	return access, nil
}

func presentKeys(rows []sshKeyModel) []SSHKey {
	keys := make([]SSHKey, 0, len(rows))
	for _, row := range rows {
		keys = append(keys, SSHKey{
			ID: row.ID, Label: row.Label, Algorithm: row.Algorithm,
			Fingerprint: row.Fingerprint, Comment: row.Comment, CreatedAt: row.CreatedAt,
		})
	}
	return keys
}

// upsert records the desired SSH state. enabled_at is set the first time access
// is switched on and cleared on disable, so the UI never claims a live login for
// an account whose shell is nologin.
func (m *Module) upsert(ctx context.Context, tx bun.Tx, site sites.Site, enabled bool) error {
	now := m.now().UTC()
	row := &sshAccessModel{SiteID: site.ID, Enabled: enabled, Username: site.UnixUser, Shell: nologinShell, UpdatedAt: now}
	if enabled {
		row.Shell = loginShell
		row.EnabledAt = &now
	}
	query := tx.NewInsert().Model(row).On("CONFLICT (site_id) DO UPDATE").
		Set("enabled = EXCLUDED.enabled").Set("username = EXCLUDED.username").
		Set("shell = EXCLUDED.shell").Set("updated_at = EXCLUDED.updated_at")
	if enabled {
		query = query.Set("enabled_at = COALESCE(site_ssh_access.enabled_at, EXCLUDED.enabled_at)")
	} else {
		query = query.Set("enabled_at = NULL")
	}
	_, err := query.Exec(ctx)
	return err
}

// sshState is a point-in-time copy of everything the control plane holds for one
// site's SSH access. It exists so a change the node refuses can be undone after
// the fact: the rows are committed before the node is driven, because holding
// the database's single connection across an agent round trip would stall the
// whole panel.
type sshState struct {
	access *sshAccessModel
	keys   []sshKeyModel
}

func (m *Module) snapshot(ctx context.Context, siteID string) (sshState, error) {
	var state sshState
	row := new(sshAccessModel)
	err := m.database.NewSelect().Model(row).Where("site_id = ?", siteID).Scan(ctx)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// No row yet: restoring means removing whatever the change inserted.
	case err != nil:
		return sshState{}, err
	default:
		state.access = row
	}
	keys, err := m.keyRows(ctx, m.database, siteID)
	if err != nil {
		return sshState{}, err
	}
	state.keys = keys
	return state, nil
}

// restore puts a site's SSH rows back exactly as snapshot found them. It rewrites
// the whole key list rather than reversing individual statements, so it undoes an
// insert, a delete, and a state flip with the same code.
func (m *Module) restore(ctx context.Context, siteID string, state sshState) error {
	return m.database.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		if _, err := tx.NewDelete().Model((*sshKeyModel)(nil)).Where("site_id = ?", siteID).Exec(ctx); err != nil {
			return err
		}
		if len(state.keys) > 0 {
			if _, err := tx.NewInsert().Model(&state.keys).Exec(ctx); err != nil {
				return err
			}
		}
		if state.access == nil {
			_, err := tx.NewDelete().Model((*sshAccessModel)(nil)).Where("site_id = ?", siteID).Exec(ctx)
			return err
		}
		_, err := tx.NewInsert().Model(state.access).On("CONFLICT (site_id) DO UPDATE").
			Set("enabled = EXCLUDED.enabled").Set("username = EXCLUDED.username").
			Set("shell = EXCLUDED.shell").Set("enabled_at = EXCLUDED.enabled_at").
			Set("updated_at = EXCLUDED.updated_at").Exec(ctx)
		return err
	})
}

// drive applies the desired state to the node. The key list it sends is the
// complete set the control plane holds, because the operator rewrites the
// authorized-keys file rather than merging into it.
func (m *Module) drive(ctx context.Context, site sites.Site, enabled bool, keys []sshKeyModel) error {
	request := deployoperator.SSHAccessRequest{
		Slug: site.Slug, UnixUser: site.UnixUser, RootPath: site.RootPath, Enabled: enabled,
		Keys: authorizedKeys(keys),
	}
	_, err := m.operator.ApplySSHAccess(ctx, request)
	return err
}

func authorizedKeys(rows []sshKeyModel) []deployoperator.AuthorizedKey {
	keys := make([]deployoperator.AuthorizedKey, 0, len(rows))
	for _, row := range rows {
		keys = append(keys, deployoperator.AuthorizedKey{Algorithm: row.Algorithm, Blob: row.PublicKey, Comment: row.Comment})
	}
	return keys
}

func (m *Module) keyRows(ctx context.Context, database bun.IDB, siteID string) ([]sshKeyModel, error) {
	var rows []sshKeyModel
	err := database.NewSelect().Model(&rows).Where("site_id = ?", siteID).Order("created_at ASC").Scan(ctx)
	return rows, err
}

func authorizedKeysPath(site sites.Site) string {
	return filepath.Join(deployoperator.AuthorizedKeysDir, site.UnixUser, "authorized_keys")
}

// The shells the operator flips the account between. They are restated here so
// the persisted state and the UI agree with the node without a round trip.
const (
	loginShell   = "/bin/bash"
	nologinShell = "/usr/sbin/nologin"
)
