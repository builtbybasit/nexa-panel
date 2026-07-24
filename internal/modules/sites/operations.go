package sites

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	siteoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/sites"
	"github.com/uptrace/bun"
)

func (m *Module) planJob(ctx context.Context, request json.RawMessage, report func(int, string) error) (any, error) {
	var payload struct {
		SiteID string `json:"siteId"`
	}
	if err := json.Unmarshal(request, &payload); err != nil || payload.SiteID == "" {
		return nil, errors.New("invalid persisted site planning request")
	}
	if err := report(20, "Loading the approved site definition."); err != nil {
		return nil, err
	}
	site, err := m.Get(ctx, payload.SiteID)
	if err != nil {
		return nil, err
	}
	if err := report(45, "Rendering confined Nginx and PHP-FPM configuration."); err != nil {
		return nil, err
	}
	definition, err := m.Definition(ctx, site.ID, nil, nil, nil)
	if err != nil {
		m.markFailed(context.WithoutCancel(ctx), site.ID, err)
		return nil, err
	}
	plan, err := m.operator.Plan(ctx, definition)
	if err != nil {
		m.markFailed(context.WithoutCancel(ctx), site.ID, err)
		return nil, err
	}
	encoded, err := json.Marshal(plan)
	if err != nil {
		m.markFailed(context.WithoutCancel(ctx), site.ID, err)
		return nil, err
	}
	if err := report(75, "Persisting the immutable activation plan."); err != nil {
		return nil, err
	}
	now := m.now().UTC()
	planRow := &planModel{SiteID: site.ID, PlanJSON: string(encoded), CreatedAt: now, ExpiresAt: now.Add(30 * time.Minute)}
	err = m.database.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		if _, err := tx.NewInsert().Model(planRow).On("CONFLICT (site_id) DO UPDATE").
			Set("plan_json = EXCLUDED.plan_json").Set("created_at = EXCLUDED.created_at").Set("expires_at = EXCLUDED.expires_at").Exec(ctx); err != nil {
			return err
		}
		_, err := tx.NewUpdate().Model((*siteModel)(nil)).Set("status = ?", StatusPlanReady).
			Set("failure = NULL").Set("updated_at = ?", now).Where("id = ?", site.ID).Exec(ctx)
		return err
	})
	if err != nil {
		m.markFailed(context.WithoutCancel(ctx), site.ID, err)
		return nil, err
	}
	if err := report(95, "Site configuration plan is ready for node validation."); err != nil {
		return nil, err
	}
	return map[string]any{"siteId": site.ID, "artifactCount": len(plan.Artifacts), "expiresAt": planRow.ExpiresAt}, nil
}

func (m *Module) activateJob(ctx context.Context, request json.RawMessage, report func(int, string) error) (any, error) {
	var plan siteoperator.Plan
	if err := json.Unmarshal(request, &plan); err != nil || plan.Site.ID == "" {
		return nil, errors.New("invalid persisted site activation plan")
	}
	if err := report(20, "Checking the signed site plan and observed node state."); err != nil {
		return nil, err
	}
	if err := report(40, "Staging site files and service configuration."); err != nil {
		return nil, err
	}
	observation, err := m.operator.Apply(ctx, plan)
	if err != nil {
		m.markFailed(context.WithoutCancel(ctx), plan.Site.ID, err)
		return nil, err
	}
	if err := report(90, "PHP-FPM, Nginx, and the virtual host are verified."); err != nil {
		return nil, err
	}
	now := m.now().UTC()
	_, err = m.database.NewUpdate().Model((*siteModel)(nil)).Set("status = ?", StatusActive).Set("failure = NULL").Set("updated_at = ?", now).Where("id = ?", plan.Site.ID).Exec(ctx)
	if err != nil {
		return nil, err
	}
	// The activation created the site's system account, so credentials staged
	// at creation can finally be applied. The site itself is live either way:
	// a staging failure is reported in the job log, never allowed to fail the
	// activation, and the staged hash survives for the next activation to retry.
	if m.sftp != nil {
		if applied, sftpErr := m.sftp.ProvisionPendingCredentials(ctx, plan.Site.ID); sftpErr != nil {
			if err := report(95, "SFTP credentials staged at creation could not be applied: "+sftpErr.Error()); err != nil {
				return nil, err
			}
		} else if applied {
			if err := report(95, "SFTP access enabled with the credentials chosen at creation."); err != nil {
				return nil, err
			}
		}
	}
	return observation, nil
}

