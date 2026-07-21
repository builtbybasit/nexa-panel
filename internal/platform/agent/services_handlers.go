package agent

import (
	"net/http"

	servicesoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/services"
)

// WithServicesOperator attaches the systemd services operator to the agent.
func WithServicesOperator(operator servicesoperator.Operator) Option {
	return func(server *Server) { server.services = operator }
}

// The read handler mutates nothing, so the bearer token that reached it is the
// whole authorization story — no signed plan is required.
func (s *Server) servicesDiscoverHTTP(w http.ResponseWriter, r *http.Request) {
	services, err := s.services.Discover(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "services_discover_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"services": services})
}

func (s *Server) servicesPlanHTTP(w http.ResponseWriter, r *http.Request) {
	var change servicesoperator.Change
	if err := decodeJSON(w, r, &change); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	plan, err := s.services.Plan(r.Context(), change)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "services_plan_failed", err.Error())
		return
	}
	plan.Signature = s.signServicesPlan(plan)
	writeJSON(w, http.StatusOK, plan)
}

func (s *Server) servicesApplyHTTP(w http.ResponseWriter, r *http.Request) {
	var plan servicesoperator.Plan
	if err := decodeJSON(w, r, &plan); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if !s.verifyServicesPlan(plan) {
		writeError(w, http.StatusUnprocessableEntity, "invalid_services_plan_signature", "The service plan was not issued by this agent.")
		return
	}
	observation, err := s.services.Apply(r.Context(), plan)
	if err != nil {
		writeError(w, http.StatusConflict, "services_operation_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, observation)
}

func (s *Server) signServicesPlan(plan servicesoperator.Plan) string {
	plan.Signature = ""
	return signPayload(s.token, "services.plan.v1", plan)
}

func (s *Server) verifyServicesPlan(plan servicesoperator.Plan) bool {
	provided := plan.Signature
	plan.Signature = ""
	return verifyPayload(s.token, "services.plan.v1", plan, provided)
}
