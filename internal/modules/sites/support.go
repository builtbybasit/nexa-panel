package sites

import (
	"crypto/rand"

	"context"
	"net/http"

	"database/sql"
	"encoding/hex"

	"errors"

	"io"

	"encoding/json"
	"github.com/nexa-panel/nexa-panel/internal/platform/identity"

	"fmt"
)

func (m *Module) markFailed(ctx context.Context, siteID string, failure error) {
	message := failure.Error()
	if len(message) > 300 {
		message = message[:300]
	}
	_, _ = m.database.NewUpdate().Model((*siteModel)(nil)).Set("status = ?", StatusFailed).
		Set("failure = ?", message).Set("updated_at = ?", m.now().UTC()).Where("id = ?", siteID).Exec(ctx)
}

func (m *Module) submitPlanMutation(w http.ResponseWriter, r *http.Request, kind string, status Status) {
	site, siteErr := m.Get(r.Context(), r.PathValue("id"))
	if siteErr != nil {
		writeError(w, http.StatusNotFound, "site_not_found", "The requested site does not exist.")
		return
	}
	if kind == "site.activate" && site.Status != StatusPlanReady {
		writeError(w, http.StatusConflict, "site_not_ready", "Only a reviewed, current site plan can be activated.")
		return
	}
	if kind == "site.rollback" && site.Status != StatusActive {
		writeError(w, http.StatusConflict, "site_not_active", "Only an active site can be rolled back.")
		return
	}
	plan, _, err := m.Plan(r.Context(), r.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "site_plan_not_found", "A signed site plan is not ready.")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "site_plan_unavailable", "The site plan could not be loaded.")
		return
	}
	user, ok := identity.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication_required", "Sign in to continue.")
		return
	}
	job, err := m.jobs.Submit(r.Context(), kind, plan, &user.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "job_submission_failed", "The site operation could not be queued.")
		return
	}
	now := m.now().UTC()
	_, err = m.database.NewUpdate().Model((*siteModel)(nil)).Set("status = ?", status).Set("last_job_id = ?", job.ID).Set("updated_at = ?", now).Where("id = ?", plan.Site.ID).Exec(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "site_update_failed", "The queued operation could not be attached to the site.")
		return
	}
	w.Header().Set("Location", fmt.Sprintf("/api/v1/jobs/%d", job.ID))
	writeJSON(w, http.StatusAccepted, job)
}

func validateCreate(request CreateRequest) error {
	if !slugPattern.MatchString(request.Slug) {
		return errors.New("slug must start with a letter and contain 2-32 lowercase letters, numbers, or hyphens")
	}
	if request.DisplayName == "" || len(request.DisplayName) > 80 {
		return errors.New("display name must contain 1-80 characters")
	}
	if !domainPattern.MatchString(request.PrimaryDomain) || len(request.PrimaryDomain) > 253 {
		return errors.New("primary domain must be a valid fully-qualified ASCII hostname")
	}
	return nil
}

func (model siteModel) toSite() Site {
	failure := ""
	if model.Failure != nil {
		failure = *model.Failure
	}
	return Site{
		ID: model.ID, Slug: model.Slug, DisplayName: model.DisplayName, PrimaryDomain: model.PrimaryDomain,
		PHPVersion: model.PHPVersion, UnixUser: model.UnixUser, RootPath: model.RootPath, SocketPath: model.SocketPath,
		Status: Status(model.Status), LastJobID: model.LastJobID, Failure: failure,
		CreatedAt: model.CreatedAt, UpdatedAt: model.UpdatedAt,
	}
}

func randomID() string {
	value := make([]byte, 12)
	if _, err := rand.Read(value); err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	return "site_" + hex.EncodeToString(value)
}

func decodeJSON(w http.ResponseWriter, r *http.Request, destination any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 16*1024)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return errors.New("Request body must be valid JSON.")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("Request body must contain one JSON object.")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]string{"code": code, "message": message})
}
