package applications

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/platform/audit"
	"github.com/nexa-panel/nexa-panel/internal/platform/jobs"
	packagesoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/packages"
	"github.com/nexa-panel/nexa-panel/internal/platform/persistence"
)

// fakePackages simulates the privileged operator without touching apt: Apply
// mutates an in-memory installed set so a later List reflects the change.
type fakePackages struct {
	installed map[string]string
	lastApply packagesoperator.Plan
	// catalog stands in for what the node's repositories offer; catalogError
	// simulates a node whose apt index cannot be read.
	catalog      []packagesoperator.CatalogEntry
	catalogError error
}

func newFakePackages() *fakePackages {
	return &fakePackages{
		installed: map[string]string{},
		catalog: []packagesoperator.CatalogEntry{
			{App: "php", Version: "8.3", Label: "PHP 8.3", Category: "php", Packages: []string{"php8.3-fpm"}},
		},
	}
}

func (f *fakePackages) Catalog(context.Context) ([]packagesoperator.CatalogEntry, error) {
	if f.catalogError != nil {
		return nil, f.catalogError
	}
	return f.catalog, nil
}

func (f *fakePackages) Discover(context.Context) ([]packagesoperator.InstalledPackage, error) {
	items := []packagesoperator.InstalledPackage{}
	for name, version := range f.installed {
		items = append(items, packagesoperator.InstalledPackage{Name: name, Version: version, Installed: true})
	}
	return items, nil
}

func (f *fakePackages) Plan(_ context.Context, change packagesoperator.Change) (packagesoperator.Plan, error) {
	now := time.Now().UTC()
	return packagesoperator.Plan{
		ID: "agent-plan", Kind: packagesoperator.PlanKind, Change: change,
		Packages: []string{"php8.3-fpm"}, ObservedFingerprint: "fp",
		PlannedAt: now, ExpiresAt: now.Add(time.Hour), Signature: "signed",
	}, nil
}

func (f *fakePackages) Apply(_ context.Context, plan packagesoperator.Plan) (packagesoperator.Observation, error) {
	f.lastApply = plan
	if plan.Change.Action == packagesoperator.ActionInstall {
		f.installed["php8.3-fpm"] = "8.3.1"
		return packagesoperator.Observation{App: plan.Change.App, Version: "8.3.1", Installed: true, Verified: true}, nil
	}
	delete(f.installed, "php8.3-fpm")
	return packagesoperator.Observation{App: plan.Change.App, Installed: false, Verified: true}, nil
}

func newTestModule(t *testing.T) (*Module, *jobs.Module, *fakePackages) {
	t.Helper()
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
	operator := newFakePackages()
	module, err := New(ctx, database, queue, operator, nil)
	if err != nil {
		t.Fatal(err)
	}
	queue.Start(ctx)
	t.Cleanup(queue.Close)
	return module, queue, operator
}

func statusOf(t *testing.T, module *Module, id string) Application {
	t.Helper()
	items, err := module.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.ID == id {
			return item
		}
	}
	t.Fatalf("application %q not in catalog", id)
	return Application{}
}

func TestInstallFlowTransitionsToInstalled(t *testing.T) {
	ctx := context.Background()
	module, queue, operator := newTestModule(t)

	_, planJob, err := module.RequestChange(ctx, "php-8.3", packagesoperator.ActionInstall, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitJob(t, queue, planJob.ID)
	if got := statusOf(t, module, "php-8.3").Status; got != string(StatusPlanReady) {
		t.Fatalf("expected plan_ready, got %s", got)
	}

	stored, err := module.StoredPlan(ctx, "php-8.3")
	if err != nil || stored.Operation != packagesoperator.ActionInstall {
		t.Fatalf("stored plan=%+v err=%v", stored, err)
	}

	applyJob, err := module.ApplyPlan(ctx, "php-8.3", nil)
	if err != nil {
		t.Fatal(err)
	}
	waitJob(t, queue, applyJob.ID)

	if operator.lastApply.Change.App != "php" || operator.lastApply.Change.Version != "8.3" {
		t.Fatalf("operator received wrong change: %+v", operator.lastApply.Change)
	}
	installed := statusOf(t, module, "php-8.3")
	if installed.Status != string(StatusInstalled) || installed.InstalledVersion != "8.3.1" {
		t.Fatalf("expected installed 8.3.1, got %+v", installed)
	}
}

func TestUninstallMapsToRemove(t *testing.T) {
	ctx := context.Background()
	module, queue, operator := newTestModule(t)
	operator.installed["php8.3-fpm"] = "8.3.1"

	_, planJob, err := module.RequestChange(ctx, "php-8.3", packagesoperator.ActionRemove, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitJob(t, queue, planJob.ID)
	applyJob, err := module.ApplyPlan(ctx, "php-8.3", nil)
	if err != nil {
		t.Fatal(err)
	}
	waitJob(t, queue, applyJob.ID)

	if operator.lastApply.Change.Action != packagesoperator.ActionRemove {
		t.Fatalf("expected remove action, got %s", operator.lastApply.Change.Action)
	}
	if got := statusOf(t, module, "php-8.3").Status; got != string(StatusAvailable) {
		t.Fatalf("expected available after removal, got %s", got)
	}
}

func TestRequestChangeRejectsUnknownApplication(t *testing.T) {
	module, _, _ := newTestModule(t)
	if _, _, err := module.RequestChange(context.Background(), "redis-7", packagesoperator.ActionInstall, nil); err == nil {
		t.Fatal("expected rejection for an application outside the catalog")
	}
}

func waitJob(t *testing.T, queue *jobs.Module, id int64) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		job, err := queue.Get(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if job.State.Terminal() {
			if job.State != jobs.StateSucceeded {
				t.Fatalf("job %d ended in %s", id, job.State)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("job %d timed out", id)
}
