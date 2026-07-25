package agent

import (
	"net/http"

	mysqloperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/mysql"
)

func WithMySQLOperator(operator mysqloperator.Operator) Option {
	return func(server *Server) { server.mysql = operator }
}

func (s *Server) mysqlDiscoverHTTP(w http.ResponseWriter, r *http.Request) {
	engine, err := s.mysql.Discover(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "mysql_family_discovery_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"engine": engine})
}

// Read-only, so no signed plan is involved — the bearer token is the whole
// authorization story, as with mysqlDiscoverHTTP.
func (s *Server) mysqlSizesHTTP(w http.ResponseWriter, r *http.Request) {
	sizes, err := s.mysql.Sizes(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "mysql_family_sizes_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sizes": sizes})
}

func (s *Server) mysqlPlanHTTP(w http.ResponseWriter, r *http.Request) {
	var change mysqloperator.Change
	if err := decodeJSON(w, r, &change); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	plan, err := s.mysql.Plan(r.Context(), change)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "mysql_family_plan_failed", err.Error())
		return
	}
	plan.Signature = s.signMySQLPlan(plan)
	writeJSON(w, http.StatusOK, plan)
}

func (s *Server) mysqlApplyHTTP(w http.ResponseWriter, r *http.Request) {
	var execution mysqloperator.Execution
	if err := decodeJSON(w, r, &execution); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if !s.verifyMySQLPlan(execution.Plan) {
		writeError(w, http.StatusUnprocessableEntity, "invalid_mysql_family_plan_signature", "The MySQL-family plan was not issued by this agent.")
		return
	}
	observation, err := s.mysql.Apply(r.Context(), execution)
	if err != nil {
		writeError(w, http.StatusConflict, "mysql_family_operation_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, observation)
}

func (s *Server) signMySQLPlan(plan mysqloperator.Plan) string {
	return s.signPlan("mysql-family.plan.v1", &plan.Signature, &plan)
}

func (s *Server) verifyMySQLPlan(plan mysqloperator.Plan) bool {
	return s.verifyPlan("mysql-family.plan.v1", &plan.Signature, &plan)
}
