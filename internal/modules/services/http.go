package services

import (
	"errors"
	"net/http"

	"github.com/nexa-panel/nexa-panel/internal/modules/safeguard"
	"github.com/nexa-panel/nexa-panel/internal/platform/httpapi"
	"github.com/nexa-panel/nexa-panel/internal/platform/module"
	"github.com/nexa-panel/nexa-panel/internal/platform/webhandler"
)

// registerHTTP binds every handler to the route and permission its operationId
// declares in the OpenAPI contract. Method, path, and required permission come
// from the embedded spec (internal/platform/httpapi/apispec), so this map is the
// whole routing table and a renamed or missing operation fails startup instead
// of drifting from the published contract.
func (m *Module) registerHTTP(registry module.Registry) error {
	return webhandler.Register(registry, map[string]http.HandlerFunc{
		"listServices":         m.listHTTP,
		"runServiceAction":     m.actionHTTP,
		"listServiceReverts":   m.revertsHTTP,
		"confirmServiceRevert": m.confirmRevertHTTP,
	})
}

func (m *Module) listHTTP(w http.ResponseWriter, r *http.Request) {
	services, err := m.List(r.Context())
	if err != nil {
		httpapi.WriteError(w, http.StatusServiceUnavailable, "services_unavailable", "Services could not be discovered. The node agent may be unreachable.")
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"items": services})
}

// actionRequest carries the unit and action in the body rather than the path
// because template unit names (e.g. postgresql@16-main.service) contain "@",
// an awkward path-encoding edge case the other routes avoid.
//
// ConfirmLockoutRisk is the caller's acknowledgement that a stop the server
// judged access-critical should proceed anyway, behind an armed automatic
// revert.
type actionRequest struct {
	Unit               string `json:"unit"`
	Action             string `json:"action"`
	ConfirmLockoutRisk bool   `json:"confirmLockoutRisk"`
}

func (m *Module) actionHTTP(w http.ResponseWriter, r *http.Request) {
	var request actionRequest
	if httpapi.DecodeJSON(w, r, &request) != nil {
		httpapi.WriteError(w, http.StatusBadRequest, "invalid_request", "Request body must be valid JSON.")
		return
	}
	actor, ok := webhandler.ActorID(r)
	if !ok {
		httpapi.WriteError(w, http.StatusUnauthorized, "authentication_required", "Sign in to continue.")
		return
	}
	submission, err := m.Toggle(r.Context(), request.Unit, request.Action, actor, request.ConfirmLockoutRisk)
	var risk *safeguard.RiskError
	if errors.As(err, &risk) {
		// 409 rather than 422: the request is well-formed and will be accepted
		// unchanged once the caller acknowledges the consequence.
		// The reasons are repeated inside message because the shared client error
		// envelope carries only code and message; a caller that reads just the
		// message still learns exactly why the stop was refused.
		httpapi.WriteJSON(w, http.StatusConflict, map[string]any{
			"code":                "lockout_risk",
			"message":             "Stopping this service can cut off your access to this server. " + risk.Error(),
			"reasons":             risk.Reasons,
			"revertWindowSeconds": m.RevertWindowSeconds(),
		})
		return
	}
	if err != nil {
		httpapi.WriteError(w, http.StatusUnprocessableEntity, "service_action_invalid", err.Error())
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
// permission-checked, so reaching it proves the operator still has a working
// panel session — the verified alternative the guard was waiting for.
func (m *Module) confirmRevertHTTP(w http.ResponseWriter, r *http.Request) {
	var request struct {
		ID string `json:"id"`
	}
	if httpapi.DecodeJSON(w, r, &request) != nil {
		httpapi.WriteError(w, http.StatusBadRequest, "invalid_request", "Request body must be valid JSON.")
		return
	}
	revert, err := m.ConfirmRevert(r.Context(), request.ID)
	if err != nil {
		httpapi.WriteError(w, http.StatusNotFound, "revert_not_found", "That automatic rollback is no longer pending.")
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, revert)
}
