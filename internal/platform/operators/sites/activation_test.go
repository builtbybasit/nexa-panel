package sites

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

type fakeNodeSystem struct {
	calls []string
	fail  string
}

func (system *fakeNodeSystem) call(name string) error {
	system.calls = append(system.calls, name)
	if system.fail == name {
		return errors.New("injected failure")
	}
	return nil
}
func (s *fakeNodeSystem) PrepareSite(context.Context, Site) error { return s.call("prepare") }
func (s *fakeNodeSystem) SecureArtifacts(context.Context, Site, []Artifact) error {
	return s.call("secure")
}
func (s *fakeNodeSystem) ValidatePHP(context.Context, string) error { return s.call("validate-php") }
func (s *fakeNodeSystem) ValidateNginx(context.Context) error       { return s.call("validate-nginx") }
func (s *fakeNodeSystem) ReloadPHP(context.Context, string) error   { return s.call("reload-php") }
func (s *fakeNodeSystem) ReloadNginx(context.Context) error         { return s.call("reload-nginx") }
func (s *fakeNodeSystem) VerifyHost(context.Context, Site) error    { return s.call("verify-host") }

func testHostOperator(t *testing.T, system NodeSystem) (*HostOperator, Site) {
	t.Helper()
	root := t.TempDir()
	renderer := Renderer{
		NginxAvailableRoot: filepath.Join(root, "nginx", "available"),
		PHPConfigRoot:      filepath.Join(root, "php"),
		SiteRoot:           filepath.Join(root, "sites"), SocketRoot: filepath.Join(root, "run", "php"),
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
	wanted := []string{"prepare", "secure", "validate-php", "validate-nginx", "reload-php", "reload-nginx", "verify-host"}
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
