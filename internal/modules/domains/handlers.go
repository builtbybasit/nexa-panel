package domains

import (
	"database/sql"
	"errors"
	"net/http"

	"github.com/nexa-panel/nexa-panel/internal/platform/httpapi"
	"github.com/nexa-panel/nexa-panel/internal/platform/identity"
)

func (m *Module) listHTTP(w http.ResponseWriter, r *http.Request) {
	items, err := m.List(r.Context(), r.URL.Query().Get("siteId"))
	if err != nil {
		httpapi.WriteError(w, 500, "domains_unavailable", "Domains could not be loaded.")
		return
	}
	httpapi.WriteJSON(w, 200, map[string]any{"items": items})
}

func (m *Module) createHTTP(w http.ResponseWriter, r *http.Request) {
	var request CreateRequest
	if httpapi.DecodeJSON(w, r, &request) != nil {
		httpapi.WriteError(w, 400, "invalid_request", "Request body must be valid JSON.")
		return
	}
	user, ok := identity.UserFromContext(r.Context())
	if !ok {
		httpapi.WriteError(w, 401, "authentication_required", "Sign in to continue.")
		return
	}
	domain, job, err := m.Create(r.Context(), request, &user.ID)
	if err != nil {
		httpapi.WriteError(w, 422, "domain_invalid", err.Error())
		return
	}
	w.Header().Set("Location", "/api/v1/domains/"+domain.ID)
	httpapi.WriteJSON(w, 202, map[string]any{"domain": domain, "job": job})
}

func (m *Module) planHTTP(w http.ResponseWriter, r *http.Request) {
	plan, expires, err := m.StoredPlan(r.Context(), r.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) {
		httpapi.WriteError(w, 404, "domain_plan_not_found", "A domain plan is not ready.")
		return
	}
	if err != nil {
		httpapi.WriteError(w, 500, "domain_plan_unavailable", "The domain plan could not be loaded.")
		return
	}
	httpapi.WriteJSON(w, 200, map[string]any{"plan": plan, "expiresAt": expires})
}

func (m *Module) activateHTTP(w http.ResponseWriter, r *http.Request) {
	domain, getErr := m.Get(r.Context(), r.PathValue("id"))
	if getErr != nil {
		httpapi.WriteError(w, 404, "domain_not_found", "The domain does not exist.")
		return
	}
	if domain.Status != StatusPlanReady {
		httpapi.WriteError(w, 409, "domain_not_ready", "Only a reviewed domain plan can be activated.")
		return
	}
	plan, _, err := m.StoredPlan(r.Context(), r.PathValue("id"))
	if err != nil {
		httpapi.WriteError(w, 404, "domain_plan_not_found", "A domain plan is not ready.")
		return
	}
	user, ok := identity.UserFromContext(r.Context())
	if !ok {
		httpapi.WriteError(w, 401, "authentication_required", "Sign in to continue.")
		return
	}
	job, err := m.jobs.SubmitTitled(r.Context(), "domain.activate", "Activate domain "+domain.Hostname, plan, &user.ID)
	if err != nil {
		httpapi.WriteError(w, 500, "job_submission_failed", "Domain activation could not be queued.")
		return
	}
	_, _ = m.database.NewUpdate().Model((*domainModel)(nil)).Set("status = ?", StatusActivating).Set("last_job_id = ?", job.ID).Set("updated_at = ?", m.now().UTC()).Where("id = ?", plan.DomainID).Exec(r.Context())
	httpapi.WriteJSON(w, 202, job)
}
