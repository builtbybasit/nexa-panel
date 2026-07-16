package domains

import (
	"crypto/rand"

	"errors"

	"encoding/hex"
	"net/http"

	"context"
	"io"

	"encoding/json"

	"strings"
)

func (m *Module) ensurePrimaryDomains(ctx context.Context) error {
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

func domainID() string {
	value := make([]byte, 12)
	if _, err := rand.Read(value); err != nil {
		panic(err)
	}
	return "domain_" + hex.EncodeToString(value)
}

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

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 16*1024)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("one object required")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]string{"code": code, "message": message})
}
