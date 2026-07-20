package jobs

import (
	"context"
	"errors"

	"github.com/uptrace/bun"

	"github.com/nexa-panel/nexa-panel/internal/platform/identity"
)

type SiteAccessPolicy interface {
	AccessibleSiteIDs(ctx context.Context, user identity.User) (all bool, ids []string, err error)
}

func (m *Module) SetSiteAccessPolicy(policy SiteAccessPolicy) {
	m.siteAccess = policy
}

// ListForUser applies the identity module's site-grant contract. Developers
// can see their own submissions and system/other-user work only when every
// site in the job's persisted scope is granted to them. Other roles are not
// site-scoped.
func (m *Module) ListForUser(ctx context.Context, user identity.User, limit int) ([]Job, error) {
	if limit < 1 || limit > 200 {
		limit = 50
	}
	models := make([]jobModel, 0, limit)
	query := m.database.NewSelect().Model(&models)
	query, err := m.scopeJobsForUser(ctx, query, user)
	if err != nil {
		return nil, err
	}
	if err := query.OrderExpr("job.id DESC").Limit(limit).Scan(ctx); err != nil {
		return nil, err
	}
	items := make([]Job, 0, len(models))
	for index := range models {
		items = append(items, models[index].toJob())
	}
	return items, nil
}

func (m *Module) GetForUser(ctx context.Context, user identity.User, id int64) (Job, error) {
	model := new(jobModel)
	query := m.database.NewSelect().Model(model).Where("job.id = ?", id)
	query, err := m.scopeJobsForUser(ctx, query, user)
	if err != nil {
		return Job{}, err
	}
	if err := query.Scan(ctx); err != nil {
		return Job{}, err
	}
	return model.toJob(), nil
}

func (m *Module) EventsAfterForUser(ctx context.Context, user identity.User, jobID, sequence int64) ([]Event, error) {
	if _, err := m.GetForUser(ctx, user, jobID); err != nil {
		return nil, err
	}
	return m.EventsAfter(ctx, jobID, sequence)
}

func (m *Module) scopeJobsForUser(ctx context.Context, query *bun.SelectQuery, user identity.User) (*bun.SelectQuery, error) {
	if user.Role != "developer" {
		return query, nil
	}
	if m.siteAccess == nil {
		return nil, errors.New("job site access policy is unavailable")
	}
	all, siteIDs, err := m.siteAccess.AccessibleSiteIDs(ctx, user)
	if err != nil {
		return nil, err
	}
	if all {
		return query, nil
	}
	return query.WhereGroup(" AND ", func(scoped *bun.SelectQuery) *bun.SelectQuery {
		scoped = scoped.Where("job.actor_user_id = ?", user.ID)
		if len(siteIDs) != 0 {
			scoped = scoped.WhereOr(`job.scope_site_ids <> '[]' AND NOT EXISTS (
				SELECT 1 FROM json_each(job.scope_site_ids) AS scoped_site
				WHERE scoped_site.value NOT IN (?)
			)`, bun.List(siteIDs))
		}
		return scoped
	}), nil
}
