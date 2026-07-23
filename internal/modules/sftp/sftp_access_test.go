package sftp

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/modules/sites"
	"github.com/nexa-panel/nexa-panel/internal/platform/identity"
	sftpoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/sftp"
	"github.com/nexa-panel/nexa-panel/internal/platform/persistence"
)

// fakeOperator records what the node was asked to do, so a test can assert that
// a refused change never reached it.
type fakeOperator struct{ applied []sftpoperator.Request }

func (f *fakeOperator) Apply(_ context.Context, request sftpoperator.Request) (sftpoperator.Observation, error) {
	f.applied = append(f.applied, request)
	return sftpoperator.Observation{Slug: request.Slug, Enabled: request.Enabled, Username: request.UnixUser}, nil
}

type fakeCatalog struct{ site sites.Site }

func (f fakeCatalog) Get(context.Context, string) (sites.Site, error) { return f.site, nil }

type fakeAccessPolicy struct{}

func (fakeAccessPolicy) SiteAccessible(context.Context, identity.User, string) (bool, error) {
	return true, nil
}

// fakeSSHState stands in for the deploy module's per-site SSH state.
type fakeSSHState struct{ enabled bool }

func (f *fakeSSHState) AccessEnabled(context.Context, string) (bool, error) { return f.enabled, nil }

var testSite = sites.Site{ID: "site_blog", Slug: "blog", UnixUser: "nexa_blog", RootPath: "/srv/nexa/sites/blog", PrimaryDomain: "blog.example"}

func newSFTPModule(t *testing.T) (*Module, *fakeOperator, *fakeSSHState) {
	t.Helper()
	ctx := context.Background()
	database, err := persistence.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := persistence.RunMigrations(ctx, database); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	// sftp_access.site_id is a real foreign key and cascades with the site, so the
	// row the fake catalog serves has to exist in the sites table too.
	now := time.Now().UTC()
	if _, err := database.ExecContext(ctx, `
		INSERT INTO sites (id, slug, display_name, primary_domain, php_version, unix_user, root_path, socket_path, status, deployment_mode, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		testSite.ID, testSite.Slug, testSite.Slug, testSite.PrimaryDomain, "8.4", testSite.UnixUser, testSite.RootPath,
		"/run/php/nexa-"+testSite.Slug+".sock", "active", "standard", now, now); err != nil {
		t.Fatalf("seed site: %v", err)
	}
	operator := &fakeOperator{}
	module, err := New(ctx, database, operator, fakeCatalog{site: testSite}, fakeAccessPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	state := &fakeSSHState{}
	module.UseSSHAccessState(state)
	return module, operator, state
}

// The SSH-access Match block sorts before the SFTP one and sshd keeps the first
// value of each keyword, so a jail enabled next to it is not enforced and the
// password it issues cannot authenticate. The refusal has to happen before the
// node is touched at all — this is the half of the exclusion the deploy module
// cannot enforce for us.
func TestEnableRefusesWhileSSHAccessIsEnabled(t *testing.T) {
	module, operator, state := newSFTPModule(t)
	state.enabled = true
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest("POST", "/api/v1/sites/site_blog/sftp/enable", nil)
	module.provision(recorder, request, testSite, true)
	if recorder.Code != 409 {
		t.Fatalf("status = %d, want 409", recorder.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["code"] != "ssh_access_enabled" {
		t.Fatalf("body = %+v, want an ssh_access_enabled refusal", body)
	}
	if len(operator.applied) != 0 {
		t.Fatalf("the node was driven despite the conflict: %+v", operator.applied)
	}
	enabled, err := module.AccessEnabled(context.Background(), testSite.ID)
	if err != nil {
		t.Fatal(err)
	}
	if enabled {
		t.Fatal("a refused enable was recorded as enabled")
	}
}

// A password rotation re-applies the same jail, so it carries the same conflict
// and has to be refused on the same terms.
func TestResetPasswordRefusesWhileSSHAccessIsEnabled(t *testing.T) {
	module, operator, state := newSFTPModule(t)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest("POST", "/api/v1/sites/site_blog/sftp/enable", nil)
	module.provision(recorder, request, testSite, true)
	if recorder.Code != 200 {
		t.Fatalf("enable status = %d, want 200", recorder.Code)
	}
	state.enabled = true
	recorder = httptest.NewRecorder()
	module.provision(recorder, httptest.NewRequest("POST", "/api/v1/sites/site_blog/sftp/reset-password", nil), testSite, false)
	if recorder.Code != 409 {
		t.Fatalf("reset status = %d, want 409", recorder.Code)
	}
	if len(operator.applied) != 1 {
		t.Fatalf("operator calls = %+v, want only the initial enable", operator.applied)
	}
}

// With SSH access off there is no conflict, and the ordinary enable must still
// reach the node and issue a password.
func TestEnableProvisionsWhenSSHAccessIsOff(t *testing.T) {
	module, operator, _ := newSFTPModule(t)
	recorder := httptest.NewRecorder()
	module.provision(recorder, httptest.NewRequest("POST", "/api/v1/sites/site_blog/sftp/enable", nil), testSite, true)
	if recorder.Code != 200 {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	if len(operator.applied) != 1 || !operator.applied[0].Enabled || operator.applied[0].Password == "" {
		t.Fatalf("operator calls = %+v, want one enabling call carrying a password", operator.applied)
	}
	enabled, err := module.AccessEnabled(context.Background(), testSite.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !enabled {
		t.Fatal("a successful enable was not recorded")
	}
}

// An unwired module cannot check the exclusion, and an unchecked enable can
// silently void the jail it reports. Fail closed rather than guess.
func TestEnableRefusesWhenTheSSHStateIsNotWired(t *testing.T) {
	module, operator, _ := newSFTPModule(t)
	module.ssh = nil
	recorder := httptest.NewRecorder()
	module.provision(recorder, httptest.NewRequest("POST", "/api/v1/sites/site_blog/sftp/enable", nil), testSite, true)
	if recorder.Code != 500 {
		t.Fatalf("status = %d, want 500", recorder.Code)
	}
	if len(operator.applied) != 0 {
		t.Fatalf("the node was driven without the exclusion checked: %+v", operator.applied)
	}
}
