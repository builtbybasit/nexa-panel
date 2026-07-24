package deploy

import (
	"net/http"

	"github.com/nexa-panel/nexa-panel/internal/platform/httpapi"
	"github.com/nexa-panel/nexa-panel/internal/platform/module"
	deployoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/deploy"
	"github.com/nexa-panel/nexa-panel/internal/platform/webhandler"
)

func (m *Module) registerLayoutHTTP(registry module.Registry) error {
	return webhandler.Register(registry, map[string]http.HandlerFunc{
		"setSiteDeploymentMode": m.deploymentModeHTTP,
		"getSiteSharedEnv":      m.sharedEnvHTTP,
		"updateSiteSharedEnv":   m.updateSharedEnvHTTP,
	})
}

type modeRequest struct {
	Mode string `json:"mode"`
}

// envRequestBody carries the whole document. It is decoded with an explicit
// limit because the default is 16 KiB (httpapi.go:17) and a shared environment
// file may be four times that.
type envRequestBody struct {
	Content string `json:"content"`
}

// deploymentModeHTTP switches the site between the panel-owned document root
// and the deployer's release tree. The re-render goes through the sites
// module's existing settings job rather than a second apply path, so a mode
// switch is exactly as safe as a settings change: byte-exact re-plan, one-shot
// apply, automatic rollback on any node failure.
func (m *Module) deploymentModeHTTP(w http.ResponseWriter, r *http.Request) {
	site, actor, ok := m.resolveActor(w, r)
	if !ok {
		return
	}
	var request modeRequest
	if httpapi.DecodeJSON(w, r, &request) != nil {
		httpapi.WriteError(w, http.StatusBadRequest, "invalid_request", "Request body must be valid JSON.")
		return
	}
	change, err := m.switchMode(r.Context(), site, request.Mode, actor, httpapi.RemoteAddress(r))
	if err != nil {
		writeFailure(w, err)
		return
	}
	if change.Job == nil {
		httpapi.WriteJSON(w, http.StatusOK, change)
		return
	}
	httpapi.WriteJSON(w, http.StatusAccepted, change)
}

func (m *Module) sharedEnvHTTP(w http.ResponseWriter, r *http.Request) {
	site, ok := m.resolveSite(w, r)
	if !ok {
		return
	}
	shared, err := m.sharedEnv(r.Context(), site)
	if err != nil {
		writeFailure(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, shared)
}

func (m *Module) updateSharedEnvHTTP(w http.ResponseWriter, r *http.Request) {
	site, actor, ok := m.resolveActor(w, r)
	if !ok {
		return
	}
	var request envRequestBody
	if httpapi.DecodeJSONLimit(w, r, &request, sharedEnvBodyLimit) != nil {
		httpapi.WriteError(w, http.StatusBadRequest, "invalid_request", "Request body must be valid JSON.")
		return
	}
	shared, err := m.writeSharedEnv(r.Context(), site, request.Content, actor, httpapi.RemoteAddress(r))
	if err != nil {
		writeFailure(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, shared)
}

// sharedEnvBodyLimit leaves room for JSON escaping on top of a document at the
// operator's cap, so a document the operator would accept is never refused by
// the reader instead — with a message about the body rather than about the file.
const sharedEnvBodyLimit = int64(4*deployoperator.MaxSharedEnvBytes + 1024)
