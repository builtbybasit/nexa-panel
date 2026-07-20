package php

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeRunner answers commands by name so tests can drive the operator without a
// live node. Each responder receives the full command and returns canned output.
type fakeRunner struct {
	aptCacheSearch string
	dpkgInstalled  map[string]bool
	settingsJSON   string
	calls          []Command
	failReload     bool
}

func (f *fakeRunner) Run(_ context.Context, command Command) ([]byte, error) {
	f.calls = append(f.calls, command)
	switch command.Name {
	case "apt-cache":
		return []byte(f.aptCacheSearch), nil
	case "dpkg-query":
		var builder strings.Builder
		for _, arg := range command.Args {
			if strings.HasPrefix(arg, "php") {
				status := "config-files"
				if f.dpkgInstalled[arg] {
					status = "installed"
				}
				builder.WriteString(arg + "|" + status + "\n")
			}
		}
		return []byte(builder.String()), nil
	case "php8.3":
		return []byte(f.settingsJSON), nil
	case "apt-get":
		return []byte("done"), nil
	case "systemctl":
		if f.failReload {
			return []byte("unit not found"), os.ErrNotExist
		}
		return []byte("ok"), nil
	default:
		return nil, os.ErrNotExist
	}
}

// newOperator builds a HostOperator rooted at a temp tree containing a single
// installed PHP 8.3 branch (an FPM conf.d directory is what marks it installed).
func newOperator(t *testing.T, runner Runner) (*HostOperator, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "8.3", "fpm", "conf.d"), 0o755); err != nil {
		t.Fatal(err)
	}
	operator, err := NewHostOperator(runner, HostConfig{ConfigRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	return operator, root
}

func TestVersionsDiscoversInstalledBranch(t *testing.T) {
	operator, _ := newOperator(t, &fakeRunner{})
	versions, err := operator.Versions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != 1 || versions[0] != "8.3" {
		t.Fatalf("versions = %v, want [8.3]", versions)
	}
}

func TestExtensionsEnumerateAndExcludeRuntimePackages(t *testing.T) {
	runner := &fakeRunner{
		aptCacheSearch: "php8.3-redis - Redis\nphp8.3-xdebug - Xdebug\nphp8.3-fpm - server\nphp8.3-cli - cli\nphp8.3-all-dev - meta\n",
		dpkgInstalled:  map[string]bool{"php8.3-redis": true},
	}
	operator, _ := newOperator(t, runner)
	extensions, err := operator.Extensions(context.Background(), "8.3")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, extension := range extensions {
		got[extension.Name] = extension.Installed
	}
	if _, ok := got["fpm"]; ok {
		t.Fatal("fpm is a runtime package and must not be listed as an extension")
	}
	if _, ok := got["all"]; ok {
		t.Fatal("multi-hyphen packages must not enumerate")
	}
	if !got["redis"] {
		t.Fatal("php8.3-redis is installed and must report installed")
	}
	if installed, ok := got["xdebug"]; !ok || installed {
		t.Fatalf("xdebug must be listed and not installed, got present=%v installed=%v", ok, installed)
	}
}

const sampleSettings = `[{"name":"memory_limit","value":"128M","module":"core","access":7},{"name":"allow_url_fopen","value":"1","module":"core","access":4}]`

func TestSettingsParseAndManagedFlag(t *testing.T) {
	runner := &fakeRunner{settingsJSON: sampleSettings}
	operator, root := newOperator(t, runner)
	// Pre-seed the managed drop-in so memory_limit reads back as managed.
	dropIn := filepath.Join(root, "8.3", "fpm", "conf.d", managedDropInName)
	if err := os.WriteFile(dropIn, []byte("; Managed by Nexa Panel.\nmemory_limit = 256M\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	directives, err := operator.Settings(context.Background(), "8.3")
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]Directive{}
	for _, directive := range directives {
		byName[directive.Name] = directive
	}
	if !byName["memory_limit"].Managed {
		t.Fatal("memory_limit is in the drop-in and must be flagged managed")
	}
	if byName["allow_url_fopen"].Managed {
		t.Fatal("allow_url_fopen is not in the drop-in and must not be flagged managed")
	}
	if byName["allow_url_fopen"].Access != "PHP_INI_SYSTEM" {
		t.Fatalf("access = %q, want PHP_INI_SYSTEM", byName["allow_url_fopen"].Access)
	}
}

func TestSaveSettingsWritesAndResetsDropIn(t *testing.T) {
	runner := &fakeRunner{settingsJSON: sampleSettings}
	operator, root := newOperator(t, runner)
	ctx := context.Background()
	dropIn := filepath.Join(root, "8.3", "fpm", "conf.d", managedDropInName)

	// Save a value, then apply it through the signed-plan path.
	plan, err := operator.Plan(ctx, Change{Action: ActionSaveSettings, Version: "8.3", Set: map[string]string{"memory_limit": "512M"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := operator.Apply(ctx, plan); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(dropIn)
	if err != nil {
		t.Fatalf("drop-in not written: %v", err)
	}
	if !strings.Contains(string(content), "memory_limit = 512M") {
		t.Fatalf("drop-in = %q, want memory_limit = 512M", content)
	}

	// Reset it and the drop-in should disappear (no overrides left).
	plan, err = operator.Plan(ctx, Change{Action: ActionSaveSettings, Version: "8.3", Reset: []string{"memory_limit"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := operator.Apply(ctx, plan); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dropIn); !os.IsNotExist(err) {
		t.Fatalf("drop-in should be removed after resetting its only override, stat err = %v", err)
	}
}

func TestNormalizeRejectsUnknownDirectiveAndInjection(t *testing.T) {
	runner := &fakeRunner{settingsJSON: sampleSettings}
	operator, _ := newOperator(t, runner)
	ctx := context.Background()

	if _, err := operator.Plan(ctx, Change{Action: ActionSaveSettings, Version: "8.3", Set: map[string]string{"not_a_directive": "1"}}); err == nil {
		t.Fatal("an unknown directive must be rejected")
	}
	if _, err := operator.Plan(ctx, Change{Action: ActionSaveSettings, Version: "8.3", Set: map[string]string{"memory_limit": "256M\nevil = 1"}}); err == nil {
		t.Fatal("a multi-line value must be rejected")
	}
	if _, err := operator.Plan(ctx, Change{Action: ActionSaveSettings, Version: "8.9", Set: map[string]string{"memory_limit": "256M"}}); err == nil {
		t.Fatal("an uninstalled branch must be rejected")
	}
}

type fakeOwner struct{ chowned []string }

func (f *fakeOwner) Chown(path, unixUser string) error {
	f.chowned = append(f.chowned, path+"|"+unixUser)
	return nil
}

// newSiteOperator builds an operator with both an installed 8.3 branch and a
// site tree (blog) under a temp sites root, with a recording ownership fake.
func newSiteOperator(t *testing.T) (*HostOperator, *fakeOwner, SiteScope) {
	t.Helper()
	configRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(configRoot, "8.3", "fpm", "conf.d"), 0o755); err != nil {
		t.Fatal(err)
	}
	sitesRoot := t.TempDir()
	rootPath := filepath.Join(sitesRoot, "blog")
	if err := os.MkdirAll(filepath.Join(rootPath, "public"), 0o750); err != nil {
		t.Fatal(err)
	}
	owner := &fakeOwner{}
	operator, err := NewHostOperator(&fakeRunner{settingsJSON: sampleSettings}, HostConfig{ConfigRoot: configRoot, SitesRoot: sitesRoot, Owner: owner})
	if err != nil {
		t.Fatal(err)
	}
	return operator, owner, SiteScope{Slug: "blog", Version: "8.3", RootPath: rootPath, UnixUser: "nexa_blog"}
}

func TestSiteSettingsOnlyOffersUserIniEditableDirectives(t *testing.T) {
	operator, _, scope := newSiteOperator(t)
	directives, err := operator.SiteSettings(context.Background(), scope)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, directive := range directives {
		names[directive.Name] = true
	}
	if !names["memory_limit"] {
		t.Fatal("memory_limit (PHP_INI_ALL) must be per-site editable")
	}
	if names["allow_url_fopen"] {
		t.Fatal("allow_url_fopen (PHP_INI_SYSTEM) cannot be overridden per-site and must be excluded")
	}
}

func TestSaveSiteSettingsWritesUserIniWithOwnership(t *testing.T) {
	operator, owner, scope := newSiteOperator(t)
	ctx := context.Background()
	userIni := filepath.Join(scope.RootPath, "public", ".user.ini")

	plan, err := operator.Plan(ctx, Change{Action: ActionSaveSiteSettings, Version: "8.3", Site: &scope, Set: map[string]string{"memory_limit": "256M"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := operator.Apply(ctx, plan); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(userIni)
	if err != nil {
		t.Fatalf(".user.ini not written: %v", err)
	}
	if !strings.Contains(string(content), "memory_limit = 256M") {
		t.Fatalf(".user.ini = %q, want memory_limit = 256M", content)
	}
	if len(owner.chowned) == 0 || !strings.HasSuffix(owner.chowned[0], "|nexa_blog") {
		t.Fatalf("expected .user.ini chowned to nexa_blog, got %v", owner.chowned)
	}

	// Resetting the only override removes the file entirely.
	plan, err = operator.Plan(ctx, Change{Action: ActionSaveSiteSettings, Version: "8.3", Site: &scope, Reset: []string{"memory_limit"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := operator.Apply(ctx, plan); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(userIni); !os.IsNotExist(err) {
		t.Fatalf(".user.ini should be removed after clearing its only override, stat err = %v", err)
	}
}

func TestSiteScopeRejectsSpoofedPathAndUser(t *testing.T) {
	operator, _, scope := newSiteOperator(t)
	ctx := context.Background()

	escaped := scope
	escaped.RootPath = "/etc"
	if _, err := operator.SiteSettings(ctx, escaped); err == nil {
		t.Fatal("a root path outside the sites root must be rejected")
	}

	spoofed := scope
	spoofed.UnixUser = "root"
	if _, err := operator.SiteSettings(ctx, spoofed); err == nil {
		t.Fatal("a unix user that does not match the slug must be rejected")
	}

	// A SYSTEM-only directive cannot be set per-site even by name.
	if _, err := operator.Plan(ctx, Change{Action: ActionSaveSiteSettings, Version: "8.3", Site: &scope, Set: map[string]string{"allow_url_fopen": "1"}}); err == nil {
		t.Fatal("a non-.user.ini directive must be rejected for per-site override")
	}
}

func TestApplyRejectsDriftedFingerprint(t *testing.T) {
	runner := &fakeRunner{settingsJSON: sampleSettings}
	operator, root := newOperator(t, runner)
	ctx := context.Background()
	plan, err := operator.Plan(ctx, Change{Action: ActionSaveSettings, Version: "8.3", Set: map[string]string{"memory_limit": "512M"}})
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a concurrent change to the drop-in after planning.
	dropIn := filepath.Join(root, "8.3", "fpm", "conf.d", managedDropInName)
	if err := os.WriteFile(dropIn, []byte("post_max_size = 64M\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := operator.Apply(ctx, plan); err == nil {
		t.Fatal("apply must reject a plan whose observed state drifted")
	}
}
