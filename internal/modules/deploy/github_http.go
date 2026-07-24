package deploy

import (
	"net/http"

	"github.com/nexa-panel/nexa-panel/internal/platform/httpapi"
	"github.com/nexa-panel/nexa-panel/internal/platform/module"
	"github.com/nexa-panel/nexa-panel/internal/platform/webhandler"
)

func (m *Module) registerGitHubHTTP(registry module.Registry) error {
	return webhandler.Register(registry, map[string]http.HandlerFunc{
		"getSiteDeployKey":    m.deployKeyHTTP,
		"ensureSiteDeployKey": m.ensureDeployKeyHTTP,
		"testSiteDeployKey":   m.testDeployKeyHTTP,
	})
}

// deployKeyRequest carries the repository the key is for, and whether the pair
// itself should be replaced. Rotate is a field rather than its own endpoint
// because the caller's intent is "make this site's key right", and rotation is
// the one variant of that which invalidates what GitHub already trusts.
type deployKeyRequest struct {
	Repository string `json:"repository"`
	Rotate     bool   `json:"rotate"`
}

func (m *Module) deployKeyHTTP(w http.ResponseWriter, r *http.Request) {
	site, ok := m.resolveSite(w, r)
	if !ok {
		return
	}
	key, err := m.deployKey(r.Context(), site)
	if err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, "deploy_unavailable", "The deploy key could not be loaded.")
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, key)
}

func (m *Module) ensureDeployKeyHTTP(w http.ResponseWriter, r *http.Request) {
	site, actor, ok := m.resolveActor(w, r)
	if !ok {
		return
	}
	var request deployKeyRequest
	if httpapi.DecodeJSON(w, r, &request) != nil {
		httpapi.WriteError(w, http.StatusBadRequest, "invalid_request", "Request body must be valid JSON.")
		return
	}
	key, err := m.ensureKey(r.Context(), site, request.Repository, request.Rotate, actor, httpapi.RemoteAddress(r))
	if err != nil {
		writeFailure(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, key)
}

func (m *Module) testDeployKeyHTTP(w http.ResponseWriter, r *http.Request) {
	site, actor, ok := m.resolveActor(w, r)
	if !ok {
		return
	}
	var request deployKeyRequest
	if httpapi.DecodeJSON(w, r, &request) != nil {
		httpapi.WriteError(w, http.StatusBadRequest, "invalid_request", "Request body must be valid JSON.")
		return
	}
	job, err := m.testGitHub(r.Context(), site, request.Repository, actor, httpapi.RemoteAddress(r))
	if err != nil {
		writeFailure(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusAccepted, map[string]any{"job": job})
}
