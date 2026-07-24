package admintools

import (
	"database/sql"
	"errors"
	"net/http"

	"github.com/nexa-panel/nexa-panel/internal/platform/httpapi"
	"github.com/nexa-panel/nexa-panel/internal/platform/module"
	admintooloperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/admintools"
	"github.com/nexa-panel/nexa-panel/internal/platform/webhandler"
)

// registerHTTP binds every handler to the route and permission its operationId
// declares in the OpenAPI contract. Method, path, and required permission come
// from the embedded spec (internal/platform/httpapi/apispec), so this map is the
// whole routing table and a renamed or missing operation fails startup instead
// of drifting from the published contract. The tool proxy route is intentionally
// absent from the contract, so it keeps its bespoke registration below the map.
func (m *Module) registerHTTP(registry module.Registry) error {
	if err := webhandler.Register(registry, map[string]http.HandlerFunc{
		"listAdminTools":         m.listHTTP,
		"prepareAdminToolChange": m.changeHTTP,
		"getAdminToolPlan":       m.planHTTP,
		"applyAdminToolPlan":     m.applyHTTP,
		"createAdminToolLaunch":  m.launchHTTP,
	}); err != nil {
		return err
	}
	return registry.HandleAuthenticated("/tools/{kind}/{path...}", http.HandlerFunc(m.proxyHTTP))
}

func (m *Module) listHTTP(w http.ResponseWriter, r *http.Request) {
	items, err := m.Sync(r.Context())
	if err != nil {
		httpapi.WriteError(w, 503, "admin_tools_unavailable", "Admin tools could not be inspected.")
		return
	}
	httpapi.WriteJSON(w, 200, map[string]any{"items": items})
}

func (m *Module) changeHTTP(w http.ResponseWriter, r *http.Request) {
	request, decodeErr := webhandler.Decode[struct {
		Action admintooloperator.Action `json:"action"`
	}](w, r)
	if decodeErr != nil {
		webhandler.Fail(w, decodeErr)
		return
	}
	actor, ok := webhandler.ActorID(r)
	if !ok {
		httpapi.WriteError(w, 401, "authentication_required", "Sign in to continue.")
		return
	}
	tool, job, err := m.RequestChange(r.Context(), admintooloperator.Kind(r.PathValue("kind")), request.Action, actor)
	if err != nil {
		httpapi.WriteError(w, 422, "admin_tool_change_invalid", err.Error())
		return
	}
	httpapi.WriteJSON(w, 202, map[string]any{"tool": tool, "job": job})
}

func (m *Module) planHTTP(w http.ResponseWriter, r *http.Request) {
	plan, err := m.StoredPlan(r.Context(), admintooloperator.Kind(r.PathValue("kind")))
	if errors.Is(err, sql.ErrNoRows) {
		httpapi.WriteError(w, 404, "admin_tool_plan_not_found", "An admin tool plan is not ready.")
		return
	}
	if err != nil {
		httpapi.WriteError(w, 500, "admin_tool_plan_unavailable", "Admin tool plan could not be loaded.")
		return
	}
	httpapi.WriteJSON(w, 200, map[string]any{"plan": plan})
}

func (m *Module) applyHTTP(w http.ResponseWriter, r *http.Request) {
	actor, ok := webhandler.ActorID(r)
	if !ok {
		httpapi.WriteError(w, 401, "authentication_required", "Sign in to continue.")
		return
	}
	job, err := m.ApplyPlan(r.Context(), admintooloperator.Kind(r.PathValue("kind")), actor)
	if err != nil {
		httpapi.WriteError(w, 409, "admin_tool_plan_not_applicable", err.Error())
		return
	}
	httpapi.WriteJSON(w, 202, job)
}
