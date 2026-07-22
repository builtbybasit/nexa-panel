package deploy

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeReloadSystem is the SSH fake plus the reload surface, so a test can drive
// the grant without a sudoers directory or a visudo on the machine.
type fakeReloadSystem struct {
	*fakeSSHSystem
	wrappers   map[string]string
	sudoers    map[string]string
	installErr error
}

func newFakeReloadSystem() *fakeReloadSystem {
	return &fakeReloadSystem{fakeSSHSystem: newFakeSSHSystem(), wrappers: map[string]string{}, sudoers: map[string]string{}}
}

func (f *fakeReloadSystem) WriteReloadWrapper(_ context.Context, path, content string) error {
	f.calls = append(f.calls, "writewrapper")
	f.wrappers[path] = content
	return nil
}

func (f *fakeReloadSystem) RemoveReloadWrapper(_ context.Context, path string) error {
	f.calls = append(f.calls, "removewrapper")
	delete(f.wrappers, path)
	return nil
}

func (f *fakeReloadSystem) InstallSudoers(_ context.Context, path, content string) error {
	f.calls = append(f.calls, "installsudoers")
	if f.installErr != nil {
		return f.installErr
	}
	f.sudoers[path] = content
	return nil
}

func (f *fakeReloadSystem) RemoveSudoers(_ context.Context, path string) error {
	f.calls = append(f.calls, "removesudoers")
	delete(f.sudoers, path)
	return nil
}

var validReloadRequest = FPMReloadRequest{
	Slug:       "blog",
	UnixUser:   "nexa_blog",
	RootPath:   "/srv/nexa/sites/blog",
	PHPVersion: "8.3",
	Enabled:    true,
}

const (
	blogWrapperPath = "/etc/nexa-panel/generated/deploy/nexa-fpm-reload-blog.sh"
	blogSudoersPath = "/etc/sudoers.d/nexa-deploy-blog"
)

// The two artifacts are the whole of the grant, so both are asserted verbatim:
// the wrapper reads no argument and names an absolute systemctl, and the rule
// names one absolute path with no wildcard, no ALL command, and no environment
// passthrough.
func TestApplyFPMReloadInstallsAFixedWrapperAndASinglePathRule(t *testing.T) {
	system := newFakeReloadSystem()
	observation, err := operatorWith(system).ApplyFPMReload(context.Background(), validReloadRequest)
	if err != nil {
		t.Fatalf("ApplyFPMReload() = %v, want nil", err)
	}
	wantWrapper := "#!/bin/sh\n" +
		"# Managed by Nexa Panel. Reloads the PHP-FPM master serving blog — do not edit.\n" +
		"set -eu\n" +
		"exec /usr/bin/systemctl reload php8.3-fpm.service\n"
	if system.wrappers[blogWrapperPath] != wantWrapper {
		t.Fatalf("wrapper = %q, want %q", system.wrappers[blogWrapperPath], wantWrapper)
	}
	wantRule := "# Managed by Nexa Panel. Deployer FPM reload for site blog — do not edit.\n" +
		"nexa_blog ALL=(root) NOPASSWD: " + blogWrapperPath + "\n"
	if system.sudoers[blogSudoersPath] != wantRule {
		t.Fatalf("sudo rule = %q, want %q", system.sudoers[blogSudoersPath], wantRule)
	}
	if !observation.Enabled || observation.Service != "php8.3-fpm.service" ||
		observation.WrapperPath != blogWrapperPath || observation.SudoersPath != blogSudoersPath {
		t.Fatalf("observation = %+v", observation)
	}
}

// The wrapper must be inert to anything appended after it on a sudo command
// line: it never expands "$@" or "$1", so `sudo <wrapper> --anything` runs the
// same systemctl invocation.
func TestReloadWrapperConsumesNoArguments(t *testing.T) {
	wrapper := renderReloadWrapper(validReloadRequest)
	for _, forbidden := range []string{"$@", "$*", "$1", "${", "`", "eval"} {
		if strings.Contains(wrapper, forbidden) {
			t.Fatalf("wrapper contains %q:\n%s", forbidden, wrapper)
		}
	}
}

