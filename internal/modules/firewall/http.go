package firewall

import (
	"errors"
	"net/http"

	"github.com/nexa-panel/nexa-panel/internal/modules/safeguard"
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
		{"GET /api/v1/firewall/reverts", "firewall.read", http.HandlerFunc(m.revertsHTTP)},
		{"POST /api/v1/firewall/reverts/confirm", "firewall.write", http.HandlerFunc(m.confirmRevertHTTP)},
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
// ignored for enable/disable. ConfirmLockoutRisk is the caller's acknowledgement
// that a change the server judged lockout-capable should proceed anyway, behind
// an armed automatic revert.
type actionRequest struct {
	Action             string                `json:"action"`
	Rule               firewalloperator.Rule `json:"rule"`
	ConfirmLockoutRisk bool                  `json:"confirmLockoutRisk"`
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
	change := firewalloperator.Change{Action: request.Action, Rule: request.Rule}
	submission, err := m.Submit(r.Context(), change, actor, httpapi.RemoteAddress(r), request.ConfirmLockoutRisk)
	var risk *safeguard.RiskError
	if errors.As(err, &risk) {
		// 409 rather than 422: the request is well-formed and will be accepted
		// unchanged once the caller acknowledges the consequence.
		// The reasons are repeated inside message because the shared client error
		// envelope carries only code and message; a caller that reads just the
		// message still learns exactly why the change was refused.
		writeJSON(w, http.StatusConflict, map[string]any{
			"code":                "lockout_risk",
			"message":             "This change can cut off your access to this server. " + risk.Error(),
			"reasons":             risk.Reasons,
			"revertWindowSeconds": m.RevertWindowSeconds(),
		})
		return
	}
	if errors.Is(err, audit.ErrUnauditable) {
		writeError(w, http.StatusServiceUnavailable, "audit_unavailable", "The change was refused because it could not be recorded in the audit log.")
		return
	}
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "firewall_action_invalid", err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, submission)
}

func (m *Module) revertsHTTP(w http.ResponseWriter, r *http.Request) {
	reverts, err := m.Reverts(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "reverts_unavailable", "Pending automatic rollbacks could not be read.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": reverts, "windowSeconds": m.RevertWindowSeconds()})
}

// confirmRevertHTTP disarms one pending revert. The route is authenticated and
// permission-checked, so reaching it proves the operator's session still works —
// which is the alternative access the guard was waiting to see verified.
func (m *Module) confirmRevertHTTP(w http.ResponseWriter, r *http.Request) {
	var request struct {
		ID string `json:"id"`
	}
	if decodeJSON(w, r, &request) != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "Request body must be valid JSON.")
		return
	}
	revert, err := m.ConfirmRevert(r.Context(), request.ID)
	if err != nil {
		writeError(w, http.StatusNotFound, "revert_not_found", "That automatic rollback is no longer pending.")
		return
	}
	writeJSON(w, http.StatusOK, revert)
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
