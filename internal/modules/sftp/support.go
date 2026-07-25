package sftp

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"math/big"
	"net/http"

	"github.com/nexa-panel/nexa-panel/internal/modules/sites"
	"github.com/nexa-panel/nexa-panel/internal/platform/httpapi"
	"github.com/nexa-panel/nexa-panel/internal/platform/identity"
)

const sshPort = 22

// passwordAlphabet omits visually ambiguous characters (0/O, 1/l/I) so a
// generated credential can be read off a screen and typed without error.
const passwordAlphabet = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"

const generatedPasswordLength = 24

// generatePassword returns a fresh high-entropy password. It is returned to the
// operator once over HTTPS and written to the node's /etc/shadow; it is never
// persisted by the control panel.
func generatePassword() (string, error) {
	buffer := make([]byte, generatedPasswordLength)
	max := big.NewInt(int64(len(passwordAlphabet)))
	for index := range buffer {
		pick, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		buffer[index] = passwordAlphabet[pick.Int64()]
	}
	return string(buffer), nil
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

// currentAccess reads the persisted SFTP state for a site, defaulting to a
// disabled record when the site has never had SFTP touched.
func (m *Module) currentAccess(ctx context.Context, site sites.Site) (Access, error) {
	row := new(accessModel)
	err := m.database.NewSelect().Model(row).Where("site_id = ?", site.ID).Scan(ctx)
	access := Access{SiteID: site.ID, Enabled: false, Username: site.UnixUser, Host: site.PrimaryDomain, Port: sshPort}
	if errors.Is(err, sql.ErrNoRows) {
		return access, nil
	}
	if err != nil {
		return Access{}, err
	}
	access.Enabled = row.Enabled
	access.PasswordSetAt = row.PasswordSetAt
	access.PendingActivation = !row.Enabled && row.PendingHash != nil && *row.PendingHash != ""
	return access, nil
}
