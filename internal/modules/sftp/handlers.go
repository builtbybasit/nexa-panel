package sftp

import (
	"context"
	"net/http"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/modules/sites"
	sftpoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/sftp"

	"github.com/uptrace/bun"
)

func (m *Module) statusHTTP(w http.ResponseWriter, r *http.Request) {
	site, ok := m.resolveSite(w, r)
	if !ok {
		return
	}
	access, err := m.currentAccess(r.Context(), site)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "sftp_unavailable", "SFTP access could not be loaded.")
		return
	}
	writeJSON(w, http.StatusOK, access)
}

// enableHTTP turns SFTP on and issues a fresh password. Enabling is idempotent
// on the node, so this doubles as the way to re-provision a site whose drop-in
// drifted. The password is returned exactly once, in this response.
func (m *Module) enableHTTP(w http.ResponseWriter, r *http.Request) {
	site, ok := m.resolveSite(w, r)
	if !ok {
		return
	}
	m.provision(w, r, site, true)
}

// resetPasswordHTTP rotates the password for an already-enabled account. It
// re-applies the same enabled configuration with a new secret.
func (m *Module) resetPasswordHTTP(w http.ResponseWriter, r *http.Request) {
	site, ok := m.resolveSite(w, r)
	if !ok {
		return
	}
	m.provision(w, r, site, false)
}

func (m *Module) provision(w http.ResponseWriter, r *http.Request, site sites.Site, allowCreate bool) {
	if !allowCreate {
		access, err := m.currentAccess(r.Context(), site)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "sftp_unavailable", "SFTP access could not be loaded.")
			return
		}
		if !access.Enabled {
			writeError(w, http.StatusConflict, "sftp_not_enabled", "Enable SFTP for this site before resetting its password.")
			return
		}
	}
	password, err := generatePassword()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "sftp_password_failed", "A new password could not be generated.")
		return
	}
	if _, err := m.operator.Apply(r.Context(), sftpoperator.Request{
		Slug: site.Slug, UnixUser: site.UnixUser, RootPath: site.RootPath, Enabled: true, Password: password,
	}); err != nil {
		writeError(w, http.StatusConflict, "sftp_operation_failed", err.Error())
		return
	}
	now := m.now().UTC()
	if err := m.upsert(r.Context(), site, true, &now); err != nil {
		writeError(w, http.StatusInternalServerError, "sftp_state_failed", "SFTP was configured but its state could not be recorded.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled": true, "username": site.UnixUser, "host": site.PrimaryDomain, "port": sshPort, "password": password,
	})
}

func (m *Module) disableHTTP(w http.ResponseWriter, r *http.Request) {
	site, ok := m.resolveSite(w, r)
	if !ok {
		return
	}
	if _, err := m.operator.Apply(r.Context(), sftpoperator.Request{
		Slug: site.Slug, UnixUser: site.UnixUser, RootPath: site.RootPath, Enabled: false,
	}); err != nil {
		writeError(w, http.StatusConflict, "sftp_operation_failed", err.Error())
		return
	}
	if err := m.upsert(r.Context(), site, false, nil); err != nil {
		writeError(w, http.StatusInternalServerError, "sftp_state_failed", "SFTP was disabled but its state could not be recorded.")
		return
	}
	writeJSON(w, http.StatusOK, Access{SiteID: site.ID, Enabled: false, Username: site.UnixUser, Host: site.PrimaryDomain, Port: sshPort})
}

// upsert records the SFTP state. On disable it clears password_set_at so the UI
// never claims a live credential for a locked account.
func (m *Module) upsert(ctx context.Context, site sites.Site, enabled bool, passwordSetAt *time.Time) error {
	row := &accessModel{SiteID: site.ID, Enabled: enabled, Username: site.UnixUser, PasswordSetAt: passwordSetAt, UpdatedAt: m.now().UTC()}
	return m.database.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		query := tx.NewInsert().Model(row).On("CONFLICT (site_id) DO UPDATE").
			Set("enabled = EXCLUDED.enabled").Set("username = EXCLUDED.username").Set("updated_at = EXCLUDED.updated_at")
		if passwordSetAt != nil {
			query = query.Set("password_set_at = EXCLUDED.password_set_at")
		} else {
			query = query.Set("password_set_at = NULL")
		}
		_, err := query.Exec(ctx)
		return err
	})
}
