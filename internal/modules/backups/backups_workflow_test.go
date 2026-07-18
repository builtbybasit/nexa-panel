package backups

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/platform/audit"
	"github.com/nexa-panel/nexa-panel/internal/platform/jobs"
	backupoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/backups"
	"github.com/nexa-panel/nexa-panel/internal/platform/persistence"
	"github.com/nexa-panel/nexa-panel/internal/platform/secrets"
)

type fakeBackupOperator struct {
	lastAccount backupoperator.Account
	lastRun     backupoperator.RunRequest
	lastRestore backupoperator.RestoreRequest
	lastDelete  backupoperator.DeleteRequest
	installed   []backupoperator.ScheduleSpec
	removed     []string
	result      backupoperator.TestResult
	manifest    backupoperator.Manifest
	err         error
}

func (f *fakeBackupOperator) TestAccount(_ context.Context, account backupoperator.Account) (backupoperator.TestResult, error) {
	f.lastAccount = account
	return f.result, f.err
}

func (f *fakeBackupOperator) Run(_ context.Context, request backupoperator.RunRequest) (backupoperator.Manifest, error) {
	f.lastRun = request
	if f.err != nil {
		return backupoperator.Manifest{}, f.err
	}
	manifest := f.manifest
	if manifest.CopyName == "" {
		manifest.CopyName = request.CopyName
	}
	return manifest, nil
}

func (f *fakeBackupOperator) Restore(_ context.Context, request backupoperator.RestoreRequest) error {
	f.lastRestore = request
	return f.err
}

func (f *fakeBackupOperator) DeleteCopy(_ context.Context, request backupoperator.DeleteRequest) error {
	f.lastDelete = request
	return f.err
}

func (f *fakeBackupOperator) InstallSchedule(_ context.Context, spec backupoperator.ScheduleSpec) error {
	f.installed = append(f.installed, spec)
	return nil
}

func (f *fakeBackupOperator) RemoveSchedule(_ context.Context, planID string) error {
	f.removed = append(f.removed, planID)
	return nil
}

