package sites

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/platform/audit"
	"github.com/nexa-panel/nexa-panel/internal/platform/jobs"
	siteoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/sites"
	"github.com/nexa-panel/nexa-panel/internal/platform/persistence"
)

type runtimeCatalog map[string]bool

func (catalog runtimeCatalog) Allowed(_ context.Context, version string) (bool, error) {
	return catalog[version], nil
}

type fakeSiteOperator struct{ renderer siteoperator.Renderer }

func (operator fakeSiteOperator) Plan(_ context.Context, site siteoperator.Site) (siteoperator.Plan, error) {
	plan, err := operator.renderer.Render(site)
	if err != nil {
		return siteoperator.Plan{}, err
	}
	plan.ID = "signed-plan"
	plan.Kind = siteoperator.PlanKind
	plan.PlannedAt = time.Now().UTC()
	plan.ExpiresAt = plan.PlannedAt.Add(30 * time.Minute)
	plan.Signature = "agent-signature"
	plan.Before = make([]siteoperator.Snapshot, len(plan.Artifacts))
	for index := range plan.Artifacts {
		plan.Before[index].Path = plan.Artifacts[index].Path
	}
	return plan, nil
}
func (fakeSiteOperator) Apply(_ context.Context, plan siteoperator.Plan) (siteoperator.Observation, error) {
	return siteoperator.Observation{SiteID: plan.Site.ID, Active: true, VerifiedAt: time.Now().UTC()}, nil
}
func (fakeSiteOperator) Rollback(_ context.Context, plan siteoperator.Plan) (siteoperator.Observation, error) {
	return siteoperator.Observation{SiteID: plan.Site.ID, Active: false, VerifiedAt: time.Now().UTC()}, nil
}

func TestCreateDerivesPrivilegedIdentityAndBuildsDurablePlan(t *testing.T) {
	ctx := context.Background()
	database, err := persistence.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	auditLog, err := audit.New(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	queue, err := jobs.NewWithConfig(ctx, database, auditLog, slog.New(slog.NewTextHandler(io.Discard, nil)), jobs.Config{PollInterval: 5 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	module, err := New(ctx, database, queue, runtimeCatalog{"7.4": true, "8.4": true}, fakeSiteOperator{})
	if err != nil {
		t.Fatal(err)
	}
	queue.Start(context.Background())
	t.Cleanup(queue.Close)

	actor := "admin-1"
	site, job, err := module.Create(ctx, CreateRequest{Slug: "demo-site", DisplayName: "Demo Site", PrimaryDomain: "DEMO.EXAMPLE.COM.", PHPVersion: "8.4"}, &actor)
	if err != nil {
		t.Fatal(err)
	}
	if site.PrimaryDomain != "demo.example.com" || site.UnixUser != "nexa_demo_site" || site.RootPath != "/srv/nexa/sites/demo-site" {
		t.Fatalf("unexpected derived site: %+v", site)
	}
	waitForJob(t, queue, job.ID)
	stored, err := module.Get(ctx, site.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != StatusPlanReady {
		t.Fatalf("site status = %s", stored.Status)
	}
	plan, expiresAt, err := module.Plan(ctx, site.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Artifacts) != 3 || !expiresAt.After(time.Now()) {
		t.Fatalf("unexpected plan: %+v, expires %v", plan, expiresAt)
	}
}

func TestCreateRejectsUnavailableRuntimeAndDuplicateDomain(t *testing.T) {
	ctx := context.Background()
	database, _ := persistence.Open(filepath.Join(t.TempDir(), "control.db"))
	t.Cleanup(func() { _ = database.Close() })
	auditLog, _ := audit.New(ctx, database)
	queue, _ := jobs.NewWithConfig(ctx, database, auditLog, slog.New(slog.NewTextHandler(io.Discard, nil)), jobs.Config{PollInterval: time.Second})
	module, err := New(ctx, database, queue, runtimeCatalog{"7.4": true, "8.4": true}, fakeSiteOperator{})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := module.Create(ctx, CreateRequest{Slug: "old-php", DisplayName: "Old", PrimaryDomain: "old.example.com", PHPVersion: "7.4"}, nil); err != nil {
		t.Fatalf("PHP 7.4 should remain allowed when installed: %v", err)
	}
	first := CreateRequest{Slug: "first-site", DisplayName: "First", PrimaryDomain: "same.example.com", PHPVersion: "8.4"}
	if _, _, err := module.Create(ctx, first, nil); err != nil {
		t.Fatal(err)
	}
	second := CreateRequest{Slug: "second-site", DisplayName: "Second", PrimaryDomain: "same.example.com", PHPVersion: "8.4"}
	if _, _, err := module.Create(ctx, second, nil); err == nil {
		t.Fatal("expected duplicate domain error")
	}
}

func waitForJob(t *testing.T, queue *jobs.Module, id int64) {
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
			t.Fatalf("planning job failed: %s", job.Failure)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("planning job did not complete")
}
