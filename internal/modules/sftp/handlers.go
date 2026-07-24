package sftp

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/modules/sites"
	"github.com/nexa-panel/nexa-panel/internal/platform/httpapi"
	sftpoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/sftp"

	"github.com/uptrace/bun"
	"golang.org/x/crypto/bcrypt"
)

// Password bounds shared by every path a caller-chosen credential enters
// through. The floor mirrors the operator's own minimum; the ceiling is
// bcrypt's 72-byte input limit, which the staged flow hashes with — one rule
// for both flows so a password accepted here is never rejected there.
const (
	minSuppliedPasswordLength = 12
	maxSuppliedPasswordLength = 72
)

// credentialsBody is the optional request body for enable/reset and the
// required one for staging: a caller-chosen password, write-only.
type credentialsBody struct {
	Password string `json:"password"`
}

// decodeCredentials tolerates an absent body (the pre-existing generate-for-me
// contract) but rejects a malformed one, so a client that meant to send a
// password never silently gets a generated one instead.
func decodeCredentials(r *http.Request) (credentialsBody, error) {
	var body credentialsBody
	err := json.NewDecoder(r.Body).Decode(&body)
	if err != nil && !errors.Is(err, io.EOF) {
		return credentialsBody{}, err
	}
	return body, nil
}

func validSuppliedPassword(password string) bool {
	return len(password) >= minSuppliedPasswordLength && len(password) <= maxSuppliedPasswordLength
}

func (m *Module) statusHTTP(w http.ResponseWriter, r *http.Request) {
	site, ok := m.resolveSite(w, r)
	if !ok {
		return
	}
	access, err := m.currentAccess(r.Context(), site)
	if err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, "sftp_unavailable", "SFTP access could not be loaded.")
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, access)
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
	// The SSH check comes first, and covers a password reset as well: both
	// re-apply the jail, and a jail written while the site has an SSH-access
	// Match block is not enforced — that block sorts first and keeps its own
	// ChrootDirectory, ForceCommand and authentication methods. The operator
	// re-checks this on the node, which closes the race against an SSH enable
	// that lands in between.
	if m.ssh == nil {
		httpapi.WriteError(w, http.StatusInternalServerError, "sftp_unavailable", "SFTP access could not be configured.")
		return
	}
	interactive, err := m.ssh.AccessEnabled(r.Context(), site.ID)
	if err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, "sftp_unavailable", "SFTP access could not be loaded.")
		return
	}
	if interactive {
		httpapi.WriteError(w, http.StatusConflict, "ssh_access_enabled", "Disable SSH access for this site first. One account cannot have both an SSH login and an SFTP jail.")
		return
	}
	if !allowCreate {
		access, err := m.currentAccess(r.Context(), site)
		if err != nil {
			httpapi.WriteError(w, http.StatusInternalServerError, "sftp_unavailable", "SFTP access could not be loaded.")
			return
		}
		if !access.Enabled {
			httpapi.WriteError(w, http.StatusConflict, "sftp_not_enabled", "Enable SFTP for this site before resetting its password.")
			return
		}
	}
	// A caller may choose the password (FastPanel-style); an absent body keeps
	// the original generate-for-me behaviour.
	body, err := decodeCredentials(r)
	if err != nil {
		httpapi.WriteError(w, http.StatusBadRequest, "sftp_request_invalid", "The request body could not be read.")
		return
	}
	password := body.Password
	if password != "" && !validSuppliedPassword(password) {
		httpapi.WriteError(w, http.StatusUnprocessableEntity, "sftp_password_invalid",
			"The SFTP password must be 12-72 characters long.")
		return
	}
	if password == "" {
		password, err = generatePassword()
		if err != nil {
			httpapi.WriteError(w, http.StatusInternalServerError, "sftp_password_failed", "A new password could not be generated.")
			return
		}
	}
	if _, err := m.operator.Apply(r.Context(), sftpoperator.Request{
		Slug: site.Slug, UnixUser: site.UnixUser, RootPath: site.RootPath, Enabled: true, Password: password,
	}); err != nil {
		if errors.Is(err, sftpoperator.ErrSSHAccessPresent) {
			httpapi.WriteError(w, http.StatusConflict, "ssh_access_enabled", "The node still has SSH access configured for this site. Disable it, then try again.")
			return
		}
		httpapi.WriteError(w, http.StatusConflict, "sftp_operation_failed", err.Error())
		return
	}
	now := m.now().UTC()
	if err := m.upsert(r.Context(), site, true, &now); err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, "sftp_state_failed", "SFTP was configured but its state could not be recorded.")
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{
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
		httpapi.WriteError(w, http.StatusConflict, "sftp_operation_failed", err.Error())
		return
	}
	if err := m.upsert(r.Context(), site, false, nil); err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, "sftp_state_failed", "SFTP was disabled but its state could not be recorded.")
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, Access{SiteID: site.ID, Enabled: false, Username: site.UnixUser, Host: site.PrimaryDomain, Port: sshPort})
}

// stageCredentialsHTTP records a caller-chosen SFTP password for a site that
// has not been activated yet. Only a bcrypt hash of it is stored; the site's
// activation installs the hash into /etc/shadow and enables the jail. An active
// site is refused — its account already exists, so the synchronous enable path
// (which never persists anything) is the right tool there.
func (m *Module) stageCredentialsHTTP(w http.ResponseWriter, r *http.Request) {
	site, ok := m.resolveSite(w, r)
	if !ok {
		return
	}
	m.stageCredentials(w, r, site)
}

