package certificates

import (
	"database/sql"
	"github.com/nexa-panel/nexa-panel/internal/platform/identity"

	"errors"

	"net/http"
)

func (m *Module) listHTTP(w http.ResponseWriter, r *http.Request) {
	items, err := m.List(r.Context(), r.URL.Query().Get("siteId"))
	if err != nil {
		writeError(w, 500, "certificates_unavailable", "Certificates could not be loaded.")
		return
	}
	writeJSON(w, 200, map[string]any{"items": items})
}

func (m *Module) createHTTP(w http.ResponseWriter, r *http.Request) {
	var request CreateRequest
	if decodeJSON(w, r, &request) != nil {
		writeError(w, 400, "invalid_request", "Request body must be valid JSON.")
		return
	}
	user, ok := identity.UserFromContext(r.Context())
	if !ok {
		writeError(w, 401, "authentication_required", "Sign in to continue.")
		return
	}
	certificate, job, err := m.Create(r.Context(), request, &user.ID)
	if err != nil {
		writeError(w, 422, "certificate_invalid", err.Error())
		return
	}
	writeJSON(w, 202, map[string]any{"certificate": certificate, "job": job})
}

func (m *Module) planHTTP(w http.ResponseWriter, r *http.Request) {
	plan, expires, err := m.StoredPlan(r.Context(), r.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, 404, "certificate_plan_not_found", "A certificate plan is not ready.")
		return
	}
	if err != nil {
		writeError(w, 500, "certificate_plan_unavailable", "The certificate plan could not be loaded.")
		return
	}
	writeJSON(w, 200, map[string]any{"plan": plan, "expiresAt": expires})
}

func (m *Module) prepareHTTP(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Operation string `json:"operation"`
	}
	if decodeJSON(w, r, &request) != nil || (request.Operation != "issue" && request.Operation != "renew" && request.Operation != "revoke") {
		writeError(w, 422, "certificate_operation_invalid", "Operation must be issue, renew, or revoke.")
		return
	}
	certificate, getErr := m.Get(r.Context(), r.PathValue("id"))
	if getErr != nil {
		writeError(w, 404, "certificate_not_found", "The certificate does not exist.")
		return
	}
	if (request.Operation == "renew" || request.Operation == "revoke") && certificate.Status != StatusActive {
		writeError(w, 409, "certificate_not_active", "Only an active certificate can be renewed or revoked.")
		return
	}
	if request.Operation == "issue" && certificate.Status != StatusRevoked && certificate.Status != StatusFailed {
		writeError(w, 409, "certificate_not_reissuable", "Only a revoked or failed certificate can be reissued.")
		return
	}
	user, ok := identity.UserFromContext(r.Context())
	if !ok {
		writeError(w, 401, "authentication_required", "Sign in to continue.")
		return
	}
	verb := "Issue"
	switch request.Operation {
	case "renew":
		verb = "Renew"
	case "revoke":
		verb = "Revoke"
	}
	job, err := m.jobs.SubmitTitled(r.Context(), "certificate.plan", verb+" certificate", map[string]string{"certificateId": r.PathValue("id"), "operation": request.Operation}, &user.ID)
	if err != nil {
		writeError(w, 500, "job_submission_failed", "Certificate planning could not be queued.")
		return
	}
	_, _ = m.database.NewUpdate().Model((*certificateModel)(nil)).Set("status = ?", StatusPlanning).Set("last_job_id = ?", job.ID).Set("updated_at = ?", m.now().UTC()).Where("id = ?", r.PathValue("id")).Exec(r.Context())
	writeJSON(w, 202, job)
}

func (m *Module) applyHTTP(w http.ResponseWriter, r *http.Request) {
	certificate, getErr := m.Get(r.Context(), r.PathValue("id"))
	if getErr != nil {
		writeError(w, 404, "certificate_not_found", "The certificate does not exist.")
		return
	}
	if certificate.Status != StatusPlanReady {
		writeError(w, 409, "certificate_not_ready", "Only a reviewed certificate plan can be applied.")
		return
	}
	plan, _, err := m.StoredPlan(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, 404, "certificate_plan_not_found", "A certificate plan is not ready.")
		return
	}
	user, ok := identity.UserFromContext(r.Context())
	if !ok {
		writeError(w, 401, "authentication_required", "Sign in to continue.")
		return
	}
	job, err := m.jobs.SubmitTitled(r.Context(), "certificate.execute", "Apply certificate change", plan, &user.ID)
	if err != nil {
		writeError(w, 500, "job_submission_failed", "Certificate operation could not be queued.")
		return
	}
	status := StatusIssuing
	if plan.Operation == "renew" {
		status = StatusRenewing
	} else if plan.Operation == "revoke" {
		status = StatusRevoking
	}
	_, _ = m.database.NewUpdate().Model((*certificateModel)(nil)).Set("status = ?", status).Set("last_job_id = ?", job.ID).Set("updated_at = ?", m.now().UTC()).Where("id = ?", r.PathValue("id")).Exec(r.Context())
	writeJSON(w, 202, job)
}
