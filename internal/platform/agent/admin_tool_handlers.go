package agent

import (
	"net/http"

	admintooloperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/admintools"
)

func WithAdminToolOperator(operator admintooloperator.Operator) Option {
	return func(server *Server) { server.adminTools = operator }
}

func (s *Server) adminToolsDiscoverHTTP(w http.ResponseWriter, r *http.Request) {
	items, err := s.adminTools.Discover(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "admin_tools_discovery_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) adminToolsPlanHTTP(w http.ResponseWriter, r *http.Request) {
	var change admintooloperator.Change
	if err := decodeJSON(w, r, &change); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	plan, err := s.adminTools.Plan(r.Context(), change)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "admin_tool_plan_failed", err.Error())
		return
	}
	plan.Signature = s.signAdminToolPlan(plan)
	writeJSON(w, http.StatusOK, plan)
}

func (s *Server) adminToolsApplyHTTP(w http.ResponseWriter, r *http.Request) {
	var execution admintooloperator.Execution
	if err := decodeJSON(w, r, &execution); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if !s.verifyAdminToolPlan(execution.Plan) {
		writeError(w, http.StatusUnprocessableEntity, "invalid_admin_tool_plan_signature", "The admin tool plan was not issued by this agent.")
		return
	}
	observation, err := s.adminTools.Apply(r.Context(), execution)
	if err != nil {
		writeError(w, http.StatusConflict, "admin_tool_operation_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, observation)
}

func (s *Server) adminToolsKeepAliveHTTP(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Kind string `json:"kind"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if err := s.adminTools.KeepAlive(r.Context(), admintooloperator.Kind(request.Kind)); err != nil {
		writeError(w, http.StatusConflict, "admin_tool_keepalive_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) signAdminToolPlan(plan admintooloperator.Plan) string {
	return s.signPlan("admin-tool.plan.v1", &plan.Signature, &plan)
}

func (s *Server) verifyAdminToolPlan(plan admintooloperator.Plan) bool {
	return s.verifyPlan("admin-tool.plan.v1", &plan.Signature, &plan)
}
