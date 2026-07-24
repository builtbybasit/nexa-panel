package domains

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/nexa-panel/nexa-panel/internal/modules/sites"
	"github.com/nexa-panel/nexa-panel/internal/platform/httpapi"
	"github.com/nexa-panel/nexa-panel/internal/platform/identity"
	siteoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/sites"
)

// deleteHTTP begins a durable domain removal. The primary domain is intrinsic to
// its site and can only be removed by deleting the site, so it is refused (409),
// as is a domain that is mid-operation.
func (m *Module) deleteHTTP(w http.ResponseWriter, r *http.Request) {
	domain, err := m.Get(r.Context(), r.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) {
		httpapi.WriteError(w, http.StatusNotFound, "domain_not_found", "The domain does not exist.")
		return
	}
	if err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, "domain_unavailable", "The domain could not be loaded.")
		return
	}
	if domain.Kind == KindPrimary {
		httpapi.WriteError(w, http.StatusConflict, "domain_primary_protected", "The primary domain is removed by deleting its site, not on its own.")
		return
	}
	if !domainRemovable(domain.Status) {
		httpapi.WriteError(w, http.StatusConflict, "domain_busy", "The domain is mid-operation; wait for the current job to finish before removing it.")
		return
	}
	user, ok := identity.UserFromContext(r.Context())
	if !ok {
		httpapi.WriteError(w, http.StatusUnauthorized, "authentication_required", "Sign in to continue.")
		return
	}
	payload := removePayload{DomainID: domain.ID}
	job, err := m.jobs.SubmitTitled(r.Context(), "domain.remove", "Remove domain "+domain.Hostname, payload, &user.ID)
	if err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, "job_submission_failed", "Domain removal could not be queued.")
		return
	}
	_, _ = m.database.NewUpdate().Model((*domainModel)(nil)).Set("status = ?", StatusRemoving).Set("last_job_id = ?", job.ID).Set("failure = NULL").Set("updated_at = ?", m.now().UTC()).Where("id = ?", domain.ID).Exec(r.Context())
	w.Header().Set("Location", fmt.Sprintf("/api/v1/jobs/%d", job.ID))
	httpapi.WriteJSON(w, http.StatusAccepted, job)
}

// domainRemovable rejects a removal while the domain is mid-operation, so a
// delete cannot interleave with an in-flight plan or activation.
func domainRemovable(status Status) bool {
	switch status {
	case StatusActive, StatusPlanReady, StatusFailed:
		return true
	default:
		return false
	}
}

type removePayload struct {
	DomainID string `json:"domainId"`
}

// removeJob strips a domain's routing from the site's live Nginx configuration
// and re-activates the remaining routes, then deletes the domain record. When the
// domain was never part of the live configuration (its site is not active, or the
// domain itself was never activated) the node is left untouched and only the
// record is removed.
func (m *Module) removeJob(ctx context.Context, raw json.RawMessage, report func(int, string) error) (any, error) {
	var payload removePayload
	if json.Unmarshal(raw, &payload) != nil || payload.DomainID == "" {
		return nil, errors.New("invalid persisted domain removal request")
	}
	if err := report(15, "Loading site routing state."); err != nil {
		return nil, err
	}
	domain, err := m.Get(ctx, payload.DomainID)
	if errors.Is(err, sql.ErrNoRows) {
		return map[string]any{"domainId": payload.DomainID, "removed": true, "alreadyGone": true}, nil
	}
	if err != nil {
		return nil, err
	}
	if domain.Kind == KindPrimary {
		return nil, errors.New("the primary domain cannot be removed on its own")
	}
	site, err := m.sites.Get(ctx, domain.SiteID)
	if err != nil {
		return nil, err
	}
	if domain.Status == StatusActive && site.Status == sites.StatusActive {
		if err := report(45, "Re-rendering routing without the removed domain."); err != nil {
			return nil, err
		}
		if err := m.reactivateWithout(ctx, site, domain.ID); err != nil {
			m.fail(context.WithoutCancel(ctx), domain.ID, err)
			return nil, err
		}
	}
	if err := report(85, "Removing the domain record."); err != nil {
		return nil, err
	}
	if _, err := m.database.NewDelete().Model((*domainModel)(nil)).Where("id = ?", domain.ID).Exec(ctx); err != nil {
		m.fail(context.WithoutCancel(ctx), domain.ID, err)
		return nil, err
	}
	if err := report(95, "Domain routing and record removed."); err != nil {
		return nil, err
	}
	return map[string]any{"domainId": domain.ID, "removed": true}, nil
}

// reactivateWithout plans and applies the site's Nginx configuration with every
// active route except the one being removed, so its server_name disappears from
// the live host. The operator's apply path validates and reloads Nginx, rolling
// itself back on failure rather than leaving a broken configuration.
func (m *Module) reactivateWithout(ctx context.Context, site sites.Site, excludeID string) error {
	routes, err := m.routesExcluding(ctx, site.ID, excludeID)
	if err != nil {
		return err
	}
	if m.tls == nil {
		return errors.New("TLS provider is unavailable")
	}
	tls, tlsDomains, err := m.tls.TLSForSite(ctx, site.ID)
	if err != nil {
		return err
	}
	tlsDomains = clampTLSDomains(site.PrimaryDomain, routes, tlsDomains)
	definition, err := m.sites.Definition(ctx, site.ID, routes, tls, tlsDomains)
	if err != nil {
		return fmt.Errorf("assemble routing without removed domain: %w", err)
	}
	nodePlan, err := m.operator.Plan(ctx, definition)
	if err != nil {
		return fmt.Errorf("plan routing without removed domain: %w", err)
	}
	if _, err := m.operator.Apply(ctx, nodePlan); err != nil {
		return fmt.Errorf("apply routing without removed domain: %w", err)
	}
	return nil
}

// routesExcluding returns the active, non-primary routes for a site with the
// removed domain left out, mirroring Routing but dropping one id.
func (m *Module) routesExcluding(ctx context.Context, siteID, excludeID string) ([]siteoperator.Route, error) {
	items, err := m.List(ctx, siteID)
	if err != nil {
		return nil, err
	}
	routes := make([]siteoperator.Route, 0)
	for _, item := range items {
		if item.Kind == KindPrimary || item.ID == excludeID || item.Status != StatusActive {
			continue
		}
		routes = append(routes, siteoperator.Route{Hostname: item.Hostname, Kind: string(item.Kind), RedirectTarget: item.RedirectTarget})
	}
	return routes, nil
}

// clampTLSDomains keeps only the certificate names that still have a serving
// hostname after removal, so the re-rendered plan never references a name that is
// no longer attached to the site (which the node operator would reject).
func clampTLSDomains(primary string, routes []siteoperator.Route, tlsDomains []string) []string {
	allowed := map[string]struct{}{primary: {}}
	for _, route := range routes {
		if route.Kind != string(KindRedirect) {
			allowed[route.Hostname] = struct{}{}
		}
	}
	clamped := make([]string, 0, len(tlsDomains))
	for _, hostname := range tlsDomains {
		if _, ok := allowed[hostname]; ok {
			clamped = append(clamped, hostname)
		}
	}
	return clamped
}
