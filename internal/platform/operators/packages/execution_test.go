package packages

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// fakeRunner records every command and simulates dpkg/apt/nvm against in-memory
// state so tests never touch the real system.
type fakeRunner struct {
	installed map[string]string // apt packages -> version
	nodes     map[string]string // node major -> full version
	calls     [][]string
}

func newFakeRunner() *fakeRunner {
	return &fakeRunner{installed: map[string]string{}, nodes: map[string]string{}}
}

func (f *fakeRunner) Run(_ context.Context, c Command) ([]byte, error) {
	record := append([]string{c.Name}, c.Args...)
	f.calls = append(f.calls, record)
	switch {
	case c.Name == "dpkg-query":
		var builder strings.Builder
		for _, arg := range c.Args {
			if version, ok := f.installed[arg]; ok {
				builder.WriteString(arg + "|" + version + "|installed\n")
			}
		}
		return []byte(builder.String()), nil
	case c.Name == "apt-get" && len(c.Args) > 0 && c.Args[0] == "install":
		for _, arg := range c.Args[1:] {
			if strings.HasPrefix(arg, "-") {
				continue
			}
			f.installed[arg] = "9.9-test"
		}
	case c.Name == "apt-get" && len(c.Args) > 0 && c.Args[0] == "purge":
		for _, arg := range c.Args[1:] {
			if strings.HasPrefix(arg, "-") {
				continue
			}
			delete(f.installed, arg)
		}
	case c.Name == "test":
		// nvm is never pre-installed in tests, so ensureNVM always runs.
		return nil, errors.New("not found")
	case c.Name == "sh" && len(c.Args) >= 2 && strings.Contains(c.Args[1], "versions/node"):
		var builder strings.Builder
		for _, full := range f.nodes {
			builder.WriteString("v" + full + "\n")
		}
		return []byte(builder.String()), nil
	case c.Name == "bash" && len(c.Args) >= 4 && c.Args[0] == "-c":
		script, major := c.Args[1], c.Args[3]
		if strings.Contains(script, "nvm install") {
			f.nodes[major] = major + ".1.0"
		} else if strings.Contains(script, "nvm uninstall") {
			delete(f.nodes, major)
		}
	}
	return nil, nil
}

func (f *fakeRunner) findCall(name, firstArg string) []string {
	for _, call := range f.calls {
		if call[0] == name && len(call) > 1 && call[1] == firstArg {
			return call
		}
	}
	return nil
}

// findInstallContaining returns the apt-get install call that includes token,
// used to locate the package-bearing install past any repo-tooling installs.
func (f *fakeRunner) findInstallContaining(token string) []string {
	for _, call := range f.calls {
		if len(call) < 2 || call[0] != "apt-get" || call[1] != "install" {
			continue
		}
		for _, arg := range call {
			if arg == token {
				return call
			}
		}
	}
	return nil
}

func newOperator(runner Runner) *HostOperator {
	return &HostOperator{runner: runner, now: func() time.Time { return time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC) }}
}

func TestNormalizeRejectsUnknownApplication(t *testing.T) {
	cases := []Change{
		{Action: ActionInstall, App: "php", Version: "9.9"},
		{Action: ActionInstall, App: "redis", Version: "7"},
		{Action: ActionInstall, App: "php", Version: "8.3; rm -rf /"},
		{Action: "package.frobnicate", App: "php", Version: "8.3"},
	}
	for _, change := range cases {
		if _, _, err := normalize(change); err == nil {
			t.Fatalf("expected rejection for %+v", change)
		}
	}
}

func TestPlanDerivesAllowlistedPackages(t *testing.T) {
	operator := newOperator(newFakeRunner())
	plan, err := operator.Plan(context.Background(), Change{Action: ActionInstall, App: "php", Version: "8.3"})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if plan.Kind != PlanKind || plan.Signature != "" {
		t.Fatalf("unexpected plan header: %+v", plan)
	}
	if plan.Packages[0] != "php8.3-fpm" {
		t.Fatalf("expected php8.3-fpm first, got %v", plan.Packages)
	}
	for _, pkg := range plan.Packages {
		if !strings.HasPrefix(pkg, "php8.3-") {
			t.Fatalf("package outside the php8.3 branch: %s", pkg)
		}
	}
}

