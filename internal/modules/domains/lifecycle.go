package domains

import (
	"context"
	"errors"
	"strings"

	"github.com/nexa-panel/nexa-panel/internal/modules/sites"
	"github.com/nexa-panel/nexa-panel/internal/platform/jobs"
	"github.com/nexa-panel/nexa-panel/internal/platform/naming"
	siteoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/sites"
)

func (m *Module) Create(ctx context.Context, request CreateRequest, actor *string) (Domain, jobs.Job, error) {
	request.SiteID = strings.TrimSpace(request.SiteID)
	request.Hostname = normalize(request.Hostname)
	request.RedirectTarget = normalize(request.RedirectTarget)
	if request.SiteID == "" || !naming.ValidHostname(request.Hostname) {
		return Domain{}, jobs.Job{}, errors.New("site and a valid ASCII hostname are required")
	}
	if request.Kind != KindSubdomain && request.Kind != KindAlias && request.Kind != KindRedirect {
		return Domain{}, jobs.Job{}, errors.New("domain kind must be subdomain, alias, or redirect")
	}
	if request.Kind == KindRedirect && !naming.ValidHostname(request.RedirectTarget) {
		return Domain{}, jobs.Job{}, errors.New("redirect target must be a valid hostname")
	}
	if request.Kind != KindRedirect && request.RedirectTarget != "" {
		return Domain{}, jobs.Job{}, errors.New("only redirects may include a target")
	}
	site, err := m.sites.Get(ctx, request.SiteID)
	if err != nil {
		return Domain{}, jobs.Job{}, errors.New("site does not exist")
	}
	if site.Status != sites.StatusActive {
		return Domain{}, jobs.Job{}, errors.New("site must be active before attaching another domain")
	}
	now := m.now().UTC()
	model := &domainModel{ID: domainID(), SiteID: request.SiteID, Hostname: request.Hostname, Kind: string(request.Kind), Status: string(StatusPlanning), ResolvedAddresses: "[]", CreatedAt: now, UpdatedAt: now}
	if request.RedirectTarget != "" {
		model.RedirectTarget = &request.RedirectTarget
	}
	if _, err := m.database.NewInsert().Model(model).Exec(ctx); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return Domain{}, jobs.Job{}, errors.New("hostname is already managed")
		}
		return Domain{}, jobs.Job{}, err
	}
	job, err := m.jobs.SubmitTitled(ctx, "domain.plan", "Plan domain "+model.Hostname, map[string]string{"domainId": model.ID}, actor)
	if err != nil {
		m.fail(ctx, model.ID, err)
		return model.toDomain(), jobs.Job{}, err
	}
	model.LastJobID = &job.ID
	model.UpdatedAt = m.now().UTC()
	_, err = m.database.NewUpdate().Model(model).Column("last_job_id", "updated_at").WherePK().Exec(ctx)
	return model.toDomain(), job, err
}

func (m *Module) List(ctx context.Context, siteID string) ([]Domain, error) {
	if err := m.ensurePrimaryDomains(ctx); err != nil {
		return nil, err
	}
	models := make([]domainModel, 0)
	query := m.database.NewSelect().Model(&models).OrderExpr("kind ASC, hostname ASC")
	if siteID != "" {
		query = query.Where("site_id = ?", siteID)
	}
	if err := query.Scan(ctx); err != nil {
		return nil, err
	}
	items := make([]Domain, 0, len(models))
	for _, model := range models {
		items = append(items, model.toDomain())
	}
	return items, nil
}

func (m *Module) Get(ctx context.Context, id string) (Domain, error) {
	model := new(domainModel)
	if err := m.database.NewSelect().Model(model).Where("id = ?", id).Scan(ctx); err != nil {
		return Domain{}, err
	}
	return model.toDomain(), nil
}

func (m *Module) Routing(ctx context.Context, siteID, includeID string) ([]siteoperator.Route, error) {
	items, err := m.List(ctx, siteID)
	if err != nil {
		return nil, err
	}
	routes := make([]siteoperator.Route, 0)
	for _, item := range items {
		if item.Kind == KindPrimary || (item.Status != StatusActive && item.ID != includeID) {
			continue
		}
		routes = append(routes, siteoperator.Route{Hostname: item.Hostname, Kind: string(item.Kind), RedirectTarget: item.RedirectTarget})
	}
	return routes, nil
}
