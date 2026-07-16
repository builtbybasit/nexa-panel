package agent

import (
	"crypto/hmac"

	"crypto/sha256"
	"crypto/subtle"

	"encoding/json"
	"fmt"
	siteoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/sites"

	"net/http"
)

func WithSiteOperator(operator siteoperator.Operator) Option {
	return func(server *Server) { server.sites = operator }
}

func (s *Server) sitePlanHTTP(w http.ResponseWriter, r *http.Request) {
	var site siteoperator.Site
	if err := decodeJSON(w, r, &site); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	plan, err := s.sites.Plan(r.Context(), site)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "site_plan_failed", err.Error())
		return
	}
	plan.Signature = s.signSitePlan(plan)
	writeJSON(w, http.StatusOK, plan)
}

func (s *Server) siteApplyHTTP(w http.ResponseWriter, r *http.Request) {
	s.sitePlanMutation(w, r, false)
}

func (s *Server) siteRollbackHTTP(w http.ResponseWriter, r *http.Request) {
	s.sitePlanMutation(w, r, true)
}

func (s *Server) sitePlanMutation(w http.ResponseWriter, r *http.Request, rollback bool) {
	var plan siteoperator.Plan
	if err := decodeJSON(w, r, &plan); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if !s.verifySitePlan(plan) {
		writeError(w, http.StatusUnprocessableEntity, "invalid_site_plan_signature", "The site plan was not issued by this agent.")
		return
	}
	var observation siteoperator.Observation
	var err error
	if rollback {
		observation, err = s.sites.Rollback(r.Context(), plan)
	} else {
		observation, err = s.sites.Apply(r.Context(), plan)
	}
	if err != nil {
		writeError(w, http.StatusConflict, "site_operation_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, observation)
}

func (s *Server) signSitePlan(plan siteoperator.Plan) string {
	plan.Signature = ""
	encoded, _ := json.Marshal(plan)
	mac := hmac.New(sha256.New, []byte(s.token))
	_, _ = mac.Write(encoded)
	return fmt.Sprintf("%x", mac.Sum(nil))
}

func (s *Server) verifySitePlan(plan siteoperator.Plan) bool {
	provided := plan.Signature
	expected := s.signSitePlan(plan)
	return len(provided) == len(expected) && subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) == 1
}