func TestApplyInstallRunsAllowlistedAptCommand(t *testing.T) {
	runner := newFakeRunner()
	operator := newOperator(runner)
	plan, err := operator.Plan(context.Background(), Change{Action: ActionInstall, App: "php", Version: "8.3"})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	observation, err := operator.Apply(context.Background(), sign(plan))
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !observation.Installed || !observation.Verified {
		t.Fatalf("expected verified install, got %+v", observation)
	}
	install := runner.findInstallContaining("php8.3-fpm")
	if install == nil {
		t.Fatal("expected an apt-get install call carrying the php packages")
	}
	want := append([]string{"apt-get", "install", "-y", "--no-install-recommends"}, phpPackages("8.3")...)
	if strings.Join(install, " ") != strings.Join(want, " ") {
		t.Fatalf("unexpected install argv:\n got %v\nwant %v", install, want)
	}
}

func TestApplyRejectsExpiredOrTamperedPlan(t *testing.T) {
	operator := newOperator(newFakeRunner())
	plan, _ := operator.Plan(context.Background(), Change{Action: ActionInstall, App: "php", Version: "8.3"})

	expired := plan
	expired.ExpiresAt = operator.now().Add(-time.Minute)
	if _, err := operator.Apply(context.Background(), expired); err == nil {
		t.Fatal("expected expired plan rejection")
	}

	wrongKind := plan
	wrongKind.Kind = "nexa.other.v1"
	if _, err := operator.Apply(context.Background(), wrongKind); err == nil {
		t.Fatal("expected wrong-kind rejection")
	}
}

func TestApplyRejectsFingerprintDrift(t *testing.T) {
	runner := newFakeRunner()
	operator := newOperator(runner)
	plan, _ := operator.Plan(context.Background(), Change{Action: ActionInstall, App: "php", Version: "8.3"})
	// Something else installed a managed package between plan and apply.
	runner.installed["php8.1-fpm"] = "8.1-test"
	if _, err := operator.Apply(context.Background(), plan); err == nil {
		t.Fatal("expected fingerprint-drift rejection")
	}
}

func TestApplyRemoveMapsToPurge(t *testing.T) {
	runner := newFakeRunner()
	for _, pkg := range phpPackages("8.3") {
		runner.installed[pkg] = "8.3-test"
	}
	operator := newOperator(runner)
	plan, err := operator.Plan(context.Background(), Change{Action: ActionRemove, App: "php", Version: "8.3"})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	observation, err := operator.Apply(context.Background(), plan)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if observation.Installed || !observation.Verified {
		t.Fatalf("expected verified removal, got %+v", observation)
	}
	if runner.findCall("apt-get", "purge") == nil {
		t.Fatal("expected an apt-get purge call")
	}
}

func TestNodeInstallUsesNvmNotApt(t *testing.T) {
	runner := newFakeRunner()
	operator := newOperator(runner)
	plan, err := operator.Plan(context.Background(), Change{Action: ActionInstall, App: "nodejs", Version: "22"})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	observation, err := operator.Apply(context.Background(), plan)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !observation.Installed || !observation.Verified || observation.Version != "22.1.0" {
		t.Fatalf("expected verified node 22.1.0, got %+v", observation)
	}
	// It must go through nvm, never `apt-get install nodejs`.
	for _, call := range runner.calls {
		if len(call) >= 2 && call[0] == "apt-get" && call[1] == "install" {
			for _, arg := range call {
				if arg == "nodejs" {
					t.Fatalf("node install used apt for nodejs: %v", call)
				}
			}
		}
	}
	nvm := false
	for _, call := range runner.calls {
		if call[0] == "bash" && len(call) >= 4 && strings.Contains(call[2], "nvm install") && call[4] == "22" {
			nvm = true
		}
	}
	if !nvm {
		t.Fatal("expected a bash nvm install call with major 22")
	}
}

func TestNodeVersionRejectedOutsideCatalog(t *testing.T) {
	if _, _, err := normalize(Change{Action: ActionInstall, App: "nodejs", Version: "23"}); err == nil {
		t.Fatal("expected rejection for an uncatalogued Node.js version")
	}
}

// sign mimics the agent's fingerprint pass-through: Apply itself does not verify
// the signature (the agent handler does), so plans flow through unchanged here.
func sign(plan Plan) Plan { return plan }
