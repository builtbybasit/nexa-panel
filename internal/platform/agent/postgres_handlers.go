package agent

import (
	"net/http"

	postgresoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/postgres"
)

func WithPostgresOperator(operator postgresoperator.Operator) Option {
	return func(server *Server) { server.postgres = operator }
}

func (s *Server) postgresDiscoverHTTP(w http.ResponseWriter, r *http.Request) {
	items, err := s.postgres.Discover(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "postgresql_discovery_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// Reads observed state and mutates nothing, so it needs no signed plan — the
// bearer token that reached this handler is the whole authorization story, as
// with postgresDiscoverHTTP.
func (s *Server) postgresSizesHTTP(w http.ResponseWriter, r *http.Request) {
	sizes, err := s.postgres.Sizes(r.Context(), r.URL.Query().Get("instanceId"))
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "postgresql_sizes_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sizes": sizes})
}

func (s *Server) postgresPlanHTTP(w http.ResponseWriter, r *http.Request) {
	var change postgresoperator.Change
	if err := decodeJSON(w, r, &change); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	plan, err := s.postgres.Plan(r.Context(), change)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "postgresql_plan_failed", err.Error())
		return
	}
	plan.Signature = s.signPostgresPlan(plan)
	writeJSON(w, http.StatusOK, plan)
}

func (s *Server) postgresApplyHTTP(w http.ResponseWriter, r *http.Request) {
	var execution postgresoperator.Execution
	if err := decodeJSON(w, r, &execution); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if !s.verifyPostgresPlan(execution.Plan) {
		writeError(w, http.StatusUnprocessableEntity, "invalid_postgresql_plan_signature", "The PostgreSQL plan was not issued by this agent.")
		return
	}
	observation, err := s.postgres.Apply(r.Context(), execution)
	if err != nil {
		writeError(w, http.StatusConflict, "postgresql_operation_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, observation)
}

func (s *Server) signPostgresPlan(plan postgresoperator.Plan) string {
	plan.Signature = ""
	return signPayload(s.token, "postgresql.plan.v1", plan)
}

func (s *Server) verifyPostgresPlan(plan postgresoperator.Plan) bool {
	provided := plan.Signature
	plan.Signature = ""
	return verifyPayload(s.token, "postgresql.plan.v1", plan, provided)
}
