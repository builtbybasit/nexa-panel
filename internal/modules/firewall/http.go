package firewall

import (
	"errors"
	"net/http"

	"github.com/nexa-panel/nexa-panel/internal/platform/audit"
	"github.com/nexa-panel/nexa-panel/internal/platform/httpapi"
	"github.com/nexa-panel/nexa-panel/internal/platform/identity"
	"github.com/nexa-panel/nexa-panel/internal/platform/module"
	firewalloperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/firewall"
)

func (m *Module) registerHTTP(registry module.Registry) error {
	routes := []struct {
		pattern, permission string
		handler             http.Handler
	}{
		{"GET /api/v1/firewall", "firewall.read", http.HandlerFunc(m.statusHTTP)},
		{"POST /api/v1/firewall/action", "firewall.write", http.HandlerFunc(m.actionHTTP)},
	}
	for _, route := range routes {
		if err := registry.HandleAuthorized(route.pattern, route.permission, route.handler); err != nil {
			return err
		}
	}
	return nil
}

func (m *Module) statusHTTP(w http.ResponseWriter, r *http.Request) {
	status, err := m.Status(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "firewall_unavailable", "The firewall status could not be read. The node agent may be unreachable.")
		return
	}
	writeJSON(w, http.StatusOK, status)
}

// actionRequest carries the change verb and the rule it targets. The rule is
// ignored for enable/disable.
type actionRequest struct {
	Action string                `json:"action"`
	Rule   firewalloperator.Rule `json:"rule"`
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
	job, err := m.Submit(r.Context(), firewalloperator.Change{Action: request.Action, Rule: request.Rule}, actor, httpapi.RemoteAddress(r))
	if errors.Is(err, audit.ErrUnauditable) {
		writeError(w, http.StatusServiceUnavailable, "audit_unavailable", "The change was refused because it could not be recorded in the audit log.")
		return
	}
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "firewall_action_invalid", err.Error())
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
