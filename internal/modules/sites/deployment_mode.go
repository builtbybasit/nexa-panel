package sites

import (
	"context"
	"errors"
	"fmt"

	"github.com/nexa-panel/nexa-panel/internal/platform/jobs"
)

// SetDeploymentMode persists a site's deployment mode and starts the re-apply
// that makes the node match it.
//
// It is the exported half of the settings follow-up path: the deploy module owns
// the HTTP surface, the audit entry, and the actor, but only this module may
// touch the sites table or submit a site job, so the persist-and-re-apply step
// lives here. The returned job is nil when nothing had to be re-applied — a site
// that is not yet live has no vhost to re-render, and its next plan reads the
// stored column.
//
// The status gate is restated here rather than trusted from the caller: this is
// an exported mutation of a live document root, and a second caller must not be
// able to interleave it with an in-flight plan, activation, or deletion.
func (m *Module) SetDeploymentMode(ctx context.Context, siteID, mode string, actorUserID *string) (*jobs.Job, error) {
	if !validDeploymentMode(mode) {
		return nil, fmt.Errorf("unknown deployment mode %q", mode)
	}
	site, err := m.Get(ctx, siteID)
	if err != nil {
		return nil, err
	}
	if !settingsEditable(site.Status) {
		return nil, errors.New("the site is mid-operation; wait for the current job to finish before changing its deployment mode")
	}
	if _, err := m.database.NewUpdate().Model((*siteModel)(nil)).
		Set("deployment_mode = ?", mode).
		Set("updated_at = ?", m.now().UTC()).
		Where("id = ?", siteID).Exec(ctx); err != nil {
		return nil, fmt.Errorf("store deployment mode: %w", err)
	}

	switch site.Status {
	case StatusActive:
		return m.submitSettingsFollowup(ctx, site, "site.settings", "Apply settings to "+site.PrimaryDomain, StatusActivating, actorUserID)
	case StatusPlanReady:
		return m.submitSettingsFollowup(ctx, site, "site.plan", "Plan site "+site.DisplayName, StatusPlanning, actorUserID)
	default:
		return nil, nil
	}
}

// submitSettingsFollowup queues the job that re-renders the node after a stored
// change and attaches it to the site. It is the transport-free core that both
// the settings endpoint and SetDeploymentMode share, so the two paths cannot
// drift on which job kind or which status a re-apply uses.
func (m *Module) submitSettingsFollowup(ctx context.Context, site Site, kind, title string, status Status, actorUserID *string) (*jobs.Job, error) {
	job, err := m.jobs.SubmitTitled(ctx, kind, title, map[string]string{"siteId": site.ID}, actorUserID)
	if err != nil {
		return nil, fmt.Errorf("queue %s: %w", kind, err)
	}
	if _, err := m.database.NewUpdate().Model((*siteModel)(nil)).
		Set("status = ?", status).
		Set("last_job_id = ?", job.ID).
		Set("failure = NULL").
		Set("updated_at = ?", m.now().UTC()).
		Where("id = ?", site.ID).Exec(ctx); err != nil {
		return nil, fmt.Errorf("attach %s to the site: %w", kind, err)
	}
	return &job, nil
}
