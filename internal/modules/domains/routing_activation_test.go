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

type runtimeCatalog map[string]bool

func (c runtimeCatalog) Allowed(context.Context, string) (bool, error) { return true, nil }

type fakeOperator struct{ renderer siteoperator.Renderer }

// PlanTeardown mirrors the real operator: the same rendered plan, but with an
// empty before-state so a rollback removes every managed path.
func (o fakeOperator) PlanTeardown(ctx context.Context, site siteoperator.Site) (siteoperator.Plan, error) {
	plan, err := o.Plan(ctx, site)
	if err != nil {
		return siteoperator.Plan{}, err
	}
	plan.Before = make([]siteoperator.Snapshot, len(plan.Artifacts))
	for index := range plan.Artifacts {
		plan.Before[index] = siteoperator.Snapshot{Path: plan.Artifacts[index].Path}
	}
	plan.RetiredBefore = make([]siteoperator.Snapshot, len(plan.Retired))
	for index, path := range plan.Retired {
		plan.RetiredBefore[index] = siteoperator.Snapshot{Path: path}
	}
	plan.EnabledBefore = false
	plan.Teardown = true
	return plan, nil
}

func (o fakeOperator) Plan(_ context.Context, site siteoperator.Site) (siteoperator.Plan, error) {
	plan, err := o.renderer.Render(site)
	if err != nil {
		return plan, err
	}
	plan.ID = "plan"
	plan.Kind = siteoperator.PlanKind
	plan.Signature = "signed"
	plan.PlannedAt = time.Now()
	plan.ExpiresAt = time.Now().Add(time.Hour)
	plan.Before = make([]siteoperator.Snapshot, len(plan.Artifacts))
	for i := range plan.Artifacts {
		plan.Before[i].Path = plan.Artifacts[i].Path
	}
	return plan, nil
}

func (fakeOperator) Apply(_ context.Context, plan siteoperator.Plan) (siteoperator.Observation, error) {
	return siteoperator.Observation{SiteID: plan.Site.ID, Active: true}, nil
}

func (fakeOperator) Rollback(_ context.Context, plan siteoperator.Plan) (siteoperator.Observation, error) {
	return siteoperator.Observation{SiteID: plan.Site.ID}, nil
}

type fakeResolver struct{}

func (fakeResolver) LookupHost(_ context.Context, host string) ([]string, error) {
	return []string{"203.0.113.10"}, nil
}

type emptyTLSProvider struct{}

func (emptyTLSProvider) TLSForSite(context.Context, string) (*siteoperator.TLS, []string, error) {
	return nil, nil, nil
}

func TestDomainPlanAndActivationIncludeAlias(t *testing.T) {
	ctx := context.Background()
	database, err := persistence.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := persistence.RunMigrations(context.Background(), database); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	auditLog, _ := audit.New(ctx, database)
	queue, err := jobs.NewWithConfig(ctx, database, auditLog, slog.New(slog.NewTextHandler(io.Discard, nil)), jobs.Config{PollInterval: 5 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	operator := fakeOperator{}
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
	domain, job, err := module.Create(ctx, CreateRequest{SiteID: site.ID, Hostname: "www.demo.example.com", Kind: KindAlias}, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitJob(t, queue, &job.ID)
	stored, _, err := module.StoredPlan(ctx, domain.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.NodePlan.Site.Routes) != 1 || stored.NodePlan.Site.Routes[0].Hostname != "www.demo.example.com" {
		t.Fatalf("routes = %+v", stored.NodePlan.Site.Routes)
	}
	apply, err := queue.Submit(ctx, "domain.activate", stored, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitJob(t, queue, &apply.ID)
	active, err := module.Get(ctx, domain.ID)
	if err != nil || active.Status != StatusActive {
		t.Fatalf("active domain = %+v, %v", active, err)
	}
}

func waitJob(t *testing.T, queue *jobs.Module, id *int64) {
	t.Helper()
	if id == nil {
		t.Fatal("missing job")
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		job, err := queue.Get(context.Background(), *id)
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

func (fakeOperator) Purge(context.Context, siteoperator.Site) error { return nil }
