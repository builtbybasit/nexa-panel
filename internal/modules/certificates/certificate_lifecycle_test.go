package certificates

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/modules/domains"
	"github.com/nexa-panel/nexa-panel/internal/modules/sites"
	"github.com/nexa-panel/nexa-panel/internal/platform/audit"
	"github.com/nexa-panel/nexa-panel/internal/platform/jobs"
	certificateoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/certificates"
	siteoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/sites"
	"github.com/nexa-panel/nexa-panel/internal/platform/persistence"
)

type fakeSites struct{ site sites.Site }

func (f fakeSites) Get(context.Context, string) (sites.Site, error) { return f.site, nil }

type fakeDomains struct{}

func (fakeDomains) List(context.Context, string) ([]domains.Domain, error) {
	return []domains.Domain{{ID: "primary", SiteID: "site-1", Hostname: "demo.example.com", Kind: domains.KindPrimary, Status: domains.StatusActive}, {ID: "alias", SiteID: "site-1", Hostname: "www.demo.example.com", Kind: domains.KindAlias, Status: domains.StatusActive}}, nil
}

func (fakeDomains) Routing(context.Context, string, string) ([]siteoperator.Route, error) {
	return []siteoperator.Route{{Hostname: "www.demo.example.com", Kind: "alias"}}, nil
}

type fakeResolver struct{}

func (fakeResolver) LookupHost(context.Context, string) ([]string, error) {
	return []string{"203.0.113.10"}, nil
}

type fakeCertificateOperator struct{}

func (fakeCertificateOperator) Plan(_ context.Context, request certificateoperator.Request) (certificateoperator.Plan, error) {
	now := time.Now()
	return certificateoperator.Plan{ID: "plan", Kind: certificateoperator.PlanKind, Request: request, CertificatePath: "/etc/letsencrypt/live/demo.example.com/fullchain.pem", PrivateKeyPath: "/etc/letsencrypt/live/demo.example.com/privkey.pem", PlannedAt: now, ExpiresAt: now.Add(time.Hour), Signature: "signed"}, nil
}

type failingCertificateOperator struct{ fakeCertificateOperator }

func (failingCertificateOperator) Execute(context.Context, certificateoperator.Plan) (certificateoperator.Observation, error) {
	return certificateoperator.Observation{}, errors.New("simulated ACME staging failure")
}

func (fakeCertificateOperator) Execute(_ context.Context, plan certificateoperator.Plan) (certificateoperator.Observation, error) {
	now := time.Now().UTC()
	return certificateoperator.Observation{CertificateID: plan.Request.CertificateID, PrimaryDomain: plan.Request.PrimaryDomain, Domains: plan.Request.Domains, CertificatePath: plan.CertificatePath, PrivateKeyPath: plan.PrivateKeyPath, IssuedAt: now, ExpiresAt: now.Add(90 * 24 * time.Hour)}, nil
}

type fakeSiteOperator struct{}

func (fakeSiteOperator) Plan(_ context.Context, site siteoperator.Site) (siteoperator.Plan, error) {
	now := time.Now()
	return siteoperator.Plan{ID: "site-plan", Kind: siteoperator.PlanKind, Site: site, PlannedAt: now, ExpiresAt: now.Add(time.Hour), Signature: "signed"}, nil
}

type recordingSiteOperator struct {
	applied []siteoperator.Site
}

func (r *recordingSiteOperator) Plan(_ context.Context, site siteoperator.Site) (siteoperator.Plan, error) {
	now := time.Now()
	return siteoperator.Plan{ID: "site-plan", Kind: siteoperator.PlanKind, Site: site, PlannedAt: now, ExpiresAt: now.Add(time.Hour), Signature: "signed"}, nil
}

func (r *recordingSiteOperator) Apply(_ context.Context, plan siteoperator.Plan) (siteoperator.Observation, error) {
	r.applied = append(r.applied, plan.Site)
	return siteoperator.Observation{SiteID: plan.Site.ID, Active: true}, nil
}

func (*recordingSiteOperator) Rollback(context.Context, siteoperator.Plan) (siteoperator.Observation, error) {
	return siteoperator.Observation{}, nil
}

