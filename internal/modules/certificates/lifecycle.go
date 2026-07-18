package certificates

import (
	"context"
	"errors"

	"strings"

	"encoding/json"
	siteoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/sites"

	"github.com/nexa-panel/nexa-panel/internal/platform/jobs"
	"net/mail"

	"database/sql"
	"github.com/nexa-panel/nexa-panel/internal/modules/sites"
)

func (m *Module) Create(ctx context.Context, request CreateRequest, actor *string) (Certificate, jobs.Job, error) {
	request.SiteID = strings.TrimSpace(request.SiteID)
	request.Email = strings.TrimSpace(request.Email)
	address, err := mail.ParseAddress(request.Email)
	if request.SiteID == "" || err != nil || address.Address != request.Email {
		return Certificate{}, jobs.Job{}, errors.New("site and valid ACME contact email are required")
	}
	site, err := m.sites.Get(ctx, request.SiteID)
	if err != nil {
		return Certificate{}, jobs.Job{}, errors.New("site does not exist")
	}
	if site.Status != sites.StatusActive {
		return Certificate{}, jobs.Job{}, errors.New("site must be active before requesting a certificate")
	}
	now := m.now().UTC()
	model := &certificateModel{ID: certificateID(), SiteID: site.ID, PrimaryDomain: site.PrimaryDomain, Email: request.Email, Status: string(StatusPlanning), DomainsJSON: "[]", CreatedAt: now, UpdatedAt: now}
	if _, err := m.database.NewInsert().Model(model).Exec(ctx); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return Certificate{}, jobs.Job{}, errors.New("site already has a certificate resource")
		}
		return Certificate{}, jobs.Job{}, err
	}
	job, err := m.jobs.SubmitTitled(ctx, "certificate.plan", "Issue certificate", map[string]string{"certificateId": model.ID, "operation": "issue"}, actor)
	if err != nil {
		m.fail(ctx, model.ID, err)
		return model.toCertificate(now), jobs.Job{}, err
	}
	model.LastJobID = &job.ID
	_, err = m.database.NewUpdate().Model(model).Column("last_job_id").WherePK().Exec(ctx)
	return model.toCertificate(now), job, err
}

func (m *Module) List(ctx context.Context, siteID string) ([]Certificate, error) {
	models := []certificateModel{}
	query := m.database.NewSelect().Model(&models).OrderExpr("created_at DESC")
	if siteID != "" {
		query = query.Where("site_id = ?", siteID)
	}
	if err := query.Scan(ctx); err != nil {
		return nil, err
	}
	now := m.now().UTC()
	items := make([]Certificate, 0, len(models))
	for _, model := range models {
		items = append(items, model.toCertificate(now))
	}
	return items, nil
}

func (m *Module) Get(ctx context.Context, id string) (Certificate, error) {
	model := new(certificateModel)
	if err := m.database.NewSelect().Model(model).Where("id = ?", id).Scan(ctx); err != nil {
		return Certificate{}, err
	}
	return model.toCertificate(m.now().UTC()), nil
}

func (m *Module) TLSForSite(ctx context.Context, siteID string) (*siteoperator.TLS, []string, error) {
	model := new(certificateModel)
	err := m.database.NewSelect().Model(model).Where("site_id = ?", siteID).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	if Status(model.Status) == StatusRevoked || model.CertificatePath == nil || model.PrivateKeyPath == nil {
		return nil, nil, nil
	}
	if *model.CertificatePath == "" || *model.PrivateKeyPath == "" {
		return nil, nil, errors.New("active certificate paths are incomplete")
	}
	names := []string{}
	_ = json.Unmarshal([]byte(model.DomainsJSON), &names)
	return &siteoperator.TLS{CertificatePath: *model.CertificatePath, PrivateKeyPath: *model.PrivateKeyPath}, names, nil
}
