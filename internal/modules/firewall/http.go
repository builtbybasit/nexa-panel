package firewall

import (
	"errors"
	"net/http"

	"github.com/nexa-panel/nexa-panel/internal/modules/safeguard"
	"github.com/nexa-panel/nexa-panel/internal/platform/audit"
	"github.com/nexa-panel/nexa-panel/internal/platform/httpapi"
	"github.com/nexa-panel/nexa-panel/internal/platform/module"
	firewalloperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/firewall"
	"github.com/nexa-panel/nexa-panel/internal/platform/webhandler"
)

// registerHTTP binds every handler to the route and permission its operationId
// declares in the OpenAPI contract. Method, path, and required permission come
// from the embedded spec (internal/platform/httpapi/apispec), so this map is the
// whole routing table and a renamed or missing operation fails startup instead
// of drifting from the published contract.
func (m *Module) registerHTTP(registry module.Registry) error {
	return webhandler.Register(registry, map[string]http.HandlerFunc{
		"getFirewallStatus":     m.statusHTTP,
		"submitFirewallAction":  m.actionHTTP,
		"listFirewallReverts":   m.revertsHTTP,
		"confirmFirewallRevert": m.confirmRevertHTTP,
	})
}

func (m *Module) statusHTTP(w http.ResponseWriter, r *http.Request) {
	status, err := m.Status(r.Context())
	if err != nil {
		httpapi.WriteError(w, http.StatusServiceUnavailable, "firewall_unavailable", "The firewall status could not be read. The node agent may be unreachable.")
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, status)
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
	request, decodeErr := webhandler.Decode[actionRequest](w, r)
	if decodeErr != nil {
		webhandler.Fail(w, decodeErr)
		return
	}
	actor, ok := webhandler.ActorID(r)
	if !ok {
		httpapi.WriteError(w, http.StatusUnauthorized, "authentication_required", "Sign in to continue.")
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
		httpapi.WriteJSON(w, http.StatusConflict, map[string]any{
			"code":                "lockout_risk",
			"message":             "This change can cut off your access to this server. " + risk.Error(),
			"reasons":             risk.Reasons,
			"revertWindowSeconds": m.RevertWindowSeconds(),
		})
		return
	}
	if errors.Is(err, audit.ErrUnauditable) {
		httpapi.WriteError(w, http.StatusServiceUnavailable, "audit_unavailable", "The change was refused because it could not be recorded in the audit log.")
		return
	}
	if err != nil {
		httpapi.WriteError(w, http.StatusUnprocessableEntity, "firewall_action_invalid", err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusAccepted, submission)
}

func (m *Module) revertsHTTP(w http.ResponseWriter, r *http.Request) {
	reverts, err := m.Reverts(r.Context())
	if err != nil {
		httpapi.WriteError(w, http.StatusServiceUnavailable, "reverts_unavailable", "Pending automatic rollbacks could not be read.")
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"items": reverts, "windowSeconds": m.RevertWindowSeconds()})
}

// confirmRevertHTTP disarms one pending revert. The route is authenticated and
// permission-checked, so reaching it proves the operator's session still works —
// which is the alternative access the guard was waiting to see verified.
func (m *Module) confirmRevertHTTP(w http.ResponseWriter, r *http.Request) {
	request, decodeErr := webhandler.Decode[struct {
		ID string `json:"id"`
	}](w, r)
	if decodeErr != nil {
		webhandler.Fail(w, decodeErr)
		return
	}
	revert, err := m.ConfirmRevert(r.Context(), request.ID)
	if err != nil {
		httpapi.WriteError(w, http.StatusNotFound, "revert_not_found", "That automatic rollback is no longer pending.")
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, revert)
}