// A grant is withdrawn rule-first: the reverse order would leave a live sudo
// entry naming a path that no longer exists.
func TestApplyFPMReloadRemovesBothArtifactsOnTeardown(t *testing.T) {
	system := newFakeReloadSystem()
	request := validReloadRequest
	if _, err := operatorWith(system).ApplyFPMReload(context.Background(), request); err != nil {
		t.Fatalf("ApplyFPMReload() = %v, want nil", err)
	}
	request.Enabled, request.PHPVersion = false, ""
	observation, err := operatorWith(system).ApplyFPMReload(context.Background(), request)
	if err != nil {
		t.Fatalf("ApplyFPMReload() = %v, want nil", err)
	}
	if len(system.wrappers) != 0 || len(system.sudoers) != 0 {
		t.Fatalf("wrappers = %v, sudoers = %v; want both gone", system.wrappers, system.sudoers)
	}
	if got := strings.Join(system.calls, ","); got != "writewrapper,installsudoers,removesudoers,removewrapper" {
		t.Fatalf("calls = %q", got)
	}
	if observation.Enabled || observation.Service != "" {
		t.Fatalf("observation = %+v, want a withdrawn grant", observation)
	}
}

// A rule sudo refuses must leave nothing behind — including the wrapper, which
// on its own is an unreachable root-owned script the panel would never clean up.
func TestApplyFPMReloadRollsBackTheWrapperWhenTheRuleIsRejected(t *testing.T) {
	system := newFakeReloadSystem()
	system.installErr = errors.New("visudo: parse error")
	if _, err := operatorWith(system).ApplyFPMReload(context.Background(), validReloadRequest); err == nil {
		t.Fatal("ApplyFPMReload() accepted a rejected rule")
	}
	if len(system.wrappers) != 0 || len(system.sudoers) != 0 {
		t.Fatalf("wrappers = %v, sudoers = %v; want nothing installed", system.wrappers, system.sudoers)
	}
}

// Nothing a caller sends may reach either file. Every one of these would be a
// privilege grant if the slug, the account, or the branch were interpolated
// without its pattern, so each is refused before the node is touched at all.
func TestApplyFPMReloadRefusesInjectionThroughTheRequest(t *testing.T) {
	for name, request := range map[string]FPMReloadRequest{
		"slug carries a second rule": {
			Slug:     "blog\nALL ALL=(ALL) NOPASSWD: ALL",
			UnixUser: "nexa_blog", RootPath: "/srv/nexa/sites/blog", PHPVersion: "8.3", Enabled: true,
		},
		"slug carries a space": {
			Slug:     "blog ALL=(ALL)",
			UnixUser: "nexa_blog", RootPath: "/srv/nexa/sites/blog", PHPVersion: "8.3", Enabled: true,
		},
		"slug traverses": {
			Slug:     "../../etc/sudoers.d/x",
			UnixUser: "nexa_blog", RootPath: "/srv/nexa/sites/blog", PHPVersion: "8.3", Enabled: true,
		},
		"slug carries a comma": {
			Slug:     "blog,root",
			UnixUser: "nexa_blog", RootPath: "/srv/nexa/sites/blog", PHPVersion: "8.3", Enabled: true,
		},
		"account is not derived from the slug": {
			Slug:     "blog",
			UnixUser: "root", RootPath: "/srv/nexa/sites/blog", PHPVersion: "8.3", Enabled: true,
		},
		"account carries its own rule": {
			Slug:     "blog",
			UnixUser: "nexa_blog ALL=(ALL) NOPASSWD: ALL", RootPath: "/srv/nexa/sites/blog", PHPVersion: "8.3", Enabled: true,
		},
		"root path is another site": {
			Slug:     "blog",
			UnixUser: "nexa_blog", RootPath: "/srv/nexa/sites/shop", PHPVersion: "8.3", Enabled: true,
		},
		"branch carries a command": {
			Slug:     "blog",
			UnixUser: "nexa_blog", RootPath: "/srv/nexa/sites/blog", PHPVersion: "8.3-fpm.service; reboot #", Enabled: true,
		},
		"branch carries a substitution": {
			Slug:     "blog",
			UnixUser: "nexa_blog", RootPath: "/srv/nexa/sites/blog", PHPVersion: "$(id)", Enabled: true,
		},
		"branch is empty": {
			Slug:     "blog",
			UnixUser: "nexa_blog", RootPath: "/srv/nexa/sites/blog", PHPVersion: "", Enabled: true,
		},
	} {
		system := newFakeReloadSystem()
		if _, err := operatorWith(system).ApplyFPMReload(context.Background(), request); err == nil {
			t.Errorf("ApplyFPMReload() accepted a request where the %s", name)
		}
		if len(system.calls) != 0 {
			t.Errorf("%s: calls = %v, want the request refused before the node was touched", name, system.calls)
		}
	}
}

