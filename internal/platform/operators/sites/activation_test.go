package sites

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type fakeNodeSystem struct {
	calls []string
	fail  string
}

func (s *fakeNodeSystem) call(name string) error {
	s.calls = append(s.calls, name)
	if s.fail == name {
		return errors.New("injected failure")
	}
	return nil
}
func (s *fakeNodeSystem) PrepareSite(context.Context, Site) error { return s.call("prepare") }
func (s *fakeNodeSystem) SecureArtifacts(context.Context, Site, []Artifact) error {
	return s.call("secure")
}

func (s *fakeNodeSystem) VerifyDocumentRoot(context.Context, Site) error {
	return s.call("verify-document-root")
}

func (s *fakeNodeSystem) ValidatePHP(context.Context, string) error { return s.call("validate-php") }

func (s *fakeNodeSystem) ValidateNginx(context.Context) error     { return s.call("validate-nginx") }
func (s *fakeNodeSystem) ReloadPHP(context.Context, string) error { return s.call("reload-php") }
func (s *fakeNodeSystem) ReloadNginx(context.Context) error       { return s.call("reload-nginx") }
func (s *fakeNodeSystem) VerifyHost(context.Context, Site) error  { return s.call("verify-host") }

func testHostOperator(t *testing.T, system NodeSystem) (*HostOperator, Site) {
	t.Helper()
	root := t.TempDir()
	// Every root is pinned inside the temp dir, including the conditional-artifact
	// ones: a test that enables rate limiting, basic auth, or log rotation would
	// otherwise write to (and, on retirement, delete from) the real /etc.
	renderer := Renderer{
		NginxAvailableRoot: filepath.Join(root, "nginx", "available"),
		PHPConfigRoot:      filepath.Join(root, "php"),
		SiteRoot:           filepath.Join(root, "sites"), SocketRoot: filepath.Join(root, "run", "php"),
		NginxConfDRoot:    filepath.Join(root, "nginx", "conf.d"),
		NginxIncludesRoot: filepath.Join(root, "nginx", "includes"),
		LogrotateRoot:     filepath.Join(root, "logrotate.d"),
	}
	operator, err := NewHostOperator(renderer, filepath.Join(root, "nginx", "enabled"), system)
	if err != nil {
		t.Fatal(err)
	}
	operator.now = func() time.Time { return time.Date(2026, 7, 16, 0, 0, 0, 0, time.UTC) }
	site := Site{ID: "site-1", Slug: "demo-site", PrimaryDomain: "demo.example.com", PHPVersion: "8.4", UnixUser: "nexa_demo_site", RootPath: filepath.Join(root, "sites", "demo-site"), SocketPath: filepath.Join(root, "run", "php", "nexa-demo-site.sock")}
	return operator, site
}

