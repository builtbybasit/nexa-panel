package domains

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/nexa-panel/nexa-panel/internal/platform/secureid"
)

// ensurePrimaryDomains backfills the implicit primary-domain row every site
// owns. It runs on the read path, so it asks first: the INSERT is skipped
// unless a site is actually missing its row. Without the probe every domain
// listing took the database's write lock to insert nothing.
func (m *Module) ensurePrimaryDomains(ctx context.Context) error {
	missing := 0
	if err := m.database.QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM sites WHERE NOT EXISTS (
			SELECT 1 FROM domains WHERE domains.id = 'domain_primary_' || sites.id))`).Scan(&missing); err != nil {
		return err
	}
	if missing == 0 {
		return nil
	}
	now := m.now().UTC()
	_, err := m.database.ExecContext(ctx, `INSERT OR IGNORE INTO domains
		(id, site_id, hostname, kind, status, resolved_addresses, created_at, updated_at)
		SELECT 'domain_primary_' || id, id, primary_domain, 'primary', 'active', '[]', ?, ? FROM sites`, now, now)
	return err
}

func (m *Module) fail(ctx context.Context, id string, failure error) {
	message := failure.Error()
	if len(message) > 300 {
		message = message[:300]
	}
	_, _ = m.database.NewUpdate().Model((*domainModel)(nil)).Set("status = ?", StatusFailed).Set("failure = ?", message).Set("updated_at = ?", m.now().UTC()).Where("id = ?", id).Exec(ctx)
}

func normalize(value string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(value)), ".")
}

func domainID() string { return "domain_" + secureid.Hex(12) }

func (m domainModel) toDomain() Domain {
	addresses := []string{}
	_ = json.Unmarshal([]byte(m.ResolvedAddresses), &addresses)
	target, failure := "", ""
	if m.RedirectTarget != nil {
		target = *m.RedirectTarget
	}
	if m.Failure != nil {
		failure = *m.Failure
	}
	return Domain{ID: m.ID, SiteID: m.SiteID, Hostname: m.Hostname, Kind: Kind(m.Kind), RedirectTarget: target, Status: Status(m.Status), ResolvedAddresses: addresses, LastJobID: m.LastJobID, Failure: failure, CreatedAt: m.CreatedAt, UpdatedAt: m.UpdatedAt}
}