// A teardown is refused on the same identity grounds as a grant: it deletes
// root-owned files by a derived path, so a forged slug must not choose them.
func TestApplyFPMReloadRefusesAForgedTeardown(t *testing.T) {
	system := newFakeReloadSystem()
	forged := FPMReloadRequest{Slug: "blog", UnixUser: "nexa_blog", RootPath: "/etc", Enabled: false}
	if _, err := operatorWith(system).ApplyFPMReload(context.Background(), forged); err == nil {
		t.Fatal("ApplyFPMReload() accepted a forged teardown")
	}
	if len(system.calls) != 0 {
		t.Fatalf("calls = %v, want nothing removed", system.calls)
	}
}

// The independent re-reading of the rendered bytes: anything wider than one
// comment and one single-path rule fails here, whatever produced it.
func TestValidateSudoersDocumentRefusesAWiderGrant(t *testing.T) {
	if err := validateSudoersDocument(renderSudoersRule(validReloadRequest)); err != nil {
		t.Fatalf("validateSudoersDocument() = %v, want nil for the managed rule", err)
	}
	header := "# Managed by Nexa Panel. Deployer FPM reload for site blog — do not edit.\n"
	for name, content := range map[string]string{
		"all commands":        header + "nexa_blog ALL=(ALL) NOPASSWD: ALL\n",
		"wildcard path":       header + "nexa_blog ALL=(root) NOPASSWD: /etc/nexa-panel/generated/deploy/*\n",
		"second rule":         header + "nexa_blog ALL=(root) NOPASSWD: " + blogWrapperPath + "\nnexa_blog ALL=(root) NOPASSWD: /bin/sh\n",
		"password argument":   header + "nexa_blog ALL=(root) NOPASSWD: " + blogWrapperPath + " -x\n",
		"another directory":   header + "nexa_blog ALL=(root) NOPASSWD: /tmp/nexa-fpm-reload-blog.sh\n",
		"defaults line":       header + "Defaults:nexa_blog !authenticate\n",
		"unterminated":        header + "nexa_blog ALL=(root) NOPASSWD: " + blogWrapperPath,
		"unmanaged header":    "# anything\nnexa_blog ALL=(root) NOPASSWD: " + blogWrapperPath + "\n",
		"continuation header": "# Managed by Nexa Panel. \\\nnexa_blog ALL=(root) NOPASSWD: " + blogWrapperPath + "\n",
	} {
		if err := validateSudoersDocument(content); err == nil {
			t.Errorf("validateSudoersDocument() accepted %s", name)
		}
	}
}

