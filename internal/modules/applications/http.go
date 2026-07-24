package applications

import (
	"database/sql"
	"errors"
	"net/http"

	"github.com/nexa-panel/nexa-panel/internal/platform/httpapi"
	"github.com/nexa-panel/nexa-panel/internal/platform/module"
	packagesoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/packages"
	"github.com/nexa-panel/nexa-panel/internal/platform/webhandler"
)

func (m *Module) registerHTTP(registry module.Registry) error {
	return webhandler.Register(registry, map[string]http.HandlerFunc{
		"listApplications":         m.listHTTP,
		"prepareApplicationChange": m.changeHTTP,
		"getApplicationPlan":       m.planHTTP,
		"applyApplicationPlan":     m.applyHTTP,
	})
}

func (m *Module) listHTTP(w http.ResponseWriter, r *http.Request) {
	items, err := m.List(r.Context())
	if err != nil {
		httpapi.WriteError(w, http.StatusServiceUnavailable, "applications_unavailable", "The installable application catalog could not be inspected.")
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (m *Module) changeHTTP(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Action packagesoperator.Action `json:"action"`
	}
	if httpapi.DecodeJSON(w, r, &request) != nil {
		httpapi.WriteError(w, http.StatusBadRequest, "invalid_request", "Request body must be valid JSON.")
		return
	}
	actor, ok := webhandler.ActorID(r)
	if !ok {
		httpapi.WriteError(w, http.StatusUnauthorized, "authentication_required", "Sign in to continue.")
		return
	}
	application, job, err := m.RequestChange(r.Context(), r.PathValue("id"), request.Action, actor)
	if err != nil {
		httpapi.WriteError(w, http.StatusUnprocessableEntity, "application_change_invalid", err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusAccepted, map[string]any{"application": application, "job": job})
}

func (m *Module) planHTTP(w http.ResponseWriter, r *http.Request) {
	plan, err := m.StoredPlan(r.Context(), r.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) {
		httpapi.WriteError(w, http.StatusNotFound, "application_plan_not_found", "An application plan is not ready.")
		return
	}
	if err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, "application_plan_unavailable", "Application plan could not be loaded.")
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"plan": plan})
}

func (m *Module) applyHTTP(w http.ResponseWriter, r *http.Request) {
	actor, ok := webhandler.ActorID(r)
	if !ok {
		httpapi.WriteError(w, http.StatusUnauthorized, "authentication_required", "Sign in to continue.")
		return
	}
	job, err := m.ApplyPlan(r.Context(), r.PathValue("id"), actor)
	if err != nil {
		httpapi.WriteError(w, http.StatusConflict, "application_plan_not_applicable", err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusAccepted, job)
}
