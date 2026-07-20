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
	plan, err := m.operator.Plan(ctx, siteoperator.Site{
		ID: site.ID, Slug: site.Slug, PrimaryDomain: site.PrimaryDomain, PHPVersion: site.PHPVersion,
		UnixUser: site.UnixUser, RootPath: site.RootPath, SocketPath: site.SocketPath,
	})
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
