package sites

import (
	"context"
	"errors"
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

// sshAccessRow mirrors the deploy module's site_ssh_access table so the blocker
// can be exercised without importing that module.
type sshAccessRow struct {
	bun.BaseModel `bun:"table:site_ssh_access,alias:site_ssh_access"`
	SiteID        string `bun:",pk"`
	Enabled       bool
	Username      string
	Shell         string
	UpdatedAt     time.Time
}

func TestDependentBlockerRefusesDeletionWhileSSHAccessIsEnabled(t *testing.T) {
	ctx := context.Background()
	module, _, site := newActiveSite(t)
	now := time.Now().UTC()
	row := &sshAccessRow{SiteID: site.ID, Enabled: true, Username: "nexa_demo", Shell: "/bin/bash", UpdatedAt: now}
	if _, err := module.database.NewInsert().Model(row).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	blocker, err := module.dependentBlocker(ctx, site.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(blocker, "SSH access") {
		t.Fatalf("blocker = %q, want it to name SSH access", blocker)
	}
	// Disabling access leaves nothing on the node to strand, so deletion proceeds.
	if _, err := module.database.NewUpdate().Model((*sshAccessRow)(nil)).Set("enabled = ?", false).Where("site_id = ?", site.ID).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	blocker, err = module.dependentBlocker(ctx, site.ID)
	if err != nil {
		t.Fatal(err)
	}
	if blocker != "" {
		t.Fatalf("blocker after disabling SSH access = %q, want empty", blocker)
	}
}

// sftpAccessRow mirrors the sftp module's table for the same reason as above.
type sftpAccessRow struct {
	bun.BaseModel `bun:"table:sftp_access,alias:sftp_access"`
	SiteID        string `bun:",pk"`
	Enabled       bool
	Username      string
	UpdatedAt     time.Time
}

// SFTP strands the same class of node state as SSH access: a drop-in the site
// teardown does not remove and a live password on an account nothing deletes.
func TestDependentBlockerRefusesDeletionWhileSFTPIsEnabled(t *testing.T) {
	ctx := context.Background()
	module, _, site := newActiveSite(t)
	now := time.Now().UTC()
	row := &sftpAccessRow{SiteID: site.ID, Enabled: true, Username: "nexa_demo", UpdatedAt: now}
	if _, err := module.database.NewInsert().Model(row).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	blocker, err := module.dependentBlocker(ctx, site.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(blocker, "SFTP access") {
		t.Fatalf("blocker = %q, want it to name SFTP access", blocker)
	}
	if _, err := module.database.NewUpdate().Model((*sftpAccessRow)(nil)).Set("enabled = ?", false).Where("site_id = ?", site.ID).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	blocker, err = module.dependentBlocker(ctx, site.ID)
	if err != nil {
		t.Fatal(err)
	}
	if blocker != "" {
		t.Fatalf("blocker after disabling SFTP = %q, want empty", blocker)
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

// fakeDeployTeardown records the sites it was asked to withdraw grants for, and
// can refuse, so a test can prove the row survives a withdrawal the node
// rejected.
type fakeDeployTeardown struct {
	siteIDs []string
	err     error
}

func (f *fakeDeployTeardown) TeardownSiteDeployment(_ context.Context, siteID string) error {
	f.siteIDs = append(f.siteIDs, siteID)
	return f.err
}

// The deploy-side grants live outside this module's artifacts — a sudoers
// drop-in outlives the vhost — so the delete job withdraws them while the row
// still exists.
func TestSiteDeleteJobWithdrawsDeployGrantsBeforeRemovingTheRecord(t *testing.T) {
	ctx := context.Background()
	module, queue, site := newActiveSite(t)
	teardown := &fakeDeployTeardown{}
	module.SetDeployTeardown(teardown)

	job, err := queue.Submit(ctx, "site.delete", deletePayload{SiteID: site.ID, TeardownHost: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitForJob(t, queue, job.ID)
	if len(teardown.siteIDs) != 1 || teardown.siteIDs[0] != site.ID {
		t.Fatalf("withdrawn grants = %v, want [%s]", teardown.siteIDs, site.ID)
	}
	if exists, _ := module.SiteExists(ctx, site.ID); exists {
		t.Fatal("site still present after teardown")
	}
}

// A withdrawal that failed must stop the deletion: removing the row would leave
// a sudoers rule naming a slug the panel no longer knows about, which the next
// site created with that slug would inherit.
func TestSiteDeleteJobStopsWhenDeployGrantsCannotBeWithdrawn(t *testing.T) {
	ctx := context.Background()
	module, queue, site := newActiveSite(t)
	module.SetDeployTeardown(&fakeDeployTeardown{err: errors.New("node unreachable")})

	job, err := queue.Submit(ctx, "site.delete", deletePayload{SiteID: site.ID, TeardownHost: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitForFailedJob(t, queue, job.ID, "node unreachable")
	if exists, err := module.SiteExists(ctx, site.ID); err != nil || !exists {
		t.Fatalf("the site row was removed despite a failed withdrawal (exists=%v, err=%v)", exists, err)
	}
}

// waitForFailedJob is waitForJob's mirror: it requires the job to fail, and to
// fail for the stated reason, so a test asserting that a refusal was honoured
// cannot pass on some unrelated failure.
func waitForFailedJob(t *testing.T, queue *jobs.Module, id int64, reason string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		job, err := queue.Get(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if job.State == jobs.StateSucceeded {
			t.Fatal("job succeeded, want a failure")
		}
		if job.State == jobs.StateFailed {
			if !strings.Contains(job.Failure, reason) {
				t.Fatalf("job failed with %q, want it to mention %q", job.Failure, reason)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("job did not complete")
}