func (m *Module) stageCredentials(w http.ResponseWriter, r *http.Request, site sites.Site) {
	if site.Status == sites.StatusActive {
		httpapi.WriteError(w, http.StatusConflict, "site_already_active",
			"The site is already live. Enable SFTP directly instead of staging credentials.")
		return
	}
	if m.ssh == nil {
		httpapi.WriteError(w, http.StatusInternalServerError, "sftp_unavailable", "SFTP access could not be configured.")
		return
	}
	interactive, err := m.ssh.AccessEnabled(r.Context(), site.ID)
	if err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, "sftp_unavailable", "SFTP access could not be loaded.")
		return
	}
	if interactive {
		httpapi.WriteError(w, http.StatusConflict, "ssh_access_enabled", "Disable SSH access for this site first. One account cannot have both an SSH login and an SFTP jail.")
		return
	}
	body, err := decodeCredentials(r)
	if err != nil || body.Password == "" {
		httpapi.WriteError(w, http.StatusBadRequest, "sftp_request_invalid", "A password is required to stage SFTP credentials.")
		return
	}
	if !validSuppliedPassword(body.Password) {
		httpapi.WriteError(w, http.StatusUnprocessableEntity, "sftp_password_invalid",
			"The SFTP password must be 12-72 characters long.")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(body.Password), bcrypt.DefaultCost)
	if err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, "sftp_password_failed", "The password could not be secured for staging.")
		return
	}
	now := m.now().UTC()
	pending := string(hash)
	row := &accessModel{SiteID: site.ID, Enabled: false, Username: site.UnixUser, PendingHash: &pending, UpdatedAt: now}
	// Enabled and password_set_at are deliberately left alone on conflict: a
	// rolled-back site that once had live SFTP keeps reporting that state until
	// the next activation swaps in the staged credential.
	if _, err := m.database.NewInsert().Model(row).On("CONFLICT (site_id) DO UPDATE").
		Set("username = EXCLUDED.username").Set("pending_hash = EXCLUDED.pending_hash").
		Set("updated_at = EXCLUDED.updated_at").Exec(r.Context()); err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, "sftp_state_failed", "The staged credentials could not be recorded.")
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{
		"siteId": site.ID, "username": site.UnixUser, "host": site.PrimaryDomain, "port": sshPort,
		"pendingActivation": true,
	})
}

// ProvisionPendingCredentials applies credentials staged at site creation. The
// sites module calls it right after an activation succeeds — the moment the
// site's account first exists. It reports whether anything was applied; (false,
// nil) means nothing was staged. On success the pending hash is destroyed.
func (m *Module) ProvisionPendingCredentials(ctx context.Context, siteID string) (bool, error) {
	row := new(accessModel)
	err := m.database.NewSelect().Model(row).Where("site_id = ?", siteID).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if row.PendingHash == nil || *row.PendingHash == "" {
		return false, nil
	}
	site, err := m.sites.Get(ctx, siteID)
	if err != nil {
		return false, err
	}
	// Same mutual exclusion as every other apply; the operator re-checks on the
	// node, this check just gives the clearer of the two errors.
	if m.ssh != nil {
		interactive, err := m.ssh.AccessEnabled(ctx, siteID)
		if err != nil {
			return false, err
		}
		if interactive {
			return false, errors.New("per-site SSH access is enabled, and one account cannot have both an SSH login and an SFTP jail")
		}
	}
	if _, err := m.operator.Apply(ctx, sftpoperator.Request{
		Slug: site.Slug, UnixUser: site.UnixUser, RootPath: site.RootPath, Enabled: true, PasswordHash: *row.PendingHash,
	}); err != nil {
		return false, err
	}
	now := m.now().UTC()
	if _, err := m.database.NewUpdate().Model((*accessModel)(nil)).
		Set("enabled = ?", true).Set("username = ?", site.UnixUser).
		Set("password_set_at = ?", now).Set("pending_hash = NULL").Set("updated_at = ?", now).
		Where("site_id = ?", siteID).Exec(ctx); err != nil {
		// The node is configured but the record is stale; surface it so the job
		// log says so, and leave the hash for the next activation to reconcile.
		return true, err
	}
	return true, nil
}

// upsert records the SFTP state. On disable it clears password_set_at so the UI
// never claims a live credential for a locked account.
func (m *Module) upsert(ctx context.Context, site sites.Site, enabled bool, passwordSetAt *time.Time) error {
	row := &accessModel{SiteID: site.ID, Enabled: enabled, Username: site.UnixUser, PasswordSetAt: passwordSetAt, UpdatedAt: m.now().UTC()}
	return m.database.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		// A direct enable, reset, or disable supersedes anything staged at
		// creation, so the pending hash is destroyed alongside.
		query := tx.NewInsert().Model(row).On("CONFLICT (site_id) DO UPDATE").
			Set("enabled = EXCLUDED.enabled").Set("username = EXCLUDED.username").
			Set("pending_hash = NULL").Set("updated_at = EXCLUDED.updated_at")
		if passwordSetAt != nil {
			query = query.Set("password_set_at = EXCLUDED.password_set_at")
		} else {
			query = query.Set("password_set_at = NULL")
		}
		_, err := query.Exec(ctx)
		return err
	})
}
