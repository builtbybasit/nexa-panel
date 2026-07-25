package agent

import (
	"net/http"

	packagesoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/packages"
)

// WithPackagesOperator attaches the apt package operator to the agent.
func WithPackagesOperator(operator packagesoperator.Operator) Option {
	return func(server *Server) { server.packages = operator }
}

// Reports what the node's repositories offer and mutates nothing, so it needs no
// signed plan — the bearer token that reached this handler is the whole
// authorization story, as with packagesDiscoverHTTP.
func (s *Server) packagesCatalogHTTP(w http.ResponseWriter, r *http.Request) {
	entries, err := s.packages.Catalog(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "packages_catalog_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": entries})
}

func (s *Server) packagesDiscoverHTTP(w http.ResponseWriter, r *http.Request) {
	items, err := s.packages.Discover(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "packages_discovery_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) packagesPlanHTTP(w http.ResponseWriter, r *http.Request) {
	var change packagesoperator.Change
	if err := decodeJSON(w, r, &change); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	plan, err := s.packages.Plan(r.Context(), change)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "package_plan_failed", err.Error())
		return
	}
	plan.Signature = s.signPackagePlan(plan)
	writeJSON(w, http.StatusOK, plan)
}

func (s *Server) packagesApplyHTTP(w http.ResponseWriter, r *http.Request) {
	var plan packagesoperator.Plan
	if err := decodeJSON(w, r, &plan); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if !s.verifyPackagePlan(plan) {
		writeError(w, http.StatusUnprocessableEntity, "invalid_package_plan_signature", "The application plan was not issued by this agent.")
		return
	}
	observation, err := s.packages.Apply(r.Context(), plan)
	if err != nil {
		writeError(w, http.StatusConflict, "package_operation_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, observation)
}

func (s *Server) signPackagePlan(plan packagesoperator.Plan) string {
	return s.signPlan("package.plan.v1", &plan.Signature, &plan)
}

func (s *Server) verifyPackagePlan(plan packagesoperator.Plan) bool {
	return s.verifyPlan("package.plan.v1", &plan.Signature, &plan)
}
