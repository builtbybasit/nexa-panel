package deploy

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/modules/sites"
	"github.com/nexa-panel/nexa-panel/internal/platform/audit"
	"github.com/nexa-panel/nexa-panel/internal/platform/identity"
	"github.com/nexa-panel/nexa-panel/internal/platform/jobs"
	deployoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/deploy"
	"github.com/nexa-panel/nexa-panel/internal/platform/persistence"

	"github.com/uptrace/bun"
)

// fakeDeployOperator records what the node was asked to do, so a test can
// assert that a refused change never reached it.
type fakeDeployOperator struct {
	applied []deployoperator.SSHAccessRequest
	ensured []deployoperator.DeployKeyRequest
	tested  []deployoperator.GitHubTestRequest
	// fingerprint is what the next ensure reports back, so a test can make one
	// ensure look like a rotation by changing it.
	fingerprint string
	result      deployoperator.GitHubTestResult
	err         error
	// The shared .env as the fake node holds it, plus what it was asked to do
	// with it. envErr fails both directions.
	env        string
	envPresent bool
	envReads   []deployoperator.EnvRequest
	envWrites  []string
	envErr     error
	// What a prepare run was asked for, and what the fake node answers with.
	prepared    []deployoperator.PrepareRequest
	preparation deployoperator.PrepareObservation
	prepareErr  error
	// Every FPM-reload grant or withdrawal the node was asked for, in order.
	reloadGrants []deployoperator.FPMReloadRequest
	reloadErr    error
	// during runs while the node call is in flight, which is where a test can
	// observe what the rest of the process can still do.
	during func()
}

func (f *fakeDeployOperator) ApplySSHAccess(_ context.Context, request deployoperator.SSHAccessRequest) (deployoperator.SSHAccessObservation, error) {
	f.applied = append(f.applied, request)
	if f.during != nil {
		f.during()
	}
	if f.err != nil {
		return deployoperator.SSHAccessObservation{}, f.err
	}
	return deployoperator.SSHAccessObservation{Slug: request.Slug, Enabled: request.Enabled, Username: request.UnixUser}, nil
}

func (f *fakeDeployOperator) GenerateUserKey(context.Context, deployoperator.SSHAccessRequest) (deployoperator.GeneratedKey, error) {
	return deployoperator.GeneratedKey{PublicKey: testPublicKey, PrivateKey: "PRIVATE"}, nil
}

func (f *fakeDeployOperator) EnsureDeployKey(_ context.Context, request deployoperator.DeployKeyRequest) (deployoperator.DeployKeyObservation, error) {
	f.ensured = append(f.ensured, request)
	if f.err != nil {
		return deployoperator.DeployKeyObservation{}, f.err
	}
	fingerprint := f.fingerprint
	if fingerprint == "" {
		fingerprint = testDeployFingerprint
	}
	return deployoperator.DeployKeyObservation{
		Slug: request.Slug, Algorithm: "ssh-ed25519", PublicKey: testDeployPublicKey,
		Fingerprint: fingerprint, Path: request.RootPath + "/.ssh/id_ed25519",
		KnownHosts: true, CreatedAt: time.Unix(1700000000, 0).UTC(),
	}, nil
}

// The shared .env the fake node holds. Content is kept here so a test can
// assert that what the module wrote is what the node was handed, and that a
// refused write never reached it at all.
func (f *fakeDeployOperator) ReadSharedEnv(_ context.Context, request deployoperator.EnvRequest) (deployoperator.EnvDocument, error) {
	f.envReads = append(f.envReads, request)
	if f.envErr != nil {
		return deployoperator.EnvDocument{}, f.envErr
	}
	return f.envDocument(request), nil
}

func (f *fakeDeployOperator) WriteSharedEnv(_ context.Context, request deployoperator.EnvRequest, content string) (deployoperator.EnvDocument, error) {
	if f.envErr != nil {
		return deployoperator.EnvDocument{}, f.envErr
	}
	f.env, f.envPresent = content, true
	f.envWrites = append(f.envWrites, content)
	return f.envDocument(request), nil
}

