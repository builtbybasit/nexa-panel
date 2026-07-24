package deploy

import (
	"errors"
	"net/http"

	"github.com/nexa-panel/nexa-panel/internal/modules/sites"
	"github.com/nexa-panel/nexa-panel/internal/platform/audit"
	"github.com/nexa-panel/nexa-panel/internal/platform/httpapi"
	"github.com/nexa-panel/nexa-panel/internal/platform/identity"
	"github.com/nexa-panel/nexa-panel/internal/platform/module"
	"github.com/nexa-panel/nexa-panel/internal/platform/webhandler"
)

func (m *Module) registerSSHHTTP(registry module.Registry) error {
	return webhandler.Register(registry, map[string]http.HandlerFunc{
		"getSiteSshAccess":     m.sshStatusHTTP,
		"enableSiteSshAccess":  m.sshEnableHTTP,
		"disableSiteSshAccess": m.sshDisableHTTP,
		"addSiteSshKey":        m.sshAddKeyHTTP,
		"removeSiteSshKey":     m.sshRemoveKeyHTTP,
		"generateSiteSshKey":   m.sshGenerateKeyHTTP,
	})
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
		httpapi.WriteError(w, http.StatusInternalServerError, "deploy_unavailable", "SSH access could not be loaded.")
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, access)
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
	if httpapi.DecodeJSON(w, r, &request) != nil {
		httpapi.WriteError(w, http.StatusBadRequest, "invalid_request", "Request body must be valid JSON.")
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
	if httpapi.DecodeJSON(w, r, &request) != nil {
		httpapi.WriteError(w, http.StatusBadRequest, "invalid_request", "Request body must be valid JSON.")
		return
	}
	generated, err := m.generateKey(r.Context(), site, request.Label, actor, httpapi.RemoteAddress(r))
	if err != nil {
		writeFailure(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, generated)
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
		httpapi.WriteError(w, http.StatusUnauthorized, "authentication_required", "Sign in to continue.")
		return sites.Site{}, nil, false
	}
	return site, &user.ID, true
}

func (m *Module) respond(w http.ResponseWriter, access SSHAccess, err error) {
	if err != nil {
		writeFailure(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, access)
}

// writeFailure translates the module's own refusals into their intended status
// and reports everything else as an internal failure. An unauditable change is
// refused rather than applied, so it gets its own machine code.
func writeFailure(w http.ResponseWriter, err error) {
	var refused *refusal
	if errors.As(err, &refused) {
		httpapi.WriteError(w, refused.status, refused.code, refused.message)
		return
	}
	if errors.Is(err, audit.ErrUnauditable) {
		httpapi.WriteError(w, http.StatusServiceUnavailable, "audit_unavailable", "The change was refused because it could not be recorded in the audit log.")
		return
	}
	httpapi.WriteError(w, http.StatusInternalServerError, "deploy_state_failed", "The change could not be completed.")
}
