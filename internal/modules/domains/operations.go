package domains

import (
	"sort"

	"context"
	siteoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/sites"
	"github.com/uptrace/bun"
	"time"

	"encoding/json"
	"errors"
)

func (m *Module) planJob(ctx context.Context, raw json.RawMessage, report func(int, string) error) (any, error) {
	var request struct {
		DomainID string `json:"domainId"`
	}
	if json.Unmarshal(raw, &request) != nil || request.DomainID == "" {
		return nil, errors.New("invalid domain plan request")
	}
	if err := report(15, "Loading site routing state."); err != nil {
		return nil, err
	}
	domain, err := m.Get(ctx, request.DomainID)
	if err != nil {
		return nil, err
	}
	site, err := m.sites.Get(ctx, domain.SiteID)
	if err != nil {
		return nil, err
	}
	addresses, lookupErr := m.resolver.LookupHost(ctx, domain.Hostname)
	sort.Strings(addresses)
	warnings := []string{}
	if lookupErr != nil || len(addresses) == 0 {
		warnings = append(warnings, "The hostname does not currently resolve; configure DNS before requesting TLS.")
	}
	if err := report(40, "Rendering the complete routing configuration."); err != nil {
		return nil, err
	}
	routes, err := m.Routing(ctx, domain.SiteID, domain.ID)
	if err != nil {
		return nil, err
	}
	var tls *siteoperator.TLS
	var tlsDomains []string
	if m.tls != nil {
		tls, tlsDomains, err = m.tls.TLSForSite(ctx, domain.SiteID)
		if err != nil {
			return nil, err
		}
	}
	nodePlan, err := m.operator.Plan(ctx, siteoperator.Site{ID: site.ID, Slug: site.Slug, PrimaryDomain: site.PrimaryDomain, PHPVersion: site.PHPVersion, UnixUser: site.UnixUser, RootPath: site.RootPath, SocketPath: site.SocketPath, Routes: routes, TLS: tls, TLSDomains: tlsDomains})
	if err != nil {
		m.fail(context.WithoutCancel(ctx), domain.ID, err)
		return nil, err
	}
	plan := Plan{DomainID: domain.ID, NodePlan: nodePlan, ResolvedAddresses: addresses, Warnings: warnings}
	encoded, _ := json.Marshal(plan)
	now := m.now().UTC()
	row := &planModel{DomainID: domain.ID, PlanJSON: string(encoded), CreatedAt: now, ExpiresAt: nodePlan.ExpiresAt}
	err = m.database.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		if _, err := tx.NewInsert().Model(row).On("CONFLICT (domain_id) DO UPDATE").Set("plan_json = EXCLUDED.plan_json").Set("created_at = EXCLUDED.created_at").Set("expires_at = EXCLUDED.expires_at").Exec(ctx); err != nil {
			return err
		}
		addressesJSON, _ := json.Marshal(addresses)
		_, err := tx.NewUpdate().Model((*domainModel)(nil)).Set("status = ?", StatusPlanReady).Set("resolved_addresses = ?", string(addressesJSON)).Set("failure = NULL").Set("updated_at = ?", now).Where("id = ?", domain.ID).Exec(ctx)
		return err
	})
	if err != nil {
		m.fail(context.WithoutCancel(ctx), domain.ID, err)
		return nil, err
	}
	if err := report(95, "Domain routing plan is ready for approval."); err != nil {
		return nil, err
	}
	return map[string]any{"domainId": domain.ID, "expiresAt": row.ExpiresAt, "warnings": warnings}, nil
}

func (m *Module) activateJob(ctx context.Context, raw json.RawMessage, report func(int, string) error) (any, error) {
	var plan Plan
	if json.Unmarshal(raw, &plan) != nil || plan.DomainID == "" {
		return nil, errors.New("invalid persisted domain activation plan")
	}
	if err := report(25, "Checking signed routing plan and node drift."); err != nil {
		return nil, err
	}
	observation, err := m.operator.Apply(ctx, plan.NodePlan)
	if err != nil {
		m.fail(context.WithoutCancel(ctx), plan.DomainID, err)
		return nil, err
	}
	if err := report(90, "Nginx routing is validated and verified."); err != nil {
		return nil, err
	}
	now := m.now().UTC()
	_, err = m.database.NewUpdate().Model((*domainModel)(nil)).Set("status = ?", StatusActive).Set("failure = NULL").Set("updated_at = ?", now).Where("id = ?", plan.DomainID).Exec(ctx)
	return observation, err
}

func (m *Module) StoredPlan(ctx context.Context, id string) (Plan, time.Time, error) {
	row := new(planModel)
	if err := m.database.NewSelect().Model(row).Where("domain_id = ?", id).Scan(ctx); err != nil {
		return Plan{}, time.Time{}, err
	}
	var plan Plan
	if err := json.Unmarshal([]byte(row.PlanJSON), &plan); err != nil {
		return Plan{}, time.Time{}, err
	}
	return plan, row.ExpiresAt, nil
}