// The real node system: the wrapper lands 0555 with no temporary half of a
// write beside it, and rewriting it for a new branch replaces it in place.
func TestWriteReloadWrapperPublishesAnUnwritableScript(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "deploy")
	path := filepath.Join(directory, "nexa-fpm-reload-blog.sh")
	system := reloadHostSystem(t, nil)
	if err := system.WriteReloadWrapper(context.Background(), path, renderReloadWrapper(validReloadRequest)); err != nil {
		t.Fatalf("WriteReloadWrapper() = %v, want nil", err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o555 {
		t.Fatalf("wrapper = %v, %v; want mode 0555", info, err)
	}
	request := validReloadRequest
	request.PHPVersion = "8.4"
	if err := system.WriteReloadWrapper(context.Background(), path, renderReloadWrapper(request)); err != nil {
		t.Fatalf("WriteReloadWrapper() = %v, want nil on rewrite", err)
	}
	content, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(content), "php8.4-fpm.service") {
		t.Fatalf("wrapper = %q, %v", content, err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("read directory: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %v, want only the published wrapper", entries)
	}
}

// The rule is validated while it is still under a name sudo ignores, and only
// renamed into place once `visudo -cf` accepts it.
func TestInstallSudoersValidatesBeforeTheRename(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "nexa-deploy-blog")
	var checked []string
	system := reloadHostSystem(t, func(_ context.Context, name string, args ...string) ([]byte, error) {
		checked = append(checked, name+" "+strings.Join(args, " "))
		if _, err := os.Stat(path); err == nil {
			return nil, errors.New("the drop-in was published before it was validated")
		}
		return nil, nil
	})
	rule := renderSudoersRule(validReloadRequest)
	if err := system.InstallSudoers(context.Background(), path, rule); err != nil {
		t.Fatalf("InstallSudoers() = %v, want nil", err)
	}
	if len(checked) != 1 || !strings.HasPrefix(checked[0], "/usr/sbin/visudo -cf "+directory+"/.nexa-deploy-") {
		t.Fatalf("commands = %v", checked)
	}
	content, err := os.ReadFile(path)
	if err != nil || string(content) != rule {
		t.Fatalf("drop-in = %q, %v", content, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o440 {
		t.Fatalf("drop-in = %v, %v; want mode 0440", info, err)
	}
}

// A malformed drop-in breaks sudo for every account on the node, so a failed
// check must leave the directory exactly as it found it — no drop-in, and no
// temporary either.
func TestInstallSudoersLeavesNothingBehindWhenVisudoRejectsTheRule(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "nexa-deploy-blog")
	system := reloadHostSystem(t, func(context.Context, string, ...string) ([]byte, error) {
		return []byte(">>> /etc/sudoers.d/nexa-deploy-blog: syntax error near line 2 <<<"), errors.New("exit status 1")
	})
	err := system.InstallSudoers(context.Background(), path, renderSudoersRule(validReloadRequest))
	if err == nil || !strings.Contains(err.Error(), "syntax error") {
		t.Fatalf("InstallSudoers() = %v, want the visudo output", err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("read directory: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("entries = %v, want an untouched directory", entries)
	}
}

func TestRemoveSudoersAndWrapperTolerateAnAlreadyCleanNode(t *testing.T) {
	directory := t.TempDir()
	system := reloadHostSystem(t, nil)
	if err := system.RemoveSudoers(context.Background(), filepath.Join(directory, "nexa-deploy-blog")); err != nil {
		t.Fatalf("RemoveSudoers() = %v, want nil", err)
	}
	if err := system.RemoveReloadWrapper(context.Background(), filepath.Join(directory, "nexa-fpm-reload-blog.sh")); err != nil {
		t.Fatalf("RemoveReloadWrapper() = %v, want nil", err)
	}
}

// reloadHostSystem builds the real node system with the chown a privileged node
// would do made a no-op, so these tests exercise the publish paths without
// being root.
func reloadHostSystem(t *testing.T, command func(context.Context, string, ...string) ([]byte, error)) *SSHHostSystem {
	t.Helper()
	previous := chownPath
	chownPath = func(string, int, int) error { return nil }
	t.Cleanup(func() { chownPath = previous })
	if command == nil {
		command = func(context.Context, string, ...string) ([]byte, error) { return nil, nil }
	}
	return &SSHHostSystem{command: command}
}
