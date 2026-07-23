package deploy

import (
	"errors"
	"net/http"

	"github.com/nexa-panel/nexa-panel/internal/modules/sites"
	"github.com/nexa-panel/nexa-panel/internal/platform/audit"
	"github.com/nexa-panel/nexa-panel/internal/platform/httpapi"
	"github.com/nexa-panel/nexa-panel/internal/platform/identity"
	"github.com/nexa-panel/nexa-panel/internal/platform/module"
)

func (m *Module) registerSSHHTTP(registry module.Registry) error {
	routes := []struct {
		pattern, permission string
		handler             http.Handler
	}{
		{"GET /api/v1/sites/{id}/ssh", "deploy.read", http.HandlerFunc(m.sshStatusHTTP)},
		{"POST /api/v1/sites/{id}/ssh/enable", "deploy.write", http.HandlerFunc(m.sshEnableHTTP)},
		{"POST /api/v1/sites/{id}/ssh/disable", "deploy.write", http.HandlerFunc(m.sshDisableHTTP)},
		{"POST /api/v1/sites/{id}/ssh/keys", "deploy.write", http.HandlerFunc(m.sshAddKeyHTTP)},
		{"DELETE /api/v1/sites/{id}/ssh/keys/{keyId}", "deploy.write", http.HandlerFunc(m.sshRemoveKeyHTTP)},
		{"POST /api/v1/sites/{id}/ssh/keys/generate", "deploy.write", http.HandlerFunc(m.sshGenerateKeyHTTP)},
	}
	for _, route := range routes {
		if err := registry.HandleAuthorized(route.pattern, route.permission, route.handler); err != nil {
			return err
		}
	}
	return nil
}

// keyRequest carries a pasted public key, or just the label when the panel is
// asked to generate one.
type keyRequest struct {
	Label     string `json:"label"`
	PublicKey string `json:"publicKey"`
}

func (m *Module) sshStatusHTTP(w http.ResponseWriter, r *http.Request) {
	site, ok := m.resolveSite(w, r)
	if !ok {
		return
	}
	access, err := m.currentAccess(r.Context(), m.database, site)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "deploy_unavailable", "SSH access could not be loaded.")
		return
	}
	writeJSON(w, http.StatusOK, access)
}

func (m *Module) sshEnableHTTP(w http.ResponseWriter, r *http.Request) {
	site, actor, ok := m.resolveActor(w, r)
	if !ok {
		return
	}
	access, err := m.enable(r.Context(), site, actor, httpapi.RemoteAddress(r))
	m.respond(w, access, err)
}

func (m *Module) sshDisableHTTP(w http.ResponseWriter, r *http.Request) {
	site, actor, ok := m.resolveActor(w, r)
	if !ok {
		return
	}
	access, err := m.disable(r.Context(), site, actor, httpapi.RemoteAddress(r))
	m.respond(w, access, err)
}

func (m *Module) sshAddKeyHTTP(w http.ResponseWriter, r *http.Request) {
	site, actor, ok := m.resolveActor(w, r)
	if !ok {
		return
	}
	var request keyRequest
	if decodeJSON(w, r, &request) != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "Request body must be valid JSON.")
		return
	}
	access, err := m.addKey(r.Context(), site, request.Label, request.PublicKey, actor, httpapi.RemoteAddress(r))
	m.respond(w, access, err)
}

func (m *Module) sshRemoveKeyHTTP(w http.ResponseWriter, r *http.Request) {
	site, actor, ok := m.resolveActor(w, r)
	if !ok {
		return
	}
	access, err := m.removeKey(r.Context(), site, r.PathValue("keyId"), actor, httpapi.RemoteAddress(r))
	m.respond(w, access, err)
}

// sshGenerateKeyHTTP returns the private half exactly once, in this response.
// Nothing stores it: a caller who loses it generates another key.
func (m *Module) sshGenerateKeyHTTP(w http.ResponseWriter, r *http.Request) {
	site, actor, ok := m.resolveActor(w, r)
	if !ok {
		return
	}
	var request keyRequest
	if decodeJSON(w, r, &request) != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "Request body must be valid JSON.")
		return
	}
	generated, err := m.generateKey(r.Context(), site, request.Label, actor, httpapi.RemoteAddress(r))
	if err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, generated)
}

// resolveActor resolves the site and the human behind the request together:
// every mutation here is audited, and an unattributable change is not one this
// module accepts.
func (m *Module) resolveActor(w http.ResponseWriter, r *http.Request) (sites.Site, *string, bool) {
	site, ok := m.resolveSite(w, r)
	if !ok {
		return sites.Site{}, nil, false
	}
	user, ok := identity.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication_required", "Sign in to continue.")
		return sites.Site{}, nil, false
	}
	return site, &user.ID, true
}

func (m *Module) respond(w http.ResponseWriter, access SSHAccess, err error) {
	if err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, access)
}

// writeFailure translates the module's own refusals into their intended status
// and reports everything else as an internal failure. An unauditable change is
// refused rather than applied, so it gets its own machine code.
func writeFailure(w http.ResponseWriter, err error) {
	var refused *refusal
	if errors.As(err, &refused) {
		writeError(w, refused.status, refused.code, refused.message)
		return
	}
	if errors.Is(err, audit.ErrUnauditable) {
		writeError(w, http.StatusServiceUnavailable, "audit_unavailable", "The change was refused because it could not be recorded in the audit log.")
		return
	}
	writeError(w, http.StatusInternalServerError, "deploy_state_failed", "The change could not be completed.")
}
