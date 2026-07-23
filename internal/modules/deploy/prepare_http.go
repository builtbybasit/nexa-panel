package deploy

import (
	"net/http"

	"github.com/nexa-panel/nexa-panel/internal/platform/httpapi"
	"github.com/nexa-panel/nexa-panel/internal/platform/module"
)

func (m *Module) registerPrepareHTTP(registry module.Registry) error {
	return registry.HandleAuthorized("POST /api/v1/sites/{id}/deployment/prepare", "deploy.write", http.HandlerFunc(m.prepareHTTP))
}

// prepareHTTP starts the node preparation. There is no request body: the only
// input is the site, and the branch to prepare is the one the site already
// serves — a caller who could name a version could name a package.
func (m *Module) prepareHTTP(w http.ResponseWriter, r *http.Request) {
	site, actor, ok := m.resolveActor(w, r)
	if !ok {
		return
	}
	job, err := m.prepare(r.Context(), site, actor, httpapi.RemoteAddress(r))
	if err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"job": job})
}