func TestHostOperatorAppliesVerifiesAndRollsBack(t *testing.T) {
	system := new(fakeNodeSystem)
	operator, site := testHostOperator(t, system)
	plan, err := operator.Plan(context.Background(), site)
	if err != nil {
		t.Fatal(err)
	}
	observation, err := operator.Apply(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if !observation.Active || len(observation.Artifacts) != 3 {
		t.Fatalf("observation = %+v", observation)
	}
	wanted := []string{"prepare", "secure", "verify-document-root", "validate-php", "validate-nginx", "reload-php", "reload-nginx", "verify-host"}
	if !reflect.DeepEqual(system.calls, wanted) {
		t.Fatalf("calls = %v", system.calls)
	}

	rolledBack, err := operator.Rollback(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if rolledBack.Active {
		t.Fatal("site should be disabled after rollback")
	}
	for _, artifact := range plan.Artifacts {
		if _, err := os.Stat(artifact.Path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("artifact survived rollback: %s", artifact.Path)
		}
	}
}

func TestHostOperatorRestoresBeforeStateWhenValidationFails(t *testing.T) {
	system := &fakeNodeSystem{fail: "validate-nginx"}
	operator, site := testHostOperator(t, system)
	plan, err := operator.Plan(context.Background(), site)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := operator.Apply(context.Background(), plan); err == nil {
		t.Fatal("expected validation failure")
	}
	for _, artifact := range plan.Artifacts {
		if _, err := os.Stat(artifact.Path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("artifact survived failed activation: %s", artifact.Path)
		}
	}
	enabled, err := operator.enabled(site.Slug)
	if err != nil || enabled {
		t.Fatalf("enabled after failure = %v, %v", enabled, err)
	}
}

func TestHostOperatorRejectsDriftAndTamperedArtifacts(t *testing.T) {
	operator, site := testHostOperator(t, new(fakeNodeSystem))
	plan, err := operator.Plan(context.Background(), site)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(plan.Artifacts[1].Path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plan.Artifacts[1].Path, []byte("drift"), 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := operator.Apply(context.Background(), plan); err == nil {
		t.Fatal("expected drift rejection")
	}
	plan, err = operator.Plan(context.Background(), site)
	if err != nil {
		t.Fatal(err)
	}
	plan.Artifacts[0].Path = filepath.Join(t.TempDir(), "escape")
	if _, err := operator.Apply(context.Background(), plan); err == nil {
		t.Fatal("expected tampered artifact rejection")
	}
}

func TestHostOperatorRejectsEnabledSymlinkToUnmanagedDefinition(t *testing.T) {
	operator, site := testHostOperator(t, new(fakeNodeSystem))
	if err := os.MkdirAll(operator.enabledRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(operator.enabledRoot, "nexa-"+site.Slug+".conf")
	if err := os.Symlink(filepath.Join(t.TempDir(), "attacker.conf"), path); err != nil {
		t.Fatal(err)
	}
	if _, err := operator.Plan(context.Background(), site); err == nil || !strings.Contains(err.Error(), "outside its managed definition") {
		t.Fatalf("Plan() error = %v, want an unmanaged enabled-symlink error", err)
	}
}

// Turning a conditional setting off must actually remove its file from the node.
// Apply only writes the artifacts a plan carries, so a stanza written by an
// earlier activation would otherwise survive indefinitely — and a stale logrotate
// stanza is not inert: it keeps rotating the site's logs while the panel reports
// rotation off.
func TestHostOperatorRemovesRetiredArtifactOnDisable(t *testing.T) {
	operator, site := testHostOperator(t, new(fakeNodeSystem))
	stanza := filepath.Join(operator.renderer.LogrotateRoot, "nexa-demo-site")

	site.Settings.LogRotation = LogRotation{Enabled: true, KeepFiles: 7, Frequency: "daily"}
	plan, err := operator.Plan(context.Background(), site)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := operator.Apply(context.Background(), plan); err != nil {
		t.Fatalf("apply with rotation enabled: %v", err)
	}
	if _, err := os.Stat(stanza); err != nil {
		t.Fatalf("stanza missing after enabling rotation: %v", err)
	}

	site.Settings.LogRotation = LogRotation{}
	plan, err = operator.Plan(context.Background(), site)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := operator.Apply(context.Background(), plan); err != nil {
		t.Fatalf("apply with rotation disabled: %v", err)
	}
	if _, err := os.Stat(stanza); !os.IsNotExist(err) {
		t.Fatalf("stanza survived disabling; logs would keep rotating (stat err = %v)", err)
	}
}

// A rollback has to put back what the removal took away, or disabling a setting
// and then failing verification would silently lose the previous stanza.
func TestHostOperatorRestoresRetiredArtifactOnRollback(t *testing.T) {
	operator, site := testHostOperator(t, new(fakeNodeSystem))
	stanza := filepath.Join(operator.renderer.LogrotateRoot, "nexa-demo-site")

	site.Settings.LogRotation = LogRotation{Enabled: true, KeepFiles: 7, Frequency: "daily"}
	plan, err := operator.Plan(context.Background(), site)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := operator.Apply(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(stanza)
	if err != nil {
		t.Fatal(err)
	}

	// Disable it, but fail the host verification so Apply auto-rolls back.
	site.Settings.LogRotation = LogRotation{}
	failing, _ := testHostOperator(t, &fakeNodeSystem{fail: "verify-host"})
	failing.renderer = operator.renderer
	failing.enabledRoot = operator.enabledRoot
	plan, err = failing.Plan(context.Background(), site)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := failing.Apply(context.Background(), plan); err == nil {
		t.Fatal("apply should have failed on verify-host")
	}
	restored, err := os.ReadFile(stanza)
	if err != nil {
		t.Fatalf("retired stanza was not restored by the rollback: %v", err)
	}
	if string(restored) != string(original) {
		t.Fatalf("restored stanza differs:\n got: %s\nwant: %s", restored, original)
	}
}
