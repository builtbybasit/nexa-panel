package applications

import (
	"database/sql"
	"errors"
	"net/http"

	"github.com/nexa-panel/nexa-panel/internal/platform/httpapi"
	"github.com/nexa-panel/nexa-panel/internal/platform/identity"
	"github.com/nexa-panel/nexa-panel/internal/platform/module"
	packagesoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/packages"
)

func (m *Module) registerHTTP(registry module.Registry) error {
	routes := []struct {
		pattern, permission string
		handler             http.Handler
	}{
		{"GET /api/v1/applications", "applications.read", http.HandlerFunc(m.listHTTP)},
		{"POST /api/v1/applications/{id}/plan", "applications.write", http.HandlerFunc(m.changeHTTP)},
		{"GET /api/v1/applications/{id}/plan", "applications.read", http.HandlerFunc(m.planHTTP)},
		{"POST /api/v1/applications/{id}/apply", "operations.apply", http.HandlerFunc(m.applyHTTP)},
	}
	for _, route := range routes {
		if err := registry.HandleAuthorized(route.pattern, route.permission, route.handler); err != nil {
			return err
		}
	}
	return nil
}

func (m *Module) listHTTP(w http.ResponseWriter, r *http.Request) {
	items, err := m.List(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "applications_unavailable", "The installable application catalog could not be inspected.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (m *Module) changeHTTP(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Action packagesoperator.Action `json:"action"`
	}
	if decodeJSON(w, r, &request) != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "Request body must be valid JSON.")
		return
	}
	actor, ok := actorID(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication_required", "Sign in to continue.")
		return
	}
	application, job, err := m.RequestChange(r.Context(), r.PathValue("id"), request.Action, actor)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "application_change_invalid", err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"application": application, "job": job})
}

func (m *Module) planHTTP(w http.ResponseWriter, r *http.Request) {
	plan, err := m.StoredPlan(r.Context(), r.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "application_plan_not_found", "An application plan is not ready.")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "application_plan_unavailable", "Application plan could not be loaded.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"plan": plan})
}

func (m *Module) applyHTTP(w http.ResponseWriter, r *http.Request) {
	actor, ok := actorID(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication_required", "Sign in to continue.")
		return
	}
	job, err := m.ApplyPlan(r.Context(), r.PathValue("id"), actor)
	if err != nil {
		writeError(w, http.StatusConflict, "application_plan_not_applicable", err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, job)
}

func actorID(r *http.Request) (*string, bool) {
	user, ok := identity.UserFromContext(r.Context())
	if !ok {
		return nil, false
	}
	return &user.ID, true
}

var (
	decodeJSON = httpapi.DecodeJSON
	writeJSON  = httpapi.WriteJSON
	writeError = httpapi.WriteError
)