func (f *fakeDeployOperator) envDocument(request deployoperator.EnvRequest) deployoperator.EnvDocument {
	return deployoperator.EnvDocument{
		Path: request.RootPath + "/app/shared/.env", Present: f.envPresent, Content: f.env,
		Bytes: len(f.env), SHA256: deployoperator.SharedEnvDigest(f.env),
		ModifiedAt: time.Unix(1700000000, 0).UTC(),
	}
}

// The node preparation the fake reports back, and what it was asked to
// prepare, so a test can assert the branch the site serves is the one that
// reached the node.
func (f *fakeDeployOperator) Prepare(_ context.Context, request deployoperator.PrepareRequest) (deployoperator.PrepareObservation, error) {
	f.prepared = append(f.prepared, request)
	if f.prepareErr != nil {
		return deployoperator.PrepareObservation{}, f.prepareErr
	}
	return f.preparation, nil
}

// reloadGrants records every grant and withdrawal in order, so a test can
// assert both that a deployer-mode site gained the narrow reload permission and
// that leaving deployer mode withdrew it.
func (f *fakeDeployOperator) ApplyFPMReload(_ context.Context, request deployoperator.FPMReloadRequest) (deployoperator.FPMReloadObservation, error) {
	f.reloadGrants = append(f.reloadGrants, request)
	if f.reloadErr != nil {
		return deployoperator.FPMReloadObservation{}, f.reloadErr
	}
	return deployoperator.FPMReloadObservation{
		Slug: request.Slug, Enabled: request.Enabled, Username: request.UnixUser,
		Service: "php" + request.PHPVersion + "-fpm.service",
	}, nil
}

func (f *fakeDeployOperator) TestGitHub(_ context.Context, request deployoperator.GitHubTestRequest) (deployoperator.GitHubTestResult, error) {
	f.tested = append(f.tested, request)
	if f.err != nil {
		return deployoperator.GitHubTestResult{}, f.err
	}
	return f.result, nil
}

// fakeCatalog is both halves of what the module asks of the sites module: the
// site lookup every surface needs, and the optional mode switcher the
// deployment-mode endpoint narrows it to.
type fakeCatalog struct {
	site  sites.Site
	modes []string
	job   *jobs.Job
	err   error
}

func (f *fakeCatalog) Get(context.Context, string) (sites.Site, error) { return f.site, nil }

func (f *fakeCatalog) SetDeploymentMode(_ context.Context, _, mode string, _ *string) (*jobs.Job, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.modes = append(f.modes, mode)
	f.site.DeploymentMode = mode
	return f.job, nil
}

type fakeAccessPolicy struct{}

func (fakeAccessPolicy) SiteAccessible(context.Context, identity.User, string) (bool, error) {
	return true, nil
}

type fakeSFTPState struct{ enabled bool }

func (f *fakeSFTPState) AccessEnabled(context.Context, string) (bool, error) { return f.enabled, nil }

// testPublicKey is a real ed25519 line: the module checks the blob's own wire
// header against the algorithm word, so an invented body would not parse.
const testPublicKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB deploy@laptop"

var testSite = sites.Site{ID: "site_blog", Slug: "blog", UnixUser: "nexa_blog", RootPath: "/srv/nexa/sites/blog", PrimaryDomain: "blog.example", PHPVersion: "8.3"}

