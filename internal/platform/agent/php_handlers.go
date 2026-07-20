package agent

import (
	"net/http"

	phpoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/php"
)

// WithPHPOperator attaches the PHP extension/settings operator to the agent.
func WithPHPOperator(operator phpoperator.Operator) Option {
	return func(server *Server) { server.php = operator }
}

// The three read handlers mutate nothing, so the bearer token that reached them
// is the whole authorization story — no signed plan is required.
func (s *Server) phpVersionsHTTP(w http.ResponseWriter, r *http.Request) {
	versions, err := s.php.Versions(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "php_versions_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"versions": versions})
}

func (s *Server) phpExtensionsHTTP(w http.ResponseWriter, r *http.Request) {
	extensions, err := s.php.Extensions(r.Context(), r.URL.Query().Get("version"))
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "php_extensions_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"extensions": extensions})
}

func (s *Server) phpSettingsHTTP(w http.ResponseWriter, r *http.Request) {
	directives, err := s.php.Settings(r.Context(), r.URL.Query().Get("version"))
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "php_settings_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"directives": directives})
}

func (s *Server) phpSiteSettingsHTTP(w http.ResponseWriter, r *http.Request) {
	var scope phpoperator.SiteScope
	if err := decodeJSON(w, r, &scope); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	directives, err := s.php.SiteSettings(r.Context(), scope)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "php_site_settings_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"directives": directives})
}

func (s *Server) phpPlanHTTP(w http.ResponseWriter, r *http.Request) {
	var change phpoperator.Change
	if err := decodeJSON(w, r, &change); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	plan, err := s.php.Plan(r.Context(), change)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "php_plan_failed", err.Error())
		return
	}
	plan.Signature = s.signPHPPlan(plan)
	writeJSON(w, http.StatusOK, plan)
}

func (s *Server) phpApplyHTTP(w http.ResponseWriter, r *http.Request) {
	var plan phpoperator.Plan
	if err := decodeJSON(w, r, &plan); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if !s.verifyPHPPlan(plan) {
		writeError(w, http.StatusUnprocessableEntity, "invalid_php_plan_signature", "The PHP plan was not issued by this agent.")
		return
	}
	observation, err := s.php.Apply(r.Context(), plan)
	if err != nil {
		writeError(w, http.StatusConflict, "php_operation_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, observation)
}

func (s *Server) signPHPPlan(plan phpoperator.Plan) string {
	plan.Signature = ""
	return signPayload(s.token, "php.plan.v1", plan)
}

func (s *Server) verifyPHPPlan(plan phpoperator.Plan) bool {
	provided := plan.Signature
	plan.Signature = ""
	return verifyPayload(s.token, "php.plan.v1", plan, provided)
}
