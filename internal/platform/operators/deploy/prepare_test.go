package deploy

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
)

// fakePrepareSystem is the SSH fake plus the node-preparation surface. It
// records every argv the operator builds, which is the whole point: the apt
// command line is the one place a request value could become a package name.
type fakePrepareSystem struct {
	*fakeSSHSystem
	// present is the set of binaries already on the fake node's PATH.
	present map[string]bool
	// installs maps a package to the binary it provides, so an install can be
	// made to succeed, to fail, or to succeed without providing the command.
	installs  map[string]string
	argv      [][]string
	failed    map[string]bool
	branches  map[string]bool
	branchErr error
}

func newFakePrepareSystem() *fakePrepareSystem {
	return &fakePrepareSystem{
		fakeSSHSystem: newFakeSSHSystem(),
		present:       map[string]bool{},
		installs: map[string]string{
			"git": "git", "unzip": "unzip", "rsync": "rsync", "acl": "setfacl",
			"sudo": "sudo", "php8.3-cli": "php8.3", "composer": "composer",
		},
		failed:   map[string]bool{},
		branches: map[string]bool{"8.3": true},
	}
}

func (f *fakePrepareSystem) LookPath(name string) (string, error) {
	if f.present[name] {
		return "/usr/bin/" + name, nil
	}
	return "", exec.ErrNotFound
}

func (f *fakePrepareSystem) RunPrepareCommand(_ context.Context, argv []string) ([]byte, error) {
	f.argv = append(f.argv, argv)
	if len(argv) == 5 && argv[0] == "apt-get" && argv[1] == "install" {
		pkg := argv[4]
		if f.failed[pkg] {
			return []byte("E: Unable to locate package " + pkg), errors.New("exit status 100")
		}
		if binary, ok := f.installs[pkg]; ok && binary != "" {
			f.present[binary] = true
		}
		return []byte("Setting up " + pkg), nil
	}
	if len(argv) == 2 && argv[1] == "--version" {
		return []byte(strings.TrimPrefix(argv[0], "/usr/bin/") + " version 1.2.3\nsecond line"), nil
	}
	return []byte(""), nil
}

func (f *fakePrepareSystem) PHPBranchInstalled(version string) (bool, error) {
	if f.branchErr != nil {
		return false, f.branchErr
	}
	return f.branches[version], nil
}

var validPrepareRequest = PrepareRequest{PHPVersion: "8.3"}

func prepareCalls(system *fakePrepareSystem, verb string) [][]string {
	var calls [][]string
	for _, argv := range system.argv {
		if len(argv) > 1 && argv[0] == "apt-get" && argv[1] == verb {
			calls = append(calls, argv)
		}
	}
	return calls
}

// A bare node installs every prerequisite, with the exact argv the php operator
// uses, and reports each one as installed rather than present.
func TestPrepareInstallsTheMissingTooling(t *testing.T) {
	system := newFakePrepareSystem()
	observation, err := operatorWith(system).Prepare(context.Background(), validPrepareRequest)
	if err != nil {
		t.Fatalf("Prepare() = %v, want nil", err)
	}
	want := []string{"git", "unzip", "rsync", "setfacl", "sudo", "php8.3", "composer"}
	if len(observation.Tools) != len(want) {
		t.Fatalf("tools = %+v", observation.Tools)
	}
	for index, name := range want {
		status := observation.Tools[index]
		if status.Name != name || !status.Installed || status.Action != ToolInstalled {
			t.Fatalf("tool %d = %+v, want %s installed", index, status, name)
		}
		if status.Path != "/usr/bin/"+name || status.Version == "" {
			t.Fatalf("tool %d = %+v, want a resolved path and a version", index, status)
		}
	}
	if len(observation.Warnings) != 0 {
		t.Fatalf("warnings = %v, want none", observation.Warnings)
	}
	// The index refresh happens once, before the first install, and never again.
	if updates := prepareCalls(system, "update"); len(updates) != 1 {
		t.Fatalf("apt-get update calls = %v, want exactly one", updates)
	}
	installs := prepareCalls(system, "install")
	wantPackages := []string{"git", "unzip", "rsync", "acl", "sudo", "php8.3-cli", "composer"}
	if len(installs) != len(wantPackages) {
		t.Fatalf("installs = %v", installs)
	}
	for index, pkg := range wantPackages {
		got := strings.Join(installs[index], " ")
		if got != "apt-get install -y --no-install-recommends "+pkg {
			t.Fatalf("install %d = %q", index, got)
		}
	}
}

