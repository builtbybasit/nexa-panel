package agent

import (
	"net/http"

	certificateoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/certificates"
)

func WithCertificateOperator(operator certificateoperator.Operator) Option {
	return func(server *Server) { server.certificates = operator }
}

func (s *Server) certificatePlanHTTP(w http.ResponseWriter, r *http.Request) {
	var request certificateoperator.Request
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, 400, "invalid_request", err.Error())
		return
	}
	plan, err := s.certificates.Plan(r.Context(), request)
	if err != nil {
		writeError(w, 422, "certificate_plan_failed", err.Error())
		return
	}
	plan.Signature = s.signCertificatePlan(plan)
	writeJSON(w, 200, plan)
}

func (s *Server) certificateExecuteHTTP(w http.ResponseWriter, r *http.Request) {
	var plan certificateoperator.Plan
	if err := decodeJSON(w, r, &plan); err != nil {
		writeError(w, 400, "invalid_request", err.Error())
		return
	}
	if !s.verifyCertificatePlan(plan) {
		writeError(w, 422, "invalid_certificate_plan_signature", "The certificate plan was not issued by this agent.")
		return
	}
	result, err := s.certificates.Execute(r.Context(), plan)
	if err != nil {
		writeError(w, 409, "certificate_operation_failed", err.Error())
		return
	}
	writeJSON(w, 200, result)
}

func (s *Server) signCertificatePlan(plan certificateoperator.Plan) string {
	plan.Signature = ""
	return signPayload(s.token, "certificate.plan.v1", plan)
}

func (s *Server) verifyCertificatePlan(plan certificateoperator.Plan) bool {
	provided := plan.Signature
	plan.Signature = ""
	return verifyPayload(s.token, "certificate.plan.v1", plan, provided)
}
