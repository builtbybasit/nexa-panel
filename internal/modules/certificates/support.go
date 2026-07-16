package certificates

import (
	"context"

	"net/http"

	"crypto/rand"
	siteoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/sites"

	"encoding/hex"
	"encoding/json"

	"io"

	"errors"
	"github.com/nexa-panel/nexa-panel/internal/modules/sites"

	"time"
)

func (m *Module) applyRouting(ctx context.Context, site sites.Site, routes []siteoperator.Route, tls *siteoperator.TLS, tlsDomains []string) error {
	plan, err := m.siteOperator.Plan(ctx, siteoperator.Site{ID: site.ID, Slug: site.Slug, PrimaryDomain: site.PrimaryDomain, PHPVersion: site.PHPVersion, UnixUser: site.UnixUser, RootPath: site.RootPath, SocketPath: site.SocketPath, Routes: routes, TLS: tls, TLSDomains: tlsDomains})
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

func certificateID() string {
	value := make([]byte, 12)
	if _, err := rand.Read(value); err != nil {
		panic(err)
	}
	return "certificate_" + hex.EncodeToString(value)
}

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