// settingsJob re-renders and re-applies an active site after a settings change.
// Unlike site.plan (which sends the bare primary-domain definition), it must
// re-derive the site's extra routes and TLS so the re-render is the *complete*
// vhost, then apply it in one shot — the operator's Apply validates byte-exact,
// reloads, and auto-rolls-back on any failure, so a bad settings blob can never
// leave the live site broken. This is why settings edits are allowed on active
// sites while a bare replan is not.
func (m *Module) settingsJob(ctx context.Context, request json.RawMessage, report func(int, string) error) (any, error) {
	var payload struct {
		SiteID string `json:"siteId"`
	}
	if err := json.Unmarshal(request, &payload); err != nil || payload.SiteID == "" {
		return nil, errors.New("invalid persisted site settings request")
	}
	if err := report(20, "Loading site routing and certificate state."); err != nil {
		return nil, err
	}
	var (
		routes     []siteoperator.Route
		tls        *siteoperator.TLS
		tlsDomains []string
		err        error
	)
	if m.routeSource != nil {
		if routes, err = m.routeSource.Routing(ctx, payload.SiteID, ""); err != nil {
			m.markFailed(context.WithoutCancel(ctx), payload.SiteID, err)
			return nil, err
		}
	}
	if m.tls != nil {
		if tls, tlsDomains, err = m.tls.TLSForSite(ctx, payload.SiteID); err != nil {
			m.markFailed(context.WithoutCancel(ctx), payload.SiteID, err)
			return nil, err
		}
	}
	// The certificate's stored SAN list can outlive a removed domain, so clamp it
	// to the hostnames the site still serves before re-rendering.
	site, err := m.Get(ctx, payload.SiteID)
	if err != nil {
		m.markFailed(context.WithoutCancel(ctx), payload.SiteID, err)
		return nil, err
	}
	definition, err := m.Definition(ctx, payload.SiteID, routes, tls, clampTLSDomains(site.PrimaryDomain, routes, tlsDomains))
	if err != nil {
		m.markFailed(context.WithoutCancel(ctx), payload.SiteID, err)
		return nil, err
	}
	if err := report(45, "Rendering the updated Nginx and PHP-FPM configuration."); err != nil {
		return nil, err
	}
	plan, err := m.operator.Plan(ctx, definition)
	if err != nil {
		m.markFailed(context.WithoutCancel(ctx), payload.SiteID, err)
		return nil, err
	}
	if err := report(70, "Applying and verifying the updated configuration."); err != nil {
		return nil, err
	}
	observation, err := m.operator.Apply(ctx, plan)
	if err != nil {
		m.markFailed(context.WithoutCancel(ctx), payload.SiteID, err)
		return nil, err
	}
	now := m.now().UTC()
	if _, err := m.database.NewUpdate().Model((*siteModel)(nil)).Set("status = ?", StatusActive).Set("failure = NULL").Set("updated_at = ?", now).Where("id = ?", payload.SiteID).Exec(ctx); err != nil {
		return nil, err
	}
	return observation, nil
}

func (m *Module) rollbackJob(ctx context.Context, request json.RawMessage, report func(int, string) error) (any, error) {
	var plan siteoperator.Plan
	if err := json.Unmarshal(request, &plan); err != nil || plan.Site.ID == "" {
		return nil, errors.New("invalid persisted site rollback plan")
	}
	if err := report(30, "Checking rollback drift and captured state."); err != nil {
		return nil, err
	}
	observation, err := m.operator.Rollback(ctx, plan)
	if err != nil {
		m.markFailed(context.WithoutCancel(ctx), plan.Site.ID, err)
		return nil, err
	}
	if err := report(90, "Previous configuration restored and services reloaded."); err != nil {
		return nil, err
	}
	now := m.now().UTC()
	_, err = m.database.NewUpdate().Model((*siteModel)(nil)).Set("status = ?", StatusRolledBack).Set("failure = NULL").Set("updated_at = ?", now).Where("id = ?", plan.Site.ID).Exec(ctx)
	if err != nil {
		return nil, err
	}
	return observation, nil
}
