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

	"github.com/nexa-panel/nexa-panel/migrations"

	"github.com/nexa-panel/nexa-panel/internal/platform/audit"
	"github.com/nexa-panel/nexa-panel/internal/platform/jobs"
	siteoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/sites"
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

func TestDependentBlockerRefusesDeletionWhileSchedulesOrBackupPlansTargetSite(t *testing.T) {
	ctx := context.Background()
	module, _, site := newActiveSite(t)
	now := time.Now().UTC()
	if _, err := module.database.ExecContext(ctx, `
		INSERT INTO scheduled_tasks
			(id, site_id, name, cron_expression, command, timeout_seconds, enabled, status, pending_removal, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"task_1", site.ID, "Queue worker", "*/5 * * * *", "php artisan queue:work --stop-when-empty", 300, true, "active", false, now, now); err != nil {
		t.Fatalf("seed scheduled task: %v", err)
	}
	if _, err := module.database.ExecContext(ctx, `
		INSERT INTO backup_accounts (id, name, type, path, config_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, "account_1", "Local", "local", "/var/backups", `{}`, now, now); err != nil {
		t.Fatalf("seed backup account: %v", err)
	}
	if _, err := module.database.ExecContext(ctx, `
		INSERT INTO backup_plans
			(id, name, account_id, copies_limit, site_ids, database_ids, schedule, enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"plan_1", "Nightly site", "account_1", 7, `["`+site.ID+`"]`, `[]`, "0 2 * * *", true, now, now); err != nil {
		t.Fatalf("seed backup plan: %v", err)
	}

	blocker, err := module.dependentBlocker(ctx, site.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(blocker, "scheduled task") || !strings.Contains(blocker, "backup plan") {
		t.Fatalf("blocker = %q, want both node schedule and backup-plan references", blocker)
	}

	if _, err := module.database.ExecContext(ctx, "DELETE FROM scheduled_tasks WHERE id = ?", "task_1"); err != nil {
		t.Fatal(err)
	}
	if _, err := module.database.ExecContext(ctx, "UPDATE backup_plans SET site_ids = '[]' WHERE id = ?", "plan_1"); err != nil {
		t.Fatal(err)
	}
	blocker, err = module.dependentBlocker(ctx, site.ID)
	if err != nil {
		t.Fatal(err)
	}
	if blocker != "" {
		t.Fatalf("blocker after removing references = %q, want empty", blocker)
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
	// TeardownHost=false skips the configuration rollback; the account and root
	// purge still runs, because a site that never activated can still have both
	// from a failed attempt, and purging what is not there is a no-op.
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

// convergingSiteOperator mimics the node's real teardown semantics: a plan
// marked as a teardown may be rolled back repeatedly, an ordinary rollback of
// an already-stripped site is refused as drift, and Purge is idempotent. Only
// with those semantics does a module-level retry test mean anything.
type convergingSiteOperator struct {
	fakeSiteOperator
	tornDown map[string]bool
	purged   []string
	purgeErr error
}

func newConvergingOperator() *convergingSiteOperator {
	return &convergingSiteOperator{tornDown: map[string]bool{}}
}

func (o *convergingSiteOperator) Rollback(ctx context.Context, plan siteoperator.Plan) (siteoperator.Observation, error) {
	if o.tornDown[plan.Site.Slug] && !plan.Teardown {
		return siteoperator.Observation{}, errors.New("managed site changed after activation; automatic rollback is unsafe")
	}
	o.tornDown[plan.Site.Slug] = true
	return o.fakeSiteOperator.Rollback(ctx, plan)
}

func (o *convergingSiteOperator) Purge(_ context.Context, site siteoperator.Site) error {
	if o.purgeErr != nil {
		return o.purgeErr
	}
	o.purged = append(o.purged, site.UnixUser+" "+site.RootPath)
	return nil
}

// newActiveSiteWith is newActiveSite with a caller-supplied operator, so a test
// can observe what the node was asked to do.
func newActiveSiteWith(t *testing.T, operator siteoperator.Operator) (*Module, *jobs.Module, Site) {
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
	module, err := New(ctx, database, queue, runtimeCatalog{"8.4": true}, operator)
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

// The site's Unix account and its files are created by an activation and were
// removed by nothing at all, so every deleted site used to leave an orphaned
// account owning an orphaned tree.
func TestSiteDeleteJobRemovesTheUnixAccountAndSiteRoot(t *testing.T) {
	ctx := context.Background()
	operator := newConvergingOperator()
	module, queue, site := newActiveSiteWith(t, operator)
	job, err := queue.Submit(ctx, "site.delete", deletePayload{SiteID: site.ID, TeardownHost: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitForJob(t, queue, job.ID)
	want := site.UnixUser + " " + site.RootPath
	if len(operator.purged) != 1 || operator.purged[0] != want {
		t.Fatalf("purged = %v, want [%q]", operator.purged, want)
	}
	if exists, _ := module.SiteExists(ctx, site.ID); exists {
		t.Fatal("site still present after teardown")
	}
}

// A node that cannot remove the account must stop the deletion: dropping the row
// would leave the account behind with nothing left that knows to remove it.
func TestSiteDeleteJobStopsWhenTheAccountCannotBeRemoved(t *testing.T) {
	ctx := context.Background()
	operator := newConvergingOperator()
	operator.purgeErr = errors.New("account still owns a running process")
	module, queue, site := newActiveSiteWith(t, operator)
	job, err := queue.Submit(ctx, "site.delete", deletePayload{SiteID: site.ID, TeardownHost: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitForFailedJob(t, queue, job.ID, "account still owns a running process")
	if exists, err := module.SiteExists(ctx, site.ID); err != nil || !exists {
		t.Fatalf("the row was removed despite a failed purge (exists=%v, err=%v)", exists, err)
	}
	// Failed, not deleting: the site must remain deletable so the operator can
	// clear the cause and try again.
	after, err := module.Get(ctx, site.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !siteDeletable(after.Status) {
		t.Fatalf("status after a failed teardown = %q, which is not deletable", after.Status)
	}
}

// The core regression: a teardown that was interrupted after the node work but
// before the row was deleted used to fail forever, because the retry re-entered
// the rollback and the node reported every already-removed artifact as drift.
func TestSiteDeleteJobConvergesAfterAnInterruptedAttempt(t *testing.T) {
	ctx := context.Background()
	operator := newConvergingOperator()
	module, queue, site := newActiveSiteWith(t, operator)
	// Exactly what a crash between teardownHost and the row delete leaves behind:
	// the node stripped, the row still there and still marked deleting.
	if err := module.teardownHost(ctx, site); err != nil {
		t.Fatal(err)
	}
	if err := module.purgeHost(ctx, site); err != nil {
		t.Fatal(err)
	}
	if _, err := module.database.NewUpdate().Model((*siteModel)(nil)).Set("status = ?", StatusDeleting).
		Where("id = ?", site.ID).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	job, err := queue.Submit(ctx, "site.delete", deletePayload{SiteID: site.ID, TeardownHost: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitForJob(t, queue, job.ID)
	if exists, err := module.SiteExists(ctx, site.ID); err != nil || exists {
		t.Fatalf("the retried teardown did not converge (exists=%v, err=%v)", exists, err)
	}
}

// Running the whole job twice is the same convergence seen from the queue: the
// second run finds nothing left and reports success rather than wedging the row.
func TestSiteDeleteJobRunTwiceConverges(t *testing.T) {
	ctx := context.Background()
	operator := newConvergingOperator()
	module, queue, site := newActiveSiteWith(t, operator)
	payload := deletePayload{SiteID: site.ID, TeardownHost: true}
	first, err := queue.Submit(ctx, "site.delete", payload, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitForJob(t, queue, first.ID)
	second, err := queue.Submit(ctx, "site.delete", payload, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitForJob(t, queue, second.ID)
	if exists, _ := module.SiteExists(ctx, site.ID); exists {
		t.Fatal("site still present after two teardown runs")
	}
}

// site.delete has to be recoverable, or a worker that dies mid-teardown leaves
// the row in "deleting" — which siteDeletable rejects — for good.
func TestSiteDeleteIsRetriedAfterAnInterruptedWorker(t *testing.T) {
	ctx := context.Background()
	database, err := persistence.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := persistence.RunMigrations(ctx, database); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	now := time.Now().UTC()
	if _, err := database.ExecContext(ctx, `
		INSERT INTO sites (id, slug, display_name, primary_domain, php_version, unix_user, root_path, socket_path, status, deployment_mode, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"site_stranded", "stranded", "Stranded", "stranded.example.com", "8.4", "nexa_stranded",
		"/srv/nexa/sites/stranded", "/run/php/nexa-stranded.sock", string(StatusDeleting), "standard", now, now); err != nil {
		t.Fatal(err)
	}
	// The row a killed worker leaves behind: running, with an expired lease.
	if _, err := database.ExecContext(ctx, `
		INSERT INTO jobs (id, kind, title, state, progress, request_json, recovery_policy, scope_site_ids, lease_token, lease_expires_at, created_at, updated_at, started_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		int64(41), "site.delete", "Delete site Stranded", jobs.StateRunning, 45,
		`{"siteId":"site_stranded","teardownHost":true}`, jobs.RecoveryRetry, `["site_stranded"]`,
		"expired", now.Add(-time.Minute), now, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, "UPDATE sites SET last_job_id = ? WHERE id = ?", 41, "site_stranded"); err != nil {
		t.Fatal(err)
	}
	auditLog, _ := audit.New(ctx, database)
	queue, err := jobs.NewWithConfig(ctx, database, auditLog, slog.New(slog.NewTextHandler(io.Discard, nil)), jobs.Config{PollInterval: 5 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	module, err := New(ctx, database, queue, runtimeCatalog{"8.4": true}, newConvergingOperator())
	if err != nil {
		t.Fatal(err)
	}
	queue.Start(ctx)
	t.Cleanup(queue.Close)
	waitForJob(t, queue, 41)
	if exists, err := module.SiteExists(ctx, "site_stranded"); err != nil || exists {
		t.Fatalf("the recovered teardown did not finish (exists=%v, err=%v)", exists, err)
	}
}

// The belt to the retry's braces: a "deleting" row whose job is already terminal
// (a teardown interrupted before the retry policy existed, say) is released on
// startup, so no site is ever permanently undeletable.
func TestStrandedDeletingSiteIsReleasedOnStartup(t *testing.T) {
	ctx := context.Background()
	module, _, site := newActiveSiteWith(t, newConvergingOperator())
	if _, err := module.database.ExecContext(ctx,
		"UPDATE sites SET status = ?, last_job_id = NULL WHERE id = ?", string(StatusDeleting), site.ID); err != nil {
		t.Fatal(err)
	}
	if err := module.reconcileInterruptedTeardowns(ctx); err != nil {
		t.Fatal(err)
	}
	released, err := module.Get(ctx, site.ID)
	if err != nil {
		t.Fatal(err)
	}
	if released.Status != StatusFailed || !siteDeletable(released.Status) {
		t.Fatalf("status = %q, want a deletable failed row", released.Status)
	}
	if released.Failure == "" {
		t.Fatal("the released row does not say why it was reset")
	}
}

// A live teardown must survive the same sweep, or a worker doing its job would
// have the row pulled out from under it.
func TestReconcileLeavesALiveTeardownAlone(t *testing.T) {
	ctx := context.Background()
	module, queue, site := newActiveSiteWith(t, newConvergingOperator())
	job, err := queue.Submit(ctx, "site.delete", deletePayload{SiteID: site.ID, TeardownHost: false}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := module.database.ExecContext(ctx,
		"UPDATE sites SET status = ?, last_job_id = ? WHERE id = ?", string(StatusDeleting), job.ID, site.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := module.database.ExecContext(ctx, "UPDATE jobs SET state = ? WHERE id = ?", jobs.StateRunning, job.ID); err != nil {
		t.Fatal(err)
	}
	if err := module.reconcileInterruptedTeardowns(ctx); err != nil {
		t.Fatal(err)
	}
	current, err := module.Get(ctx, site.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != StatusDeleting {
		t.Fatalf("status = %q, want the in-flight teardown left as deleting", current.Status)
	}
}

// Stored copies hang off the plan, never off the site, and a plan holding copies
// cannot be deleted — so a teardown that only mentioned the plan sent the
// operator round in a circle. The database is deliberately named nothing like
// the site: it is found through site_id, so the old "nexa_<slug>*" match that
// would have missed it entirely no longer decides anything.
func TestDependentBlockerReportsStoredCopiesAndOwnedDatabases(t *testing.T) {
	ctx := context.Background()
	module, _, site := newActiveSite(t)
	now := time.Now().UTC()
	if _, err := module.database.ExecContext(ctx, `
		INSERT INTO backup_accounts (id, name, type, path, config_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, "account_1", "Local", "local", "/var/backups", `{}`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := module.database.ExecContext(ctx, `
		INSERT INTO backup_plans (id, name, account_id, copies_limit, site_ids, database_ids, schedule, enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"plan_1", "Nightly site", "account_1", 7, `["`+site.ID+`"]`, `[]`, "0 2 * * *", true, now, now); err != nil {
		t.Fatal(err)
	}
	// The plan's targets are a real relation now, maintained from the JSON column.
	targets, err := module.database.NewSelect().TableExpr("backup_plan_sites").Where("site_id = ?", site.ID).Count(ctx)
	if err != nil || targets != 1 {
		t.Fatalf("backup_plan_sites rows = %d, err %v, want the plan's target joined", targets, err)
	}
	if _, err := module.database.ExecContext(ctx, `
		INSERT INTO backup_copies (id, plan_id, account_id, copy_name, remote_path, size_bytes, entries, status, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"copy_1", "plan_1", "account_1", "2026-07-22", "local:/var/backups/2026-07-22", 1024, `[]`, "complete", now); err != nil {
		t.Fatal(err)
	}
	seedMySQLDatabase(t, module, "analytics_prod", site.ID)

	blocker, err := module.dependentBlocker(ctx, site.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(blocker, "stored backup copies") {
		t.Fatalf("blocker = %q, want it to name the stored copies", blocker)
	}
	if !strings.Contains(blocker, "analytics_prod") {
		t.Fatalf("blocker = %q, want it to name the site's database", blocker)
	}

	// Repointing the plan away from the site clears both the join row and the
	// copies that were only relevant through it.
	if _, err := module.database.ExecContext(ctx, "UPDATE backup_plans SET site_ids = '[]' WHERE id = ?", "plan_1"); err != nil {
		t.Fatal(err)
	}
	if _, err := module.database.ExecContext(ctx, "DELETE FROM mysql_databases"); err != nil {
		t.Fatal(err)
	}
	blocker, err = module.dependentBlocker(ctx, site.ID)
	if err != nil {
		t.Fatal(err)
	}
	if blocker != "" {
		t.Fatalf("blocker after clearing the plan targets = %q, want empty", blocker)
	}
}

// The database trigger is the last line of defence behind dependentBlocker: a
// site row must not be able to vanish from under a plan that still names it.
func TestSiteRowCannotBeDeletedWhileABackupPlanTargetsIt(t *testing.T) {
	ctx := context.Background()
	module, _, site := newActiveSite(t)
	now := time.Now().UTC()
	if _, err := module.database.ExecContext(ctx, `
		INSERT INTO backup_accounts (id, name, type, path, config_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, "account_1", "Local", "local", "/var/backups", `{}`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := module.database.ExecContext(ctx, `
		INSERT INTO backup_plans (id, name, account_id, copies_limit, site_ids, database_ids, schedule, enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"plan_1", "Nightly", "account_1", 7, `["`+site.ID+`"]`, `[]`, "0 2 * * *", true, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := module.database.NewDelete().Model((*siteModel)(nil)).Where("id = ?", site.ID).Exec(ctx); err == nil {
		t.Fatal("the site row was deleted while a backup plan still targeted it")
	}
}

// A database owned by a site whose name follows no convention at all — the case
// the old name match could not see, and the reason DATA-001 needed a relation.
func TestDependentBlockerFindsADatabaseWhoseNameDoesNotNameTheSite(t *testing.T) {
	ctx := context.Background()
	module, _, site := newActiveSite(t)
	seedMySQLDatabase(t, module, "wp_prod_2026", site.ID)
	blocker, err := module.dependentBlocker(ctx, site.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(blocker, "wp_prod_2026") {
		t.Fatalf("blocker = %q, want the owned database reported despite its name", blocker)
	}
	// The mirror image: a database matching the old convention but owned by
	// nobody is not this site's problem, and must not block its teardown.
	if _, err := module.database.ExecContext(ctx, "UPDATE mysql_databases SET site_id = NULL, name = ? WHERE id = ?", site.UnixUser+"_app", "db_1"); err != nil {
		t.Fatal(err)
	}
	blocker, err = module.dependentBlocker(ctx, site.ID)
	if err != nil {
		t.Fatal(err)
	}
	if blocker != "" {
		t.Fatalf("blocker for an unowned database = %q, want empty", blocker)
	}
}

// The last line of defence behind dependentBlocker, matching the backup-plan
// guard: a site row must not vanish from under a database it still owns.
func TestSiteRowCannotBeDeletedWhileItOwnsADatabase(t *testing.T) {
	ctx := context.Background()
	module, _, site := newActiveSite(t)
	seedMySQLDatabase(t, module, "wp_prod_2026", site.ID)
	if _, err := module.database.NewDelete().Model((*siteModel)(nil)).Where("id = ?", site.ID).Exec(ctx); err == nil {
		t.Fatal("the site row was deleted while it still owned a database")
	}
}

// The relation has to survive the upgrade, not just new writes: rolling the
// migration back over a populated database and re-applying it must reconstruct
// the ownership the naming convention used to carry.
func TestDatabaseSiteOwnershipMigrationBackfillsFromTheNamingConvention(t *testing.T) {
	ctx := context.Background()
	module, _, site := newActiveSite(t)
	run := func(file string) {
		body, err := migrations.FS.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, statement := range strings.Split(string(body), "--bun:split") {
			if strings.TrimSpace(statement) == "" {
				continue
			}
			if _, err := module.database.ExecContext(ctx, statement); err != nil {
				t.Fatalf("%s: %v\n%s", file, err, statement)
			}
		}
	}
	run("20260723000001_database_site_ownership.tx.down.sql")
	// Pre-migration rows: one named after the site's account, one named after
	// nothing in particular.
	seedMySQLDatabase(t, module, site.UnixUser+"_app", "")
	if _, err := module.database.ExecContext(ctx, `
		INSERT INTO mysql_databases (id, engine_id, name, owner_account_id, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, "db_2", "engine_1", "unrelated_db", "account_db_1", "ready", time.Now().UTC(), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	run("20260723000001_database_site_ownership.tx.up.sql")

	owned, err := module.databasesOwnedBySite(ctx, site.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(owned) != 1 || owned[0] != site.UnixUser+"_app" {
		t.Fatalf("backfilled databases = %v, want only the one named after the site", owned)
	}
	var orphan *string
	if err := module.database.NewSelect().TableExpr("mysql_databases").Column("site_id").
		Where("id = ?", "db_2").Scan(ctx, &orphan); err != nil {
		t.Fatal(err)
	}
	if orphan != nil {
		t.Fatalf("unrelated database was attached to %q", *orphan)
	}
}

// A scoped user's grants are the one site reference left with no constraint
// behind it: identity_site_grants.site_id is a plain column, so a deleted site
// leaves the grant behind. This pins the current behaviour — the teardown is
// not blocked by a grant, which is right, but the row survives, which is not —
// so whoever adds the cascade has a test that flips.
func TestSiteAccessGrantsOutliveTheSiteTheyName(t *testing.T) {
	ctx := context.Background()
	module, _, site := newActiveSite(t)
	now := time.Now().UTC()
	if _, err := module.database.ExecContext(ctx, `
		INSERT INTO identity_users (id, username, password_hash, created_at) VALUES (?, ?, ?, ?)`,
		"user_1", "scoped", "hash", now); err != nil {
		t.Fatal(err)
	}
	if _, err := module.database.ExecContext(ctx, `
		INSERT INTO identity_site_grants (user_id, site_id, created_at) VALUES (?, ?, ?)`,
		"user_1", site.ID, now); err != nil {
		t.Fatal(err)
	}
	blocker, err := module.dependentBlocker(ctx, site.ID)
	if err != nil {
		t.Fatal(err)
	}
	if blocker != "" {
		t.Fatalf("blocker = %q, want a grant not to block the teardown", blocker)
	}
	if _, err := module.database.NewDelete().Model((*siteModel)(nil)).Where("id = ?", site.ID).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	remaining, err := module.database.NewSelect().TableExpr("identity_site_grants").Where("site_id = ?", site.ID).Count(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if remaining != 1 {
		t.Fatalf("grants after the site was deleted = %d, want the known-stale row (update this test when the cascade lands)", remaining)
	}
}

// seedMySQLDatabase writes the engine, account, and database rows a managed
// MySQL database needs, so the ownership check has something real to find. An
// empty siteID leaves the database owned by no site.
func seedMySQLDatabase(t *testing.T, module *Module, name, siteID string) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	var owner *string
	if siteID != "" {
		owner = &siteID
	}
	if _, err := module.database.ExecContext(ctx, `
		INSERT INTO mysql_family_engines (id, kind, version, version_text, port, status, socket_path, systemd_unit, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"engine_1", "mysql", "8.4", "8.4.0", 3306, "running", "/run/mysqld/mysqld.sock", "mysql.service", now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := module.database.ExecContext(ctx, `
		INSERT INTO mysql_accounts (id, engine_id, name, host, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, "account_db_1", "engine_1", "app", "localhost", "ready", now, now); err != nil {
		t.Fatal(err)
	}
	// The unowned case also has to insert without naming site_id at all, so the
	// same helper can seed rows for the pre-migration schema.
	if owner == nil {
		if _, err := module.database.ExecContext(ctx, `
			INSERT INTO mysql_databases (id, engine_id, name, owner_account_id, status, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, "db_1", "engine_1", name, "account_db_1", "ready", now, now); err != nil {
			t.Fatal(err)
		}
		return
	}
	if _, err := module.database.ExecContext(ctx, `
		INSERT INTO mysql_databases (id, engine_id, name, owner_account_id, site_id, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, "db_1", "engine_1", name, "account_db_1", *owner, "ready", now, now); err != nil {
		t.Fatal(err)
	}
}
