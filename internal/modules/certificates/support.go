package certificates

import (
	"context"
	"encoding/json"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/modules/sites"
	siteoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/sites"
	"github.com/nexa-panel/nexa-panel/internal/platform/secureid"
)

func (m *Module) applyRouting(ctx context.Context, site sites.Site, routes []siteoperator.Route, tls *siteoperator.TLS, tlsDomains []string) error {
	definition, err := m.sites.Definition(ctx, site.ID, routes, tls, tlsDomains)
	if err != nil {
		return err
	}
	plan, err := m.siteOperator.Plan(ctx, definition)
	if err != nil {
		return err
	}
	_, err = m.siteOperator.Apply(ctx, plan)
	return err
}

func (m *Module) fail(ctx context.Context, id string, failure error) {
	message := failure.Error()
	if len(message) > 300 {
		message = message[:300]
	}
	_, _ = m.database.NewUpdate().Model((*certificateModel)(nil)).
		Set("status = CASE WHEN certificate_path IS NOT NULL AND private_key_path IS NOT NULL THEN ? ELSE ? END", StatusActive, StatusFailed).
		Set("failure = ?", message).
		Set("updated_at = ?", m.now().UTC()).
		Where("id = ?", id).
		Exec(ctx)
}

func certificateID() string { return "certificate_" + secureid.Hex(12) }

func (m certificateModel) toCertificate(now time.Time) Certificate {
	names := []string{}
	_ = json.Unmarshal([]byte(m.DomainsJSON), &names)
	path, failure := "", ""
	if m.CertificatePath != nil {
		path = *m.CertificatePath
	}
	if m.Failure != nil {
		failure = *m.Failure
	}
	return Certificate{ID: m.ID, SiteID: m.SiteID, PrimaryDomain: m.PrimaryDomain, Email: m.Email, Status: Status(m.Status), Domains: names, CertificatePath: path, IssuedAt: m.IssuedAt, ExpiresAt: m.ExpiresAt, ExpiringSoon: m.ExpiresAt != nil && m.ExpiresAt.Before(now.Add(30*24*time.Hour)), LastJobID: m.LastJobID, Failure: failure, CreatedAt: m.CreatedAt, UpdatedAt: m.UpdatedAt}
}
