package sites

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/platform/audit"
	"github.com/nexa-panel/nexa-panel/internal/platform/jobs"
	"github.com/nexa-panel/nexa-panel/internal/platform/persistence"
	"github.com/uptrace/bun"
)

// newActiveSite builds a sites module on a real control database, then drives one
// site all the way to active so the teardown paths have managed state to remove.
func newActiveSite(t *testing.T) (*Module, *jobs.Module, Site) {
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
	module, err := New(ctx, database, queue, runtimeCatalog{"8.4": true}, fakeSiteOperator{})
	if err != nil {
		t.Fatal(err)
	}
	queue.Start(ctx)
	t.Cleanup(queue.Close)
	site, job, err := module.Create(ctx, CreateRequest{Slug: "demo-site", DisplayName: "Demo", PrimaryDomain: "demo.example.com", PHPVersion: "8.4"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitForJob(t, queue, job.ID)
	plan, _, err := module.Plan(ctx, site.ID)
	if err != nil {
		t.Fatal(err)
	}
	activation, err := queue.Submit(ctx, "site.activate", plan, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitForJob(t, queue, activation.ID)
	active, err := module.Get(ctx, site.ID)
	if err != nil {
		t.Fatal(err)
	}
	return module, queue, active
}

func TestSiteDeletableRejectsMidOperationStatuses(t *testing.T) {
	deletable := map[Status]bool{
		StatusActive: true, StatusDraft: true, StatusPlanReady: true, StatusRolledBack: true, StatusFailed: true,
		StatusPlanning: false, StatusActivating: false, StatusRollingBack: false, StatusDeleting: false,
	}
	for status, want := range deletable {
		if got := siteDeletable(status); got != want {
			t.Errorf("siteDeletable(%q) = %v, want %v", status, got, want)
		}
	}
}

// domainRow is a minimal insert model so the sites package can seed the domains
// table without importing the domains module (which imports sites).
type domainRow struct {
	bun.BaseModel     `bun:"table:domains,alias:domain"`
	ID                string `bun:",pk"`
	SiteID            string
	Hostname          string
	Kind              string
	Status            string
	ResolvedAddresses string
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

type certificateRow struct {
	bun.BaseModel `bun:"table:certificates,alias:certificate"`
	ID            string `bun:",pk"`
	SiteID        string
	PrimaryDomain string
	Email         string
	Status        string
	DomainsJSON   string `bun:"domains_json"`
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

func TestDependentBlockerListsAttachedDomainsAndCertificate(t *testing.T) {
	ctx := context.Background()
	module, _, site := newActiveSite(t)
	now := time.Now().UTC()
	if _, err := module.database.NewInsert().Model(&domainRow{ID: "domain_alias_1", SiteID: site.ID, Hostname: "www.demo.example.com", Kind: "alias", Status: "active", ResolvedAddresses: "[]", CreatedAt: now, UpdatedAt: now}).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := module.database.NewInsert().Model(&certificateRow{ID: "cert_1", SiteID: site.ID, PrimaryDomain: "demo.example.com", Email: "ops@example.com", Status: "active", DomainsJSON: "[]", CreatedAt: now, UpdatedAt: now}).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	blocker, err := module.dependentBlocker(ctx, site.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(blocker, "www.demo.example.com") || !strings.Contains(blocker, "TLS certificate") {
		t.Fatalf("blocker = %q, want it to name the domain and the certificate", blocker)
	}
	// A revoked certificate and no extra domains leaves nothing blocking.
	if _, err := module.database.NewDelete().Model((*domainRow)(nil)).Where("id = ?", "domain_alias_1").Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := module.database.NewUpdate().Model((*certificateRow)(nil)).Set("status = ?", "revoked").Where("id = ?", "cert_1").Exec(ctx); err != nil {
		t.Fatal(err)
	}
	blocker, err = module.dependentBlocker(ctx, site.ID)
	if err != nil {
		t.Fatal(err)
	}
	if blocker != "" {
		t.Fatalf("blocker after clearing dependents = %q, want empty", blocker)
	}
}

func TestSiteDeleteJobRemovesActiveSiteAndCascadesPrimaryDomain(t *testing.T) {
	ctx := context.Background()
	module, queue, site := newActiveSite(t)
	// Seed the primary domain row the domains module would create, to prove the
	// foreign-key cascade removes it with the site.
	now := time.Now().UTC()
	if _, err := module.database.NewInsert().Model(&domainRow{ID: "domain_primary_" + site.ID, SiteID: site.ID, Hostname: site.PrimaryDomain, Kind: "primary", Status: "active", ResolvedAddresses: "[]", CreatedAt: now, UpdatedAt: now}).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	job, err := queue.Submit(ctx, "site.delete", deletePayload{SiteID: site.ID, TeardownHost: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitForJob(t, queue, job.ID)
	if exists, err := module.SiteExists(ctx, site.ID); err != nil || exists {
		t.Fatalf("site still present after teardown (exists=%v, err=%v)", exists, err)
	}
	count, err := module.database.NewSelect().TableExpr("domains").Where("site_id = ?", site.ID).Count(ctx)
	if err != nil || count != 0 {
		t.Fatalf("primary domain rows after teardown = %d, err %v (foreign-key cascade expected)", count, err)
	}
	planCount, err := module.database.NewSelect().TableExpr("site_plans").Where("site_id = ?", site.ID).Count(ctx)
	if err != nil || planCount != 0 {
		t.Fatalf("site_plans rows after teardown = %d, err %v", planCount, err)
	}
}

func TestSiteDeleteJobWithoutHostTeardownJustRemovesRecord(t *testing.T) {
	ctx := context.Background()
	module, queue, site := newActiveSite(t)
	// TeardownHost=false must not touch the node operator, only remove the row.
	job, err := queue.Submit(ctx, "site.delete", deletePayload{SiteID: site.ID, TeardownHost: false}, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitForJob(t, queue, job.ID)
	if exists, _ := module.SiteExists(ctx, site.ID); exists {
		t.Fatal("site still present after record-only teardown")
	}
}