func (fakeSiteOperator) Apply(_ context.Context, plan siteoperator.Plan) (siteoperator.Observation, error) {
	return siteoperator.Observation{SiteID: plan.Site.ID, Active: true}, nil
}

func (fakeSiteOperator) Rollback(context.Context, siteoperator.Plan) (siteoperator.Observation, error) {
	return siteoperator.Observation{}, nil
}

func TestCertificateIssuePlanApplyAndExpiryState(t *testing.T) {
	ctx := context.Background()
	database, err := persistence.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	_, err = database.ExecContext(ctx, "CREATE TABLE sites (id TEXT PRIMARY KEY)")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = database.ExecContext(ctx, "INSERT INTO sites(id) VALUES ('site-1')")
	auditLog, _ := audit.New(ctx, database)
	queue, err := jobs.NewWithConfig(ctx, database, auditLog, slog.New(slog.NewTextHandler(io.Discard, nil)), jobs.Config{PollInterval: 5 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	site := sites.Site{ID: "site-1", Slug: "demo-site", PrimaryDomain: "demo.example.com", PHPVersion: "8.4", UnixUser: "nexa_demo_site", RootPath: "/srv/nexa/sites/demo-site", SocketPath: "/run/php/nexa-demo-site.sock", Status: sites.StatusActive}
	module, err := New(ctx, database, queue, fakeSites{site}, fakeDomains{}, fakeCertificateOperator{}, fakeSiteOperator{}, fakeResolver{})
	if err != nil {
		t.Fatal(err)
	}
	queue.Start(ctx)
	t.Cleanup(queue.Close)
	certificate, job, err := module.Create(ctx, CreateRequest{SiteID: "site-1", Email: "admin@example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitCertificateJob(t, queue, job.ID)
	plan, _, err := module.StoredPlan(ctx, certificate.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.AgentPlan.Request.Domains) != 2 {
		t.Fatalf("SANs = %v", plan.AgentPlan.Request.Domains)
	}
	apply, err := queue.Submit(ctx, "certificate.execute", plan, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitCertificateJob(t, queue, apply.ID)
	active, err := module.Get(ctx, certificate.ID)
	if err != nil || active.Status != StatusActive || active.ExpiresAt == nil {
		t.Fatalf("certificate = %+v, %v", active, err)
	}

	module.certificates = failingCertificateOperator{}
	routing := new(recordingSiteOperator)
	module.siteOperator = routing
	revokePlanJob, err := queue.Submit(ctx, "certificate.plan", map[string]string{"certificateId": certificate.ID, "operation": "revoke"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitCertificateJob(t, queue, revokePlanJob.ID)
	revokePlan, _, err := module.StoredPlan(ctx, certificate.ID)
	if err != nil {
		t.Fatal(err)
	}
	revokeJob, err := queue.Submit(ctx, "certificate.execute", revokePlan, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitCertificateJobFailure(t, queue, revokeJob.ID)

	preserved, err := module.Get(ctx, certificate.ID)
	if err != nil || preserved.Status != StatusActive || preserved.Failure == "" {
		t.Fatalf("failed revoke did not preserve the active certificate: %+v, %v", preserved, err)
	}
	tls, tlsDomains, err := module.TLSForSite(ctx, certificate.SiteID)
	if err != nil || tls == nil || len(tlsDomains) != 2 {
		t.Fatalf("preserved TLS = %+v domains=%v err=%v", tls, tlsDomains, err)
	}
	if len(routing.applied) != 2 || routing.applied[0].TLS != nil || routing.applied[1].TLS == nil {
		t.Fatalf("failed revoke routing sequence = %+v", routing.applied)
	}
}

func waitCertificateJob(t *testing.T, queue *jobs.Module, id int64) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		job, err := queue.Get(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if job.State == jobs.StateSucceeded {
			return
		}
		if job.State == jobs.StateFailed {
			t.Fatalf("job failed: %s", job.Failure)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("job timeout")
}

func waitCertificateJobFailure(t *testing.T, queue *jobs.Module, id int64) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		job, err := queue.Get(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if job.State == jobs.StateFailed {
			return
		}
		if job.State == jobs.StateSucceeded {
			t.Fatal("expected certificate job to fail")
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("job timeout")
}