// The whole request is one PHP version. Nothing a caller sends may reach a
// package name except through the fixed table, and the branch's own CLI package
// is the only interpolated one.
func TestPrepareCannotBeToldWhatToInstall(t *testing.T) {
	for _, version := range []string{"8.3 --allow-downgrades", "8.3;curl evil", "../../etc", "8.3-cli", "", "8.3 8.4"} {
		system := newFakePrepareSystem()
		if _, err := operatorWith(system).Prepare(context.Background(), PrepareRequest{PHPVersion: version}); err == nil {
			t.Fatalf("Prepare(%q) was accepted", version)
		}
		if len(system.argv) != 0 {
			t.Fatalf("Prepare(%q) ran %v", version, system.argv)
		}
	}
}

// A branch the node does not serve is refused before any package name is built
// from it: the shape check alone would let php9.9-cli reach apt.
func TestPrepareRefusesAnUninstalledBranch(t *testing.T) {
	system := newFakePrepareSystem()
	_, err := operatorWith(system).Prepare(context.Background(), PrepareRequest{PHPVersion: "9.9"})
	if err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("Prepare() = %v, want a not-installed refusal", err)
	}
	if len(system.argv) != 0 {
		t.Fatalf("a rejected branch still ran %v", system.argv)
	}
}

// A prepared node must be free to re-run: nothing is installed, the apt index
// is not even refreshed, and every tool reads as present.
func TestPrepareIsIdempotent(t *testing.T) {
	system := newFakePrepareSystem()
	for _, binary := range []string{"git", "unzip", "rsync", "setfacl", "sudo", "php8.3", "composer"} {
		system.present[binary] = true
	}
	observation, err := operatorWith(system).Prepare(context.Background(), validPrepareRequest)
	if err != nil {
		t.Fatalf("Prepare() = %v, want nil", err)
	}
	for _, status := range observation.Tools {
		if status.Action != ToolPresent || !status.Installed {
			t.Fatalf("tool = %+v, want present", status)
		}
	}
	if calls := len(prepareCalls(system, "update")) + len(prepareCalls(system, "install")); calls != 0 {
		t.Fatalf("a prepared node still ran apt %v", system.argv)
	}
}

// One package that will not install is a warning about that package, not a
// failed run: the other five answers are what the operator asked for.
func TestPrepareReportsAFailedInstallAsAWarning(t *testing.T) {
	system := newFakePrepareSystem()
	system.failed["composer"] = true
	observation, err := operatorWith(system).Prepare(context.Background(), validPrepareRequest)
	if err != nil {
		t.Fatalf("Prepare() = %v, want nil", err)
	}
	last := observation.Tools[len(observation.Tools)-1]
	if last.Name != "composer" || last.Installed || last.Action != ToolFailed {
		t.Fatalf("composer = %+v, want a failed install", last)
	}
	if len(observation.Warnings) != 1 || !strings.Contains(observation.Warnings[0], "composer could not be installed") {
		t.Fatalf("warnings = %v", observation.Warnings)
	}
	for _, status := range observation.Tools[:len(observation.Tools)-1] {
		if !status.Installed {
			t.Fatalf("tool = %+v, want the other tools still reported", status)
		}
	}
}

// apt exiting zero is not proof the command exists; the binary is looked up
// again, and a package that provides nothing is reported as failed.
func TestPrepareRefusesAPackageThatProvidesNoBinary(t *testing.T) {
	system := newFakePrepareSystem()
	system.installs["acl"] = ""
	observation, err := operatorWith(system).Prepare(context.Background(), validPrepareRequest)
	if err != nil {
		t.Fatalf("Prepare() = %v, want nil", err)
	}
	status := observation.Tools[3]
	if status.Name != "setfacl" || status.Installed || status.Action != ToolFailed {
		t.Fatalf("setfacl = %+v, want a failed install", status)
	}
	if len(observation.Warnings) != 1 || !strings.Contains(observation.Warnings[0], "still not on the node's PATH") {
		t.Fatalf("warnings = %v", observation.Warnings)
	}
}

// The version line is the tool's own first line, capped, and never a reason to
// fail: a tool that will not identify itself is still installed.
func TestPrepareReportsTheFirstVersionLineOnly(t *testing.T) {
	system := newFakePrepareSystem()
	system.present["git"] = true
	observation, err := operatorWith(system).Prepare(context.Background(), validPrepareRequest)
	if err != nil {
		t.Fatalf("Prepare() = %v, want nil", err)
	}
	if observation.Tools[0].Version != "git version 1.2.3" {
		t.Fatalf("git version = %q", observation.Tools[0].Version)
	}
}

// A node system without the prepare surface is a programming error, and it must
// be reported as one rather than silently reporting a node with no tooling.
func TestPrepareRefusesANodeSystemWithoutTheSurface(t *testing.T) {
	_, err := operatorWith(newFakeSSHSystem()).Prepare(context.Background(), validPrepareRequest)
	if err == nil || !strings.Contains(err.Error(), "node preparation") {
		t.Fatalf("Prepare() = %v, want an unsupported-system error", err)
	}
}
