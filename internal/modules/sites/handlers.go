package sites

import (
	"database/sql"

	"errors"

	"github.com/nexa-panel/nexa-panel/internal/platform/identity"
	"net/http"
)

func (m *Module) listHTTP(w http.ResponseWriter, r *http.Request) {
	items, err := m.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "sites_unavailable", "Sites could not be loaded.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (m *Module) createHTTP(w http.ResponseWriter, r *http.Request) {
	var request CreateRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	user, ok := identity.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication_required", "Sign in to continue.")
		return
	}
	site, job, err := m.Create(r.Context(), request, &user.ID)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "site_invalid", err.Error())
		return
	}
	w.Header().Set("Location", "/api/v1/sites/"+site.ID)
	writeJSON(w, http.StatusAccepted, map[string]any{"site": site, "job": job})
}

func (m *Module) getHTTP(w http.ResponseWriter, r *http.Request) {
	site, err := m.Get(r.Context(), r.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "site_not_found", "The requested site does not exist.")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "site_unavailable", "The site could not be loaded.")
		return
	}
	writeJSON(w, http.StatusOK, site)
}

func (m *Module) planHTTP(w http.ResponseWriter, r *http.Request) {
	plan, expiresAt, err := m.Plan(r.Context(), r.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "site_plan_not_found", "A configuration plan is not ready for this site.")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "site_plan_unavailable", "The site plan could not be loaded.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"plan": plan, "expiresAt": expiresAt})
}

func (m *Module) replanHTTP(w http.ResponseWriter, r *http.Request) {
	site, err := m.Get(r.Context(), r.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "site_not_found", "The requested site does not exist.")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "site_unavailable", "The site could not be loaded.")
		return
	}
	if site.Status == StatusActive || site.Status == StatusActivating {
		writeError(w, http.StatusConflict, "site_already_active", "Active site changes must use the owning runtime, domain, or certificate module.")
		return
	}
	user, ok := identity.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication_required", "Sign in to continue.")
		return
	}
	job, err := m.jobs.Submit(r.Context(), "site.plan", map[string]string{"siteId": site.ID}, &user.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "job_submission_failed", "Site planning could not be queued.")
		return
	}
	_, _ = m.database.NewUpdate().Model((*siteModel)(nil)).Set("status = ?", StatusPlanning).Set("last_job_id = ?", job.ID).Set("updated_at = ?", m.now().UTC()).Where("id = ?", site.ID).Exec(r.Context())
	writeJSON(w, http.StatusAccepted, job)
}

func (m *Module) activateHTTP(w http.ResponseWriter, r *http.Request) {
	m.submitPlanMutation(w, r, "site.activate", StatusActivating)
}

func (m *Module) rollbackHTTP(w http.ResponseWriter, r *http.Request) {
	m.submitPlanMutation(w, r, "site.rollback", StatusRollingBack)
}
