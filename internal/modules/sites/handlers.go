package sites

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/nexa-panel/nexa-panel/internal/platform/httpapi"
	"github.com/nexa-panel/nexa-panel/internal/platform/identity"
)

func (m *Module) listHTTP(w http.ResponseWriter, r *http.Request) {
	items, err := m.List(r.Context())
	if err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, "sites_unavailable", "Sites could not be loaded.")
		return
	}
	if m.access != nil {
		user, ok := identity.UserFromContext(r.Context())
		if !ok {
			httpapi.WriteError(w, http.StatusUnauthorized, "authentication_required", "Sign in to continue.")
			return
		}
		all, ids, err := m.access.AccessibleSiteIDs(r.Context(), user)
		if err != nil {
			httpapi.WriteError(w, http.StatusInternalServerError, "sites_unavailable", "Sites could not be loaded.")
			return
		}
		if !all {
			granted := make(map[string]struct{}, len(ids))
			for _, id := range ids {
				granted[id] = struct{}{}
			}
			filtered := make([]Site, 0, len(items))
			for _, site := range items {
				if _, ok := granted[site.ID]; ok {
					filtered = append(filtered, site)
				}
			}
			items = filtered
		}
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (m *Module) createHTTP(w http.ResponseWriter, r *http.Request) {
	var request CreateRequest
	if err := httpapi.DecodeJSON(w, r, &request); err != nil {
		httpapi.WriteError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	user, ok := identity.UserFromContext(r.Context())
	if !ok {
		httpapi.WriteError(w, http.StatusUnauthorized, "authentication_required", "Sign in to continue.")
		return
	}
	site, job, err := m.Create(r.Context(), request, &user.ID)
	if err != nil {
		httpapi.WriteError(w, http.StatusUnprocessableEntity, "site_invalid", err.Error())
		return
	}
	w.Header().Set("Location", "/api/v1/sites/"+site.ID)
	httpapi.WriteJSON(w, http.StatusAccepted, map[string]any{"site": site, "job": job})
}

func (m *Module) getHTTP(w http.ResponseWriter, r *http.Request) {
	accessible, err := m.siteAccessible(r, r.PathValue("id"))
	if err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, "site_unavailable", "The site could not be loaded.")
		return
	}
	if !accessible {
		httpapi.WriteError(w, http.StatusNotFound, "site_not_found", "The requested site does not exist.")
		return
	}
	site, err := m.Get(r.Context(), r.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) {
		httpapi.WriteError(w, http.StatusNotFound, "site_not_found", "The requested site does not exist.")
		return
	}
	if err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, "site_unavailable", "The site could not be loaded.")
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, site)
}

func (m *Module) planHTTP(w http.ResponseWriter, r *http.Request) {
	accessible, err := m.siteAccessible(r, r.PathValue("id"))
	if err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, "site_plan_unavailable", "The site plan could not be loaded.")
		return
	}
	if !accessible {
		httpapi.WriteError(w, http.StatusNotFound, "site_plan_not_found", "A configuration plan is not ready for this site.")
		return
	}
	plan, expiresAt, err := m.Plan(r.Context(), r.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) {
		httpapi.WriteError(w, http.StatusNotFound, "site_plan_not_found", "A configuration plan is not ready for this site.")
		return
	}
	if err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, "site_plan_unavailable", "The site plan could not be loaded.")
		return
	}
	// sites.read reaches this endpoint, which includes read-only roles, so the
	// basic-auth credential is stripped from the response (never from storage).
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"plan": redactPlanForAPI(plan), "expiresAt": expiresAt})
}

// siteAccessible hides sites the user cannot reach; callers respond 404 so
// site existence does not leak to scoped roles.
func (m *Module) siteAccessible(r *http.Request, siteID string) (bool, error) {
	if m.access == nil {
		return false, errors.New("site access policy is unavailable")
	}
	user, ok := identity.UserFromContext(r.Context())
	if !ok {
		return false, nil
	}
	return m.access.SiteAccessible(r.Context(), user, siteID)
}

func (m *Module) replanHTTP(w http.ResponseWriter, r *http.Request) {
	site, err := m.Get(r.Context(), r.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) {
		httpapi.WriteError(w, http.StatusNotFound, "site_not_found", "The requested site does not exist.")
		return
	}
	if err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, "site_unavailable", "The site could not be loaded.")
		return
	}
	if site.Status == StatusActive || site.Status == StatusActivating {
		httpapi.WriteError(w, http.StatusConflict, "site_already_active", "Active site changes must use the owning runtime, domain, or certificate module.")
		return
	}
	user, ok := identity.UserFromContext(r.Context())
	if !ok {
		httpapi.WriteError(w, http.StatusUnauthorized, "authentication_required", "Sign in to continue.")
		return
	}
	job, err := m.jobs.SubmitTitled(r.Context(), "site.plan", "Plan site "+site.DisplayName, map[string]string{"siteId": site.ID}, &user.ID)
	if err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, "job_submission_failed", "Site planning could not be queued.")
		return
	}
	_, _ = m.database.NewUpdate().Model((*siteModel)(nil)).Set("status = ?", StatusPlanning).Set("last_job_id = ?", job.ID).Set("updated_at = ?", m.now().UTC()).Where("id = ?", site.ID).Exec(r.Context())
	httpapi.WriteJSON(w, http.StatusAccepted, job)
}