func newTestModule(t *testing.T) (*Module, *fakeBackupOperator) {
	t.Helper()
	ctx := context.Background()
	store, err := persistence.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	cipher, err := secrets.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	auditLog, err := audit.New(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	queue, err := jobs.NewWithConfig(ctx, store, auditLog, slog.New(slog.NewTextHandler(io.Discard, nil)), jobs.Config{PollInterval: 5 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	operator := &fakeBackupOperator{result: backupoperator.TestResult{OK: true, Message: "Connection succeeded."}}
	module, err := New(ctx, Dependencies{Database: store, Jobs: queue, Cipher: cipher, Operator: operator})
	if err != nil {
		t.Fatal(err)
	}
	return module, operator
}

// The core Phase 1 guarantee: an account's secret is encrypted at rest and
// never appears in the JSON view, yet is transparently merged back into the
// rclone params the operator receives.
func TestAccountSecretRoundTrip(t *testing.T) {
	ctx := context.Background()
	module, operator := newTestModule(t)

	account, err := module.CreateAccount(ctx, AccountRequest{
		Name: "minio", Type: TypeS3, Path: "backups/nightly",
		Config: map[string]string{"provider": "Minio", "endpoint": "https://10.0.0.5:9000", "access_key_id": "admin"},
		Secret: map[string]string{"secret_access_key": "s3cr3t"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !account.HasSecret {
		t.Fatal("account should report a stored secret")
	}
	if _, leaked := account.Config["secret_access_key"]; leaked {
		t.Fatal("secret leaked into the account's public config")
	}
	if account.Config["provider"] != "Minio" {
		t.Fatalf("config not preserved: %v", account.Config)
	}

	model, err := module.getAccountModel(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if model.SecretCiphertext == nil || *model.SecretCiphertext == "" {
		t.Fatal("secret was not persisted as ciphertext")
	}
	if _, err := module.TestAccount(ctx, *model); err != nil {
		t.Fatalf("test: %v", err)
	}
	if got := operator.lastAccount.Params["secret_access_key"]; got != "s3cr3t" {
		t.Fatalf("secret not decrypted into operator params, got %q", got)
	}
	if got := operator.lastAccount.Params["provider"]; got != "Minio" {
		t.Fatalf("config not merged into operator params, got %q", got)
	}
}

// Updating with an empty Secret keeps the stored one; a non-empty Secret
// replaces it.
func TestUpdateSecretRetention(t *testing.T) {
	ctx := context.Background()
	module, operator := newTestModule(t)
	created, err := module.CreateAccount(ctx, AccountRequest{
		Name: "sftp", Type: TypeSFTP, Path: "backups",
		Config: map[string]string{"host": "10.0.0.9", "user": "backup"},
		Secret: map[string]string{"pass": "original"},
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := module.UpdateAccount(ctx, created.ID, AccountRequest{
		Name: "sftp", Type: TypeSFTP, Path: "backups",
		Config: map[string]string{"host": "10.0.0.9", "user": "backup"},
	}); err != nil {
		t.Fatalf("update without secret: %v", err)
	}
	model, _ := module.getAccountModel(ctx, created.ID)
	if _, err := module.TestAccount(ctx, *model); err != nil {
		t.Fatal(err)
	}
	if got := operator.lastAccount.Params["pass"]; got != "original" {
		t.Fatalf("secret should have been retained, got %q", got)
	}

	if _, err := module.UpdateAccount(ctx, created.ID, AccountRequest{
		Name: "sftp", Type: TypeSFTP, Path: "backups",
		Config: map[string]string{"host": "10.0.0.9", "user": "backup"},
		Secret: map[string]string{"pass": "rotated"},
	}); err != nil {
		t.Fatalf("update with secret: %v", err)
	}
	model, _ = module.getAccountModel(ctx, created.ID)
	if _, err := module.TestAccount(ctx, *model); err != nil {
		t.Fatal(err)
	}
	if got := operator.lastAccount.Params["pass"]; got != "rotated" {
		t.Fatalf("secret should have been replaced, got %q", got)
	}
}

func seedAccount(t *testing.T, module *Module) Account {
	t.Helper()
	account, err := module.CreateAccount(context.Background(), AccountRequest{Name: "local", Type: TypeLocal, Path: "/srv/backups"})
	if err != nil {
		t.Fatal(err)
	}
	return account
}

func TestPlanCreateAndToggle(t *testing.T) {
	ctx := context.Background()
	module, _ := newTestModule(t)
	account := seedAccount(t, module)

	plan, err := module.CreatePlan(ctx, PlanRequest{
		Name: "nightly", AccountID: account.ID, CopiesLimit: 7,
		SiteIDs: []string{"site_1"}, DatabaseIDs: []string{"postgres:db_1"},
		Schedule: "0 3 * * *", Enabled: true,
	})
	if err != nil {
		t.Fatalf("create plan: %v", err)
	}
	if plan.CopiesLimit != 7 || len(plan.SiteIDs) != 1 || plan.DatabaseIDs[0] != "postgres:db_1" || !plan.Enabled {
		t.Fatalf("plan not persisted faithfully: %+v", plan)
	}

	model := new(planModel)
	if err := module.database.NewSelect().Model(model).Where("id = ?", plan.ID).Scan(ctx); err != nil {
		t.Fatal(err)
	}
	if !model.Enabled {
		t.Fatal("expected enabled")
	}
}

func TestPlanValidation(t *testing.T) {
	ctx := context.Background()
	module, _ := newTestModule(t)
	account := seedAccount(t, module)
	cases := []PlanRequest{
		{Name: "", AccountID: account.ID, CopiesLimit: 5, SiteIDs: []string{"s"}, Schedule: "0 3 * * *"},
		{Name: "no-target", AccountID: account.ID, CopiesLimit: 5, Schedule: "0 3 * * *"},
		{Name: "bad-limit", AccountID: account.ID, CopiesLimit: 0, SiteIDs: []string{"s"}, Schedule: "0 3 * * *"},
		{Name: "bad-cron", AccountID: account.ID, CopiesLimit: 5, SiteIDs: []string{"s"}, Schedule: "3 * * *"},
		{Name: "missing-account", AccountID: "bkacct_missing", CopiesLimit: 5, SiteIDs: []string{"s"}, Schedule: "0 3 * * *"},
	}
	for _, request := range cases {
		if _, err := module.CreatePlan(ctx, request); err == nil {
			t.Fatalf("expected error for %+v", request)
		}
	}
}

func TestDeleteAccountInUse(t *testing.T) {
	ctx := context.Background()
	module, _ := newTestModule(t)
	account := seedAccount(t, module)
	if _, err := module.CreatePlan(ctx, PlanRequest{
		Name: "p", AccountID: account.ID, CopiesLimit: 3, SiteIDs: []string{"s"}, Schedule: "0 3 * * *",
	}); err != nil {
		t.Fatal(err)
	}
	if err := module.DeleteAccount(ctx, account.ID); err != errAccountInUse {
		t.Fatalf("expected errAccountInUse, got %v", err)
	}
}

type fakeSiteResolver struct{}

func (fakeSiteResolver) BackupSite(_ context.Context, _ string) (backupoperator.SiteTarget, error) {
	return backupoperator.SiteTarget{Slug: "blog", RootPath: "/srv/nexa/sites/blog", UnixUser: "nexa_blog"}, nil
}

func TestRunJobResolvesTargetsAndRecordsCopy(t *testing.T) {
	ctx := context.Background()
	module, operator := newTestModule(t)
	module.sites = fakeSiteResolver{}
	operator.manifest = backupoperator.Manifest{
		RemotePath: "/srv/backups/x", SizeBytes: 1234,
		Entries: []backupoperator.CopyEntry{{Name: "site-blog.tar.gz", SizeBytes: 1234}},
	}
	account := seedAccount(t, module)
	plan, err := module.CreatePlan(ctx, PlanRequest{
		Name: "p", AccountID: account.ID, CopiesLimit: 5, SiteIDs: []string{"site_1"}, Schedule: "0 3 * * *", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	payload, _ := json.Marshal(runPayload{PlanID: plan.ID})
	if _, err := module.runJob(ctx, payload, func(int, string) error { return nil }); err != nil {
		t.Fatalf("runJob: %v", err)
	}
	if len(operator.lastRun.Sites) != 1 || operator.lastRun.Sites[0].Slug != "blog" {
		t.Fatalf("site not resolved into run request: %+v", operator.lastRun.Sites)
	}
	if operator.lastRun.PlanID != plan.ID || operator.lastRun.Limit != 5 {
		t.Fatalf("run request not built from plan: %+v", operator.lastRun)
	}

	var copies []copyModel
	if err := module.database.NewSelect().Model(&copies).Scan(ctx); err != nil {
		t.Fatal(err)
	}
	if len(copies) != 1 || copies[0].SizeBytes != 1234 || copies[0].Status != "succeeded" {
		t.Fatalf("copy not recorded: %+v", copies)
	}
}

func TestRunJobFailsWhenSiteResolverMissing(t *testing.T) {
	ctx := context.Background()
	module, _ := newTestModule(t) // no site resolver injected
	account := seedAccount(t, module)
	plan, err := module.CreatePlan(ctx, PlanRequest{
		Name: "p", AccountID: account.ID, CopiesLimit: 5, SiteIDs: []string{"site_1"}, Schedule: "0 3 * * *", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(runPayload{PlanID: plan.ID})
	if _, err := module.runJob(ctx, payload, func(int, string) error { return nil }); err == nil {
		t.Fatal("expected runJob to fail when site backups are unavailable")
	}
}

func TestRestoreJobResolvesDestinations(t *testing.T) {
	ctx := context.Background()
	module, operator := newTestModule(t)
	module.sites = fakeSiteResolver{}
	account := seedAccount(t, module)
	plan, err := module.CreatePlan(ctx, PlanRequest{
		Name: "p", AccountID: account.ID, CopiesLimit: 5, SiteIDs: []string{"site_1"}, Schedule: "0 3 * * *", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	record := &copyModel{
		ID: "bkcopy_1", PlanID: plan.ID, AccountID: account.ID, CopyName: "2026-01-04_120000",
		RemotePath: "/srv/backups/x", SizeBytes: 1, EntriesJSON: `[{"name":"site-blog.tar.gz","sizeBytes":1}]`,
		Status: "succeeded", CreatedAt: time.Now().UTC(),
	}
	if _, err := module.database.NewInsert().Model(record).Exec(ctx); err != nil {
		t.Fatal(err)
	}

	payload, _ := json.Marshal(restorePayload{
		CopyID: "bkcopy_1",
		Sites:  []siteSelection{{Entry: "site-blog.tar.gz", SiteID: "site_1", Clear: true}},
	})
	if _, err := module.restoreJob(ctx, payload, func(int, string) error { return nil }); err != nil {
		t.Fatalf("restoreJob: %v", err)
	}
	if operator.lastRestore.CopyName != "2026-01-04_120000" {
		t.Fatalf("restore copy name = %q", operator.lastRestore.CopyName)
	}
	if len(operator.lastRestore.Sites) != 1 || operator.lastRestore.Sites[0].Target.Slug != "blog" || !operator.lastRestore.Sites[0].Clear {
		t.Fatalf("site restore target not resolved: %+v", operator.lastRestore.Sites)
	}
}

func TestCreateValidation(t *testing.T) {
	ctx := context.Background()
	module, _ := newTestModule(t)
	cases := []AccountRequest{
		{Name: "", Type: TypeLocal, Path: "/srv/backups"},
		{Name: "bad-type", Type: "dropbox", Path: "/srv/backups"},
		{Name: "relative-local", Type: TypeLocal, Path: "srv/backups"},
		{Name: "no-bucket", Type: TypeS3, Path: ""},
	}
	for _, request := range cases {
		if _, err := module.CreateAccount(ctx, request); err == nil {
			t.Fatalf("expected validation error for %+v", request)
		}
	}
}
