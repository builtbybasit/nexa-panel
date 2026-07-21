package services

import (
	"net/http"

	"github.com/nexa-panel/nexa-panel/internal/platform/httpapi"
	"github.com/nexa-panel/nexa-panel/internal/platform/identity"
	"github.com/nexa-panel/nexa-panel/internal/platform/module"
)

func (m *Module) registerHTTP(registry module.Registry) error {
	routes := []struct {
		pattern, permission string
		handler             http.Handler
	}{
		{"GET /api/v1/services", "services.read", http.HandlerFunc(m.listHTTP)},
		{"POST /api/v1/services/action", "services.write", http.HandlerFunc(m.actionHTTP)},
	}
	for _, route := range routes {
		if err := registry.HandleAuthorized(route.pattern, route.permission, route.handler); err != nil {
			return err
		}
	}
	return nil
}

func (m *Module) listHTTP(w http.ResponseWriter, r *http.Request) {
	services, err := m.List(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "services_unavailable", "Services could not be discovered. The node agent may be unreachable.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": services})
}

// actionRequest carries the unit and action in the body rather than the path
// because template unit names (e.g. postgresql@16-main.service) contain "@",
// an awkward path-encoding edge case the other routes avoid.
type actionRequest struct {
	Unit   string `json:"unit"`
	Action string `json:"action"`
}

func (m *Module) actionHTTP(w http.ResponseWriter, r *http.Request) {
	var request actionRequest
	if decodeJSON(w, r, &request) != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "Request body must be valid JSON.")
		return
	}
	actor, ok := actorID(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication_required", "Sign in to continue.")
		return
	}
	job, err := m.Toggle(r.Context(), request.Unit, request.Action, actor)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "service_action_invalid", err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"job": job})
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
