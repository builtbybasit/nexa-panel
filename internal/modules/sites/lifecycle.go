package sites

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/platform/jobs"
	siteoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/sites"
)

func (m *Module) Create(ctx context.Context, request CreateRequest, actorUserID *string) (Site, jobs.Job, error) {
	request.Slug = strings.ToLower(strings.TrimSpace(request.Slug))
	request.DisplayName = strings.TrimSpace(request.DisplayName)
	request.PrimaryDomain = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(request.PrimaryDomain)), ".")
	request.PHPVersion = strings.TrimSpace(request.PHPVersion)
	if err := validateCreate(request); err != nil {
		return Site{}, jobs.Job{}, err
	}
	allowed, err := m.runtimes.Allowed(ctx, request.PHPVersion)
	if err != nil {
		return Site{}, jobs.Job{}, fmt.Errorf("discover allowed runtimes: %w", err)
	}
	if !allowed {
		return Site{}, jobs.Job{}, fmt.Errorf("PHP %s is not installed and enabled on this node", request.PHPVersion)
	}

	now := m.now().UTC()
	model := &siteModel{
		ID: randomID(), Slug: request.Slug, DisplayName: request.DisplayName,
		PrimaryDomain: request.PrimaryDomain, PHPVersion: request.PHPVersion,
		UnixUser:   "nexa_" + strings.ReplaceAll(request.Slug, "-", "_"),
		RootPath:   filepath.Join("/srv/nexa/sites", request.Slug),
		SocketPath: filepath.Join("/run/php", "nexa-"+request.Slug+".sock"),
		Status:     string(StatusPlanning), CreatedAt: now, UpdatedAt: now,
	}
	if _, err := m.database.NewInsert().Model(model).Exec(ctx); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return Site{}, jobs.Job{}, errors.New("the site slug or primary domain is already managed")
		}
		return Site{}, jobs.Job{}, fmt.Errorf("create site: %w", err)
	}
	job, err := m.jobs.SubmitTitled(ctx, "site.plan", "Plan site "+model.DisplayName, map[string]string{"siteId": model.ID}, actorUserID)
	if err != nil {
		failure := "The site configuration plan could not be queued."
		_, _ = m.database.NewUpdate().Model((*siteModel)(nil)).Set("status = ?", StatusFailed).
			Set("failure = ?", failure).Set("updated_at = ?", m.now().UTC()).Where("id = ?", model.ID).Exec(ctx)
		return model.toSite(), jobs.Job{}, err
	}
	model.LastJobID = &job.ID
	model.UpdatedAt = m.now().UTC()
	_, err = m.database.NewUpdate().Model(model).Column("last_job_id", "updated_at").WherePK().Exec(ctx)
	if err != nil {
		return Site{}, jobs.Job{}, fmt.Errorf("attach planning job: %w", err)
	}
	return model.toSite(), job, nil
}

func (m *Module) List(ctx context.Context) ([]Site, error) {
	models := make([]siteModel, 0)
	if err := m.database.NewSelect().Model(&models).OrderExpr("created_at DESC").Scan(ctx); err != nil {
		return nil, err
	}
	items := make([]Site, 0, len(models))
	for index := range models {
		items = append(items, models[index].toSite())
	}
	return items, nil
}

func (m *Module) Get(ctx context.Context, id string) (Site, error) {
	model := new(siteModel)
	if err := m.database.NewSelect().Model(model).Where("id = ?", id).Scan(ctx); err != nil {
		return Site{}, err
	}
	return model.toSite(), nil
}

func (m *Module) SiteExists(ctx context.Context, id string) (bool, error) {
	return m.database.NewSelect().Model((*siteModel)(nil)).Where("id = ?", id).Exists(ctx)
}

func (m *Module) Plan(ctx context.Context, siteID string) (siteoperator.Plan, time.Time, error) {
	model := new(planModel)
	if err := m.database.NewSelect().Model(model).Where("site_id = ?", siteID).Scan(ctx); err != nil {
		return siteoperator.Plan{}, time.Time{}, err
	}
	var plan siteoperator.Plan
	if err := json.Unmarshal([]byte(model.PlanJSON), &plan); err != nil {
		return siteoperator.Plan{}, time.Time{}, fmt.Errorf("decode persisted site plan: %w", err)
	}
	return plan, model.ExpiresAt, nil
}
