package domains

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/modules/sites"
	"github.com/nexa-panel/nexa-panel/internal/platform/audit"
	"github.com/nexa-panel/nexa-panel/internal/platform/jobs"
	siteoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/sites"
	"github.com/nexa-panel/nexa-panel/internal/platform/persistence"
)

// recordingOperator counts Apply calls so a test can prove whether removing a
// domain re-rendered the live Nginx configuration or left the node untouched.
type recordingOperator struct {
	fakeOperator
	applies *int
}

func (o recordingOperator) Apply(ctx context.Context, plan siteoperator.Plan) (siteoperator.Observation, error) {
	*o.applies++
	return o.fakeOperator.Apply(ctx, plan)
}

// newActiveSiteAndDomains builds sites + domains modules, drives a site to
// active, and returns both modules and the active site.
func newActiveSiteAndDomains(t *testing.T, applies *int) (*sites.Module, *Module, *jobs.Module, sites.Site) {
	t.Helper()
	ctx := context.Background()
	database, err := persistence.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := persistence.RunMigrations(ctx, database); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	auditLog, _ := audit.New(ctx, database)
	queue, err := jobs.NewWithConfig(ctx, database, auditLog, slog.New(slog.NewTextHandler(io.Discard, nil)), jobs.Config{PollInterval: 5 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	operator := recordingOperator{applies: applies}
	sitesModule, err := sites.New(ctx, database, queue, runtimeCatalog{}, operator)
	if err != nil {
		t.Fatal(err)
	}
	site, _, err := sitesModule.Create(ctx, sites.CreateRequest{Slug: "demo-site", DisplayName: "Demo", PrimaryDomain: "demo.example.com", PHPVersion: "8.4"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	module, err := New(ctx, database, queue, sitesModule, operator, fakeResolver{})
	if err != nil {
		t.Fatal(err)
	}
	module.SetTLSProvider(emptyTLSProvider{})
	queue.Start(ctx)
	t.Cleanup(queue.Close)
	waitJob(t, queue, site.LastJobID)
	sitePlan, _, err := sitesModule.Plan(ctx, site.ID)
	if err != nil {
		t.Fatal(err)
	}
	activation, err := queue.Submit(ctx, "site.activate", sitePlan, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitJob(t, queue, &activation.ID)
	active, err := sitesModule.Get(ctx, site.ID)
	if err != nil {
		t.Fatal(err)
	}
	return sitesModule, module, queue, active
}

func activeAlias(t *testing.T, module *Module, queue *jobs.Module, siteID, hostname string) Domain {
	t.Helper()
	ctx := context.Background()
	domain, job, err := module.Create(ctx, CreateRequest{SiteID: siteID, Hostname: hostname, Kind: KindAlias}, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitJob(t, queue, &job.ID)
	stored, _, err := module.StoredPlan(ctx, domain.ID)
	if err != nil {
		t.Fatal(err)
	}
	apply, err := queue.Submit(ctx, "domain.activate", stored, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitJob(t, queue, &apply.ID)
	active, err := module.Get(ctx, domain.ID)
	if err != nil || active.Status != StatusActive {
		t.Fatalf("alias not active: %+v, %v", active, err)
	}
	return active
}

func TestDomainRemovableRejectsMidOperationStatuses(t *testing.T) {
	cases := map[Status]bool{
		StatusActive: true, StatusPlanReady: true, StatusFailed: true,
		StatusPlanning: false, StatusActivating: false, StatusRemoving: false,
	}
	for status, want := range cases {
		if got := domainRemovable(status); got != want {
			t.Errorf("domainRemovable(%q) = %v, want %v", status, got, want)
		}
	}
}

func TestRemoveJobReactivatesRoutingWithoutDomainAndDeletesRecord(t *testing.T) {
	ctx := context.Background()
	applies := 0
	_, module, queue, site := newActiveSiteAndDomains(t, &applies)
	alias := activeAlias(t, module, queue, site.ID, "www.demo.example.com")
	appliesBefore := applies

	job, err := queue.Submit(ctx, "domain.remove", removePayload{DomainID: alias.ID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitJob(t, queue, &job.ID)

	if _, err := module.Get(ctx, alias.ID); err == nil {
		t.Fatal("alias record still present after removal")
	}
	if applies <= appliesBefore {
		t.Fatalf("expected the live configuration to be re-applied without the removed alias (applies %d -> %d)", appliesBefore, applies)
	}
	// The remaining plan must no longer route the removed alias.
	routes, err := module.routesExcluding(ctx, site.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, route := range routes {
		if route.Hostname == "www.demo.example.com" {
			t.Fatalf("removed alias still routed: %+v", routes)
		}
	}
}

func TestRemoveJobIsIdempotentWhenDomainAlreadyGone(t *testing.T) {
	ctx := context.Background()
	applies := 0
	_, module, queue, _ := newActiveSiteAndDomains(t, &applies)
	job, err := queue.Submit(ctx, "domain.remove", removePayload{DomainID: "domain_missing"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitJob(t, queue, &job.ID)
	_ = module
}

func TestClampTLSDomainsDropsRemovedHostnames(t *testing.T) {
	routes := []siteoperator.Route{{Hostname: "www.demo.example.com", Kind: string(KindAlias)}}
	clamped := clampTLSDomains("demo.example.com", routes, []string{"demo.example.com", "www.demo.example.com", "gone.demo.example.com"})
	if len(clamped) != 2 {
		t.Fatalf("clamped = %v, want the two surviving names", clamped)
	}
	for _, name := range clamped {
		if name == "gone.demo.example.com" {
			t.Fatalf("clamped kept a removed hostname: %v", clamped)
		}
	}
}
