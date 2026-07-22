package sites

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/nexa-panel/nexa-panel/internal/platform/identity"

	"github.com/uptrace/bun"
)

// deletePayload is the durable request for a site teardown. TeardownHost is
// captured at request time (only an active site has managed configuration on the
// node) so a retried job keeps deciding correctly after the row moves to the
// transient "deleting" status.
type deletePayload struct {
	SiteID       string `json:"siteId"`
	TeardownHost bool   `json:"teardownHost"`
}

// deleteHTTP begins a durable site teardown. It blocks (409) while the site is
// mid-operation, or while dependents that carry their own node state — extra
// domains or a live TLS certificate — are still attached, so the node is never
// left with orphaned Nginx or Let's Encrypt configuration.
func (m *Module) deleteHTTP(w http.ResponseWriter, r *http.Request) {
	site, err := m.Get(r.Context(), r.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "site_not_found", "The requested site does not exist.")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "site_unavailable", "The site could not be loaded.")
		return
	}
	if !siteDeletable(site.Status) {
		writeError(w, http.StatusConflict, "site_busy", "The site is mid-operation; wait for the current job to finish before deleting it.")
		return
	}
	if blocker, err := m.dependentBlocker(r.Context(), site.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "site_unavailable", "The site dependents could not be checked.")
		return
	} else if blocker != "" {
		writeError(w, http.StatusConflict, "site_has_dependents", blocker)
		return
	}
	user, ok := identity.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication_required", "Sign in to continue.")
		return
	}
	payload := deletePayload{SiteID: site.ID, TeardownHost: site.Status == StatusActive}
	job, err := m.jobs.SubmitTitled(r.Context(), "site.delete", "Delete site "+site.DisplayName, payload, &user.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "job_submission_failed", "Site removal could not be queued.")
		return
	}
	_, err = m.database.NewUpdate().Model((*siteModel)(nil)).Set("status = ?", StatusDeleting).Set("last_job_id = ?", job.ID).Set("failure = NULL").Set("updated_at = ?", m.now().UTC()).Where("id = ?", site.ID).Exec(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "site_update_failed", "The queued removal could not be attached to the site.")
		return
	}
	w.Header().Set("Location", fmt.Sprintf("/api/v1/jobs/%d", job.ID))
	writeJSON(w, http.StatusAccepted, job)
}

// siteDeletable rejects a teardown while the site is mid-operation, so a delete
// cannot interleave with an in-flight plan, activation, or rollback.
func siteDeletable(status Status) bool {
	switch status {
	case StatusActive, StatusDraft, StatusPlanReady, StatusRolledBack, StatusFailed:
		return true
	default:
		return false
	}
}

// dependentBlocker returns a descriptive message when the site still owns
// resources that must be removed through their own node-aware teardown first,
// or an empty string when the site is free to delete. Extra domains and live
// certificates carry Nginx server blocks and Let's Encrypt material that a bare
// site teardown would strand on the node.
func (m *Module) dependentBlocker(ctx context.Context, siteID string) (string, error) {
	hostnames := make([]string, 0)
	if err := m.database.NewSelect().TableExpr("domains").Column("hostname").
		Where("site_id = ?", siteID).Where("kind <> ?", "primary").
		OrderExpr("hostname ASC").Scan(ctx, &hostnames); err != nil {
		return "", err
	}
	certificates, err := m.database.NewSelect().TableExpr("certificates").
		Where("site_id = ?", siteID).Where("status NOT IN (?)", bun.List([]string{"revoked", "failed"})).Count(ctx)
	if err != nil {
		return "", err
	}
	blockers := make([]string, 0, 2)
	if len(hostnames) > 0 {
		blockers = append(blockers, fmt.Sprintf("remove its attached domains first (%s)", strings.Join(hostnames, ", ")))
	}
	if certificates > 0 {
		blockers = append(blockers, "remove its TLS certificate first")
	}
	if len(blockers) == 0 {
		return "", nil
	}
	return "The site cannot be deleted yet: " + strings.Join(blockers, "; ") + ".", nil
}

// deleteJob removes the managed Nginx and PHP-FPM configuration from the node and
// then deletes the site record. Its primary domain, stored plan, and any cascaded
// rows are removed by the foreign-key cascade once the row is gone.
func (m *Module) deleteJob(ctx context.Context, request json.RawMessage, report func(int, string) error) (any, error) {
	var payload deletePayload
	if err := json.Unmarshal(request, &payload); err != nil || payload.SiteID == "" {
		return nil, errors.New("invalid persisted site removal request")
	}
	if err := report(15, "Loading the site marked for removal."); err != nil {
		return nil, err
	}
	site, err := m.Get(ctx, payload.SiteID)
	if errors.Is(err, sql.ErrNoRows) {
		return map[string]any{"siteId": payload.SiteID, "removed": true, "alreadyGone": true}, nil
	}
	if err != nil {
		return nil, err
	}
	if payload.TeardownHost {
		if err := report(45, "Removing managed Nginx and PHP-FPM configuration from the node."); err != nil {
			return nil, err
		}
		if err := m.teardownHost(ctx, site); err != nil {
			m.markFailed(context.WithoutCancel(ctx), site.ID, err)
			return nil, err
		}
	}
	if err := report(85, "Removing the site record."); err != nil {
		return nil, err
	}
	if _, err := m.database.NewDelete().Model((*siteModel)(nil)).Where("id = ?", site.ID).Exec(ctx); err != nil {
		m.markFailed(context.WithoutCancel(ctx), site.ID, err)
		return nil, err
	}
	if err := report(95, "Site configuration and record removed."); err != nil {
		return nil, err
	}
	return map[string]any{"siteId": site.ID, "removed": true}, nil
}

// teardownHost drives the node operator to strip the managed configuration. It
// reuses the activation Rollback path with a synthetic plan whose "before" state
// is empty: rolling that plan back removes the rendered artifacts and disables
// the Nginx site atomically, refusing (rather than half-applying) if the managed
// configuration has drifted since it was activated.
func (m *Module) teardownHost(ctx context.Context, site Site) error {
	// Definition threads the persisted settings, so the teardown plan renders the
	// same conditional artifacts (htpasswd, rate-limit zone) the site activated
	// with — and the synthetic rollback below removes every one of them, not just
	// the three core files.
	definition, err := m.Definition(ctx, site.ID, nil, nil, nil)
	if err != nil {
		return fmt.Errorf("assemble site teardown definition: %w", err)
	}
	// The agent issues the synthetic plan and signs it. Building this shape here
	// by editing an activation plan is what used to break teardown: the signature
	// covers the whole plan, so the agent rejected the edited copy with "The site
	// plan was not issued by this agent." Send back exactly what was issued.
	plan, err := m.operator.PlanTeardown(ctx, definition)
	if err != nil {
		return fmt.Errorf("plan site teardown: %w", err)
	}
	if _, err := m.operator.Rollback(ctx, plan); err != nil {
		return fmt.Errorf("remove managed site configuration: %w", err)
	}
	return nil
}
