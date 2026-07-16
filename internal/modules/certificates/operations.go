package certificates

import (
	"errors"
	"fmt"

	"time"

	"context"
	"github.com/uptrace/bun"

	"sort"

	"github.com/nexa-panel/nexa-panel/internal/modules/domains"

	certificateoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/certificates"

	"encoding/json"
	siteoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/sites"
)

func (m *Module) planJob(ctx context.Context, raw json.RawMessage, report func(int, string) error) (any, error) {
	var request struct {
		CertificateID string `json:"certificateId"`
		Operation     string `json:"operation"`
	}
	if json.Unmarshal(raw, &request) != nil || request.CertificateID == "" {
		return nil, errors.New("invalid certificate plan request")
	}
	certificate, err := m.Get(ctx, request.CertificateID)
	if err != nil {
		return nil, err
	}
	if request.Operation != "issue" && request.Operation != "renew" && request.Operation != "revoke" {
		return nil, errors.New("invalid certificate operation")
	}
	if err := report(20, "Checking domain routing and public DNS."); err != nil {
		return nil, err
	}
	domainItems, err := m.domains.List(ctx, certificate.SiteID)
	if err != nil {
		return nil, err
	}
	names := []string{}
	dns := map[string][]string{}
	for _, domain := range domainItems {
		if domain.Kind == domains.KindRedirect || domain.Status != domains.StatusActive {
			continue
		}
		names = append(names, domain.Hostname)
		if request.Operation == "revoke" {
			continue
		}
		addresses, lookupErr := m.resolver.LookupHost(ctx, domain.Hostname)
		if lookupErr != nil || len(addresses) == 0 {
			err = fmt.Errorf("%s does not resolve; HTTP-01 preflight failed", domain.Hostname)
			m.fail(context.WithoutCancel(ctx), certificate.ID, err)
			return nil, err
		}
		sort.Strings(addresses)
		dns[domain.Hostname] = addresses
	}
	sort.Strings(names)
	if err := report(55, "Requesting an agent-signed Certbot operation plan."); err != nil {
		return nil, err
	}
	agentPlan, err := m.certificates.Plan(ctx, certificateoperator.Request{CertificateID: certificate.ID, Operation: request.Operation, PrimaryDomain: certificate.PrimaryDomain, Domains: names, Email: certificate.Email})
	if err != nil {
		m.fail(context.WithoutCancel(ctx), certificate.ID, err)
		return nil, err
	}
	stored := StoredPlan{Operation: request.Operation, AgentPlan: agentPlan, DNS: dns}
	encoded, _ := json.Marshal(stored)
	now := m.now().UTC()
	row := &planModel{CertificateID: certificate.ID, Operation: request.Operation, PlanJSON: string(encoded), CreatedAt: now, ExpiresAt: agentPlan.ExpiresAt}
	err = m.database.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		if _, err := tx.NewInsert().Model(row).On("CONFLICT (certificate_id) DO UPDATE").Set("operation = EXCLUDED.operation").Set("plan_json = EXCLUDED.plan_json").Set("created_at = EXCLUDED.created_at").Set("expires_at = EXCLUDED.expires_at").Exec(ctx); err != nil {
			return err
		}
		namesJSON, _ := json.Marshal(names)
		_, err := tx.NewUpdate().Model((*certificateModel)(nil)).Set("status = ?", StatusPlanReady).Set("domains_json = ?", string(namesJSON)).Set("failure = NULL").Set("updated_at = ?", now).Where("id = ?", certificate.ID).Exec(ctx)
		return err
	})
	if err != nil {
		return nil, err
	}
	if err := report(95, "Certificate plan is ready for approval."); err != nil {
		return nil, err
	}
	return map[string]any{"certificateId": certificate.ID, "operation": request.Operation, "expiresAt": agentPlan.ExpiresAt}, nil
}

func (m *Module) executeJob(ctx context.Context, raw json.RawMessage, report func(int, string) error) (any, error) {
	var stored StoredPlan
	if json.Unmarshal(raw, &stored) != nil || stored.AgentPlan.Request.CertificateID == "" {
		return nil, errors.New("invalid persisted certificate plan")
	}
	certificateID := stored.AgentPlan.Request.CertificateID
	certificate, err := m.Get(ctx, certificateID)
	if err != nil {
		return nil, err
	}
	site, err := m.sites.Get(ctx, certificate.SiteID)
	if err != nil {
		return nil, err
	}
	routes, err := m.domains.Routing(ctx, certificate.SiteID, "")
	if err != nil {
		return nil, err
	}
	if err := report(20, "Executing the signed HTTP-01 certificate operation."); err != nil {
		return nil, err
	}
	var observation certificateoperator.Observation
	if stored.Operation == "revoke" {
		if err := m.applyRouting(ctx, site, routes, nil, nil); err != nil {
			m.fail(context.WithoutCancel(ctx), certificateID, err)
			return nil, err
		}
		observation, err = m.certificates.Execute(ctx, stored.AgentPlan)
		if err != nil {
			previousTLS := &siteoperator.TLS{CertificatePath: stored.AgentPlan.CertificatePath, PrivateKeyPath: stored.AgentPlan.PrivateKeyPath}
			_ = m.applyRouting(context.WithoutCancel(ctx), site, routes, previousTLS, certificate.Domains)
		}
	} else {
		observation, err = m.certificates.Execute(ctx, stored.AgentPlan)
		if err == nil {
			tls := &siteoperator.TLS{CertificatePath: observation.CertificatePath, PrivateKeyPath: observation.PrivateKeyPath}
			err = m.applyRouting(ctx, site, routes, tls, observation.Domains)
		}
	}
	if err != nil {
		m.fail(context.WithoutCancel(ctx), certificateID, err)
		return nil, err
	}
	if err := report(90, "Certificate and Nginx TLS state are verified."); err != nil {
		return nil, err
	}
	now := m.now().UTC()
	status := StatusActive
	if stored.Operation == "revoke" {
		status = StatusRevoked
	}
	domainsJSON, _ := json.Marshal(observation.Domains)
	query := m.database.NewUpdate().Model((*certificateModel)(nil)).Set("status = ?", status).Set("domains_json = ?", string(domainsJSON)).Set("failure = NULL").Set("updated_at = ?", now).Where("id = ?", certificateID)
	if status == StatusActive {
		query = query.Set("certificate_path = ?", observation.CertificatePath).Set("private_key_path = ?", observation.PrivateKeyPath).Set("issued_at = ?", observation.IssuedAt).Set("expires_at = ?", observation.ExpiresAt)
	} else {
		query = query.Set("certificate_path = NULL").Set("private_key_path = NULL").Set("expires_at = NULL")
	}
	_, err = query.Exec(ctx)
	return observation, err
}

func (m *Module) StoredPlan(ctx context.Context, id string) (StoredPlan, time.Time, error) {
	row := new(planModel)
	if err := m.database.NewSelect().Model(row).Where("certificate_id = ?", id).Scan(ctx); err != nil {
		return StoredPlan{}, time.Time{}, err
	}
	var plan StoredPlan
	if err := json.Unmarshal([]byte(row.PlanJSON), &plan); err != nil {
		return StoredPlan{}, time.Time{}, err
	}
	return plan, row.ExpiresAt, nil
}