// updateSettingsHTTP persists a site's per-site Nginx/PHP-FPM settings and then
// re-renders the node to match. It is the one sanctioned self-service mutation of
// an active site (replanHTTP still refuses those) precisely because the settings
// job re-assembles the *full* definition — routes and TLS included — via
// Definition, so it can never strip a site's domains or certificate the way a
// bare re-plan would. The lifecycle depends on the site's current state:
//   - active     -> persist, enqueue site.settings, 202 + Job (one-shot apply)
//   - plan_ready -> persist, re-plan so the stored plan can't go stale, 202 + Job
//   - other settled states -> persist only, 200 + Site (next plan picks it up)
func (m *Module) updateSettingsHTTP(w http.ResponseWriter, r *http.Request) {
	site, err := m.Get(r.Context(), r.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) {
		httpapi.WriteError(w, http.StatusNotFound, "site_not_found", "The requested site does not exist.")
		return
	}
	if err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, "site_unavailable", "The site could not be loaded.")
		return
	}
	if !settingsEditable(site.Status) {
		httpapi.WriteError(w, http.StatusConflict, "site_busy", "The site is mid-operation; wait for the current job to finish before changing settings.")
		return
	}
	user, ok := identity.UserFromContext(r.Context())
	if !ok {
		httpapi.WriteError(w, http.StatusUnauthorized, "authentication_required", "Sign in to continue.")
		return
	}
	var request SettingsRequest
	if err := httpapi.DecodeJSON(w, r, &request); err != nil {
		httpapi.WriteError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	current, err := m.loadSettings(r.Context(), site.ID)
	if err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, "site_unavailable", "The current settings could not be loaded.")
		return
	}
	settings, err := normalizeSettings(current, request)
	if err != nil {
		httpapi.WriteError(w, http.StatusUnprocessableEntity, "settings_invalid", err.Error())
		return
	}
	encoded, err := json.Marshal(settings)
	if err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, "settings_invalid", "The settings could not be stored.")
		return
	}
	stored := string(encoded)
	if _, err := m.database.NewUpdate().Model((*siteModel)(nil)).Set("settings_json = ?", stored).Set("updated_at = ?", m.now().UTC()).Where("id = ?", site.ID).Exec(r.Context()); err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, "settings_update_failed", "The settings could not be saved.")
		return
	}

	switch site.Status {
	case StatusActive:
		m.enqueueSettingsFollowup(w, r, site, "site.settings", "Apply settings to "+site.PrimaryDomain, StatusActivating, &user.ID)
	case StatusPlanReady:
		m.enqueueSettingsFollowup(w, r, site, "site.plan", "Plan site "+site.DisplayName, StatusPlanning, &user.ID)
	default:
		refreshed, err := m.Get(r.Context(), site.ID)
		if err != nil {
			httpapi.WriteError(w, http.StatusInternalServerError, "site_unavailable", "The site could not be reloaded.")
			return
		}
		httpapi.WriteJSON(w, http.StatusOK, refreshed)
	}
}

// enqueueSettingsFollowup queues the job that re-renders the node after a settings
// change and attaches it to the site, keeping the persisted settings and the job
// atomic from the caller's perspective.
func (m *Module) enqueueSettingsFollowup(w http.ResponseWriter, r *http.Request, site Site, kind, title string, status Status, actorUserID *string) {
	job, err := m.submitSettingsFollowup(r.Context(), site, kind, title, status, actorUserID)
	if err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, "job_submission_failed", "The settings change could not be queued.")
		return
	}
	w.Header().Set("Location", fmt.Sprintf("/api/v1/jobs/%d", job.ID))
	httpapi.WriteJSON(w, http.StatusAccepted, *job)
}

func (m *Module) activateHTTP(w http.ResponseWriter, r *http.Request) {
	m.submitPlanMutation(w, r, "site.activate", StatusActivating)
}

func (m *Module) rollbackHTTP(w http.ResponseWriter, r *http.Request) {
	m.submitPlanMutation(w, r, "site.rollback", StatusRollingBack)
}