func newDeployModule(t *testing.T) (*Module, *fakeDeployOperator, *fakeSFTPState, *audit.Module, *bun.DB) {
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
	auditLog, err := audit.New(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	queue, err := jobs.NewWithConfig(ctx, database, auditLog, slog.New(slog.NewTextHandler(io.Discard, nil)), jobs.Config{PollInterval: 5 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	operator := &fakeDeployOperator{}
	state := &fakeSFTPState{}
	module, err := New(ctx, database, operator, queue, &fakeCatalog{site: testSite}, fakeAccessPolicy{}, state)
	if err != nil {
		t.Fatal(err)
	}
	// The site row has to exist: site_ssh_access references it.
	if _, err := database.ExecContext(ctx, "INSERT INTO sites (id, slug, display_name, primary_domain, php_version, unix_user, root_path, socket_path, status, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		testSite.ID, testSite.Slug, "Blog", testSite.PrimaryDomain, "8.3", testSite.UnixUser, testSite.RootPath, "/run/php/blog.sock", "active", time.Now().UTC(), time.Now().UTC()); err != nil {
		t.Fatalf("seed site: %v", err)
	}
	return module, operator, state, auditLog, database
}

func findEvent(t *testing.T, auditLog *audit.Module, action string) audit.Event {
	t.Helper()
	events, err := auditLog.List(context.Background(), 50)
	if err != nil {
		t.Fatal(err)
	}
	for index := range events {
		if events[index].Action == action {
			return events[index]
		}
	}
	t.Fatalf("no %s entry in %+v", action, events)
	return audit.Event{}
}

// Enabling a login must be answerable from the audit table alone: who turned it
// on, for which site, and from where.
func TestEnableRecordsASensitiveEntry(t *testing.T) {
	module, operator, _, auditLog, _ := newDeployModule(t)
	actor := "user_admin"
	access, err := module.enable(context.Background(), testSite, &actor, "203.0.113.9")
	if err != nil {
		t.Fatalf("enable returned an error: %v", err)
	}
	if !access.Enabled || access.Shell != loginShell {
		t.Fatalf("access = %+v", access)
	}
	if len(operator.applied) != 1 || !operator.applied[0].Enabled {
		t.Fatalf("operator calls = %+v", operator.applied)
	}
	recorded := findEvent(t, auditLog, "deploy.ssh_enabled")
	if recorded.ActorUserID == nil || *recorded.ActorUserID != actor {
		t.Fatalf("actor = %v, want %s", recorded.ActorUserID, actor)
	}
	if recorded.Subject != "site:site_blog" || recorded.RemoteAddress != "203.0.113.9" {
		t.Fatalf("subject = %q, remote = %q", recorded.Subject, recorded.RemoteAddress)
	}
}

// Dropping the table stands in for an audit log that cannot be written: the
// login must not be installed, audited or not.
func TestEnableRefusesAnUnauditableChange(t *testing.T) {
	module, operator, _, _, database := newDeployModule(t)
	if _, err := database.ExecContext(context.Background(), "DROP TABLE audit_events"); err != nil {
		t.Fatal(err)
	}
	actor := "user_admin"
	if _, err := module.enable(context.Background(), testSite, &actor, ""); !errors.Is(err, audit.ErrUnauditable) {
		t.Fatalf("enable err = %v, want audit.ErrUnauditable", err)
	}
	if len(operator.applied) != 0 {
		t.Fatalf("an unauditable enable reached the node anyway: %+v", operator.applied)
	}
}

// The two features write competing sshd Match blocks for one account, so the
// refusal has to happen before the node is touched at all.
func TestEnableRefusesWhileSFTPIsEnabled(t *testing.T) {
	module, operator, state, _, _ := newDeployModule(t)
	state.enabled = true
	actor := "user_admin"
	_, err := module.enable(context.Background(), testSite, &actor, "")
	var refused *refusal
	if !errors.As(err, &refused) || refused.code != "sftp_access_enabled" {
		t.Fatalf("enable err = %v, want an sftp_access_enabled refusal", err)
	}
	if len(operator.applied) != 0 {
		t.Fatalf("the node was driven despite the conflict: %+v", operator.applied)
	}
}

// A key is only useful once it is on the node, so adding one to an enabled site
// must reinstall the whole list — and record the fingerprint, never the body.
func TestAddKeyInstallsAndAuditsTheFingerprint(t *testing.T) {
	module, operator, _, auditLog, _ := newDeployModule(t)
	actor := "user_admin"
	if _, err := module.enable(context.Background(), testSite, &actor, ""); err != nil {
		t.Fatal(err)
	}
	access, err := module.addKey(context.Background(), testSite, "Laptop", testPublicKey, &actor, "")
	if err != nil {
		t.Fatalf("addKey returned an error: %v", err)
	}
	if len(access.Keys) != 1 || access.Keys[0].Algorithm != "ssh-ed25519" {
		t.Fatalf("keys = %+v", access.Keys)
	}
	last := operator.applied[len(operator.applied)-1]
	if len(last.Keys) != 1 || last.Keys[0].Comment != "deploy@laptop" {
		t.Fatalf("installed keys = %+v", last.Keys)
	}
	recorded := findEvent(t, auditLog, "deploy.ssh_key_added")
	if recorded.Metadata["fingerprint"] != access.Keys[0].Fingerprint || recorded.Metadata["label"] != "Laptop" {
		t.Fatalf("metadata = %+v", recorded.Metadata)
	}
	for _, value := range recorded.Metadata {
		if text, ok := value.(string); ok && text == last.Keys[0].Blob {
			t.Fatalf("the key body leaked into the audit metadata: %+v", recorded.Metadata)
		}
	}
}

// A node that refuses the change must leave no trace of it in the control
// plane: the transaction the state was written in is the same one the operator
// call runs inside.
func TestEnableRollsBackWhenTheNodeRefuses(t *testing.T) {
	module, operator, _, _, _ := newDeployModule(t)
	operator.err = errors.New("sshd -t failed")
	actor := "user_admin"
	if _, err := module.enable(context.Background(), testSite, &actor, ""); err == nil {
		t.Fatal("enable accepted a change the node refused")
	}
	access, err := module.currentAccess(context.Background(), module.database, testSite)
	if err != nil {
		t.Fatal(err)
	}
	if access.Enabled {
		t.Fatalf("access was recorded as enabled after a node failure: %+v", access)
	}
}

// Disabling is a node operation, not a flag flip: the drop-in, the root-owned
// authorized-keys file and the login shell only go away when the operator is
// driven with Enabled false. A "disabled" site whose node still accepts the key
// is the worst outcome the feature can produce.
func TestDisableDrivesTheNodeToRemoveTheLogin(t *testing.T) {
	module, operator, _, auditLog, _ := newDeployModule(t)
	actor := "user_admin"
	ctx := context.Background()
	if _, err := module.enable(ctx, testSite, &actor, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := module.addKey(ctx, testSite, "Laptop", testPublicKey, &actor, ""); err != nil {
		t.Fatal(err)
	}
	access, err := module.disable(ctx, testSite, &actor, "203.0.113.9")
	if err != nil {
		t.Fatalf("disable returned an error: %v", err)
	}
	if access.Enabled || access.Shell != nologinShell {
		t.Fatalf("access = %+v, want disabled with a nologin shell", access)
	}
	last := operator.applied[len(operator.applied)-1]
	if last.Enabled {
		t.Fatalf("the node was never told to remove the login: %+v", operator.applied)
	}
	if last.Slug != testSite.Slug || last.UnixUser != testSite.UnixUser {
		t.Fatalf("disable drove the wrong account: %+v", last)
	}
	findEvent(t, auditLog, "deploy.ssh_disabled")
}

// A node that refuses the disable must leave the control panel still reporting
// the login it cannot yet prove is gone, otherwise the teardown guard would let
// the site be deleted around a live shell account.
func TestDisableKeepsTheRecordedStateWhenTheNodeRefuses(t *testing.T) {
	module, operator, _, _, _ := newDeployModule(t)
	actor := "user_admin"
	ctx := context.Background()
	if _, err := module.enable(ctx, testSite, &actor, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := module.addKey(ctx, testSite, "Laptop", testPublicKey, &actor, ""); err != nil {
		t.Fatal(err)
	}
	operator.err = errors.New("sshd -t failed")
	if _, err := module.disable(ctx, testSite, &actor, ""); err == nil {
		t.Fatal("disable reported success for a change the node refused")
	}
	access, err := module.currentAccess(ctx, module.database, testSite)
	if err != nil {
		t.Fatal(err)
	}
	if !access.Enabled || access.Shell != loginShell {
		t.Fatalf("access = %+v, want the enabled state kept after a refused disable", access)
	}
	if len(access.Keys) != 1 {
		t.Fatalf("keys = %+v, want the installed key kept after a refused disable", access.Keys)
	}
}

// A key insert the node refuses must not survive the request: the stored list is
// what every later apply sends, so a leftover row would poison them all.
func TestAddKeyRollsBackTheStoredKeyWhenTheNodeRefuses(t *testing.T) {
	module, operator, _, _, _ := newDeployModule(t)
	actor := "user_admin"
	ctx := context.Background()
	if _, err := module.enable(ctx, testSite, &actor, ""); err != nil {
		t.Fatal(err)
	}
	operator.err = errors.New("sshd -t failed")
	if _, err := module.addKey(ctx, testSite, "Laptop", testPublicKey, &actor, ""); err == nil {
		t.Fatal("addKey accepted a key the node refused")
	}
	operator.err = nil
	access, err := module.currentAccess(ctx, module.database, testSite)
	if err != nil {
		t.Fatal(err)
	}
	if len(access.Keys) != 0 {
		t.Fatalf("keys = %+v, want the refused key gone", access.Keys)
	}
	if !access.Enabled {
		t.Fatalf("access = %+v, want the pre-existing enabled state untouched", access)
	}
}

// The node's allowlist has to be enforced where the key is pasted. Stored, an
// unsupported key would fail the *next* enable — an unrelated action — and keep
// failing it until somebody works out which row is at fault.
func TestAddKeyRejectsAnAlgorithmTheNodeWillNotInstall(t *testing.T) {
	module, operator, _, _, _ := newDeployModule(t)
	actor := "user_admin"
	// A real security-key ECDSA line: well-formed, wire header matching, and
	// outside the operator's allowlist.
	const unsupported = "sk-ecdsa-sha2-nistp256@openssh.com AAAAInNrLWVjZHNhLXNoYTItbmlzdHAyNTZAb3BlbnNzaC5jb20AAAAAAAAAAA== sk@laptop"
	_, err := module.addKey(context.Background(), testSite, "Security key", unsupported, &actor, "")
	var refused *refusal
	if !errors.As(err, &refused) || refused.code != "invalid_public_key" {
		t.Fatalf("addKey err = %v, want an invalid_public_key refusal", err)
	}
	count, err := module.database.NewSelect().Model((*sshKeyModel)(nil)).Count(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("stored keys = %d, want none; an unsupported key must never reach the table", count)
	}
	if len(operator.applied) != 0 {
		t.Fatalf("the node was driven for a refused key: %+v", operator.applied)
	}
}

// The agent round trip writes /etc/ssh, runs sshd -t and reloads sshd under a
// multi-minute timeout. The control database is a single connection, so holding
// a transaction across that call would stall every other request in the panel.
func TestSSHChangesDoNotHoldTheDatabaseAcrossTheNodeCall(t *testing.T) {
	module, operator, _, _, database := newDeployModule(t)
	reachable := make(chan error, 1)
	operator.during = func() {
		// Any other writer in the process: if the change still ran inside a
		// transaction, this would block on the single pooled connection until the
		// node call returned.
		done := make(chan error, 1)
		go func() {
			_, err := database.NewInsert().Model(&sshKeyModel{
				ID: "sshkey_probe", SiteID: testSite.ID, Label: "probe", Algorithm: "ssh-ed25519",
				PublicKey: "AAAA", Fingerprint: "SHA256:probe", CreatedAt: time.Now().UTC(),
			}).Exec(context.Background())
			done <- err
		}()
		select {
		case err := <-done:
			reachable <- err
		case <-time.After(3 * time.Second):
			reachable <- errors.New("a concurrent write blocked while the node was being driven")
		}
	}
	actor := "user_admin"
	if _, err := module.enable(context.Background(), testSite, &actor, ""); err != nil {
		t.Fatal(err)
	}
	if err := <-reachable; err != nil {
		t.Fatalf("the panel could not write while the node call was in flight: %v", err)
	}
}

// A pasted line that starts with options is arbitrary code execution as the
// site account if it ever reaches authorized_keys.
func TestAddKeyRejectsAnOptionsField(t *testing.T) {
	module, operator, _, _, _ := newDeployModule(t)
	actor := "user_admin"
	_, err := module.addKey(context.Background(), testSite, "Laptop", `command="/bin/sh" `+testPublicKey, &actor, "")
	var refused *refusal
	if !errors.As(err, &refused) || refused.code != "invalid_public_key" {
		t.Fatalf("addKey err = %v, want an invalid_public_key refusal", err)
	}
	if len(operator.applied) != 0 {
		t.Fatalf("a forged key line reached the node: %+v", operator.applied)
	}
}
