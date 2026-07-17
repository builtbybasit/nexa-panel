package packages

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeRunner records every command and simulates dpkg/apt/nvm against in-memory
// state so tests never touch the real system.
type fakeRunner struct {
	installed map[string]string // apt packages -> version
	nodes     map[string]string // node major -> full version
	// phpRepo/pgRepo are the versions the fake apt index offers, standing in for
	// the ondrej PPA and PGDG. Both include a version below the catalog floor so
	// the floor is actually exercised.
	phpRepo []string
	pgRepo  []string
	// arch is the node's dpkg architecture, which gates the database series whose
	// vendor repositories do not publish for it. Defaults to amd64 so the full
	// catalog is offered; tests override it to exercise the gate.
	arch string
	// files stands in for the node's filesystem where the operator writes apt
	// sources, so tests can assert which repository was configured.
	files map[string]string
	// repoAddsNothing simulates a vendor repository that publishes nothing for
	// this node's architecture: the sources file lands, apt reports success, and
	// the server still comes from Ubuntu's archive at an older series.
	repoAddsNothing bool
	calls           [][]string
}

func newFakeRunner() *fakeRunner {
	return &fakeRunner{
		installed: map[string]string{},
		nodes:     map[string]string{},
		phpRepo:   []string{"5.6", "7.4", "8.1", "8.3", "8.4", "8.5"},
		pgRepo:    []string{"9.6", "16", "17", "18"},
		arch:      "amd64",
		files:     map[string]string{},
	}
}

func (f *fakeRunner) Run(_ context.Context, c Command) ([]byte, error) {
	record := append([]string{c.Name}, c.Args...)
	f.calls = append(f.calls, record)
	switch {
	case c.Name == "apt-cache" && len(c.Args) > 0 && c.Args[0] == "search":
		var builder strings.Builder
		pattern := c.Args[len(c.Args)-1]
		if strings.Contains(pattern, "php") {
			for _, version := range f.phpRepo {
				builder.WriteString("php" + version + "-fpm - server-side scripting language (FPM-CGI binary)\n")
			}
		}
		if strings.Contains(pattern, "postgresql") {
			for _, version := range f.pgRepo {
				builder.WriteString("postgresql-" + version + " - object-relational SQL database\n")
			}
		}
		return []byte(builder.String()), nil
	case c.Name == "dpkg-query":
		var builder strings.Builder
		for _, arg := range c.Args {
			if version, ok := f.installed[arg]; ok {
				builder.WriteString(arg + "|" + version + "|installed\n")
			}
		}
		return []byte(builder.String()), nil
	case c.Name == "dpkg" && len(c.Args) > 0 && c.Args[0] == "--print-architecture":
		return []byte(f.arch + "\n"), nil
	case c.Name == "sh" && len(c.Args) >= 2 && strings.Contains(c.Args[1], "/etc/os-release"):
		return []byte("noble"), nil
	case c.Name == "sh" && len(c.Args) >= 10 && strings.Contains(c.Args[1], "want_fpr"):
		f.addRepo(c.Args[6], c.Args[8], c.Args[9])
	case c.Name == "rm" && len(c.Args) == 2 && c.Args[0] == "-f":
		f.removeRepo(c.Args[1])
	case c.Name == "apt-get" && len(c.Args) > 0 && c.Args[0] == "install":
		for _, arg := range c.Args[1:] {
			if strings.HasPrefix(arg, "-") {
				continue
			}
			if version, ok := f.databaseVersion(arg); ok {
				f.installed[arg] = version
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

// addRepo/removeRepo record which series the operator pointed apt at. The fake
// keeps only the series because that is what apt's resolution turns on.
func (f *fakeRunner) addRepo(repoURL, component, listPath string) {
	if f.repoAddsNothing {
		return
	}
	f.files[listPath] = "deb " + repoURL + " " + component
}

func (f *fakeRunner) removeRepo(listPath string) { delete(f.files, listPath) }

// databaseVersion models apt resolving a database server package against
// whatever repository is configured right now: the vendor series if its sources
// file is present, otherwise the version Ubuntu's own archive carries. That
// fallback is the real hazard this operator has to catch — on an architecture
// the vendor does not publish, apt quietly satisfies the request from the base
// repo and reports success.
func (f *fakeRunner) databaseVersion(pkg string) (string, bool) {
	switch pkg {
	case "mysql-server":
		return configuredSeries(f.files, "mysql", "8.0") + ".10-1ubuntu24.04", true
	case "mariadb-server":
		return "1:" + configuredSeries(f.files, "mariadb", "10.11") + ".12+maria~ubu2404", true
	}
	return "", false
}

// configuredSeries reads the series back out of the sources line the operator
// wrote — MySQL puts it in the component, MariaDB in the URL path.
func configuredSeries(files map[string]string, app, base string) string {
	line, ok := files[dbVendors[app].listPath]
	if !ok {
		return base
	}
	if app == "mysql" {
		fields := strings.Fields(line)
		return strings.TrimSuffix(strings.TrimPrefix(fields[len(fields)-1], "mysql-"), "-lts")
	}
	for _, part := range strings.Split(line, "/") {
		if dbVersionPattern.MatchString(part) {
			return part
		}
	}
	return base
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

// fakeNodeIndex serves a cut-down nodejs.org release index: LTS lines 18/20/22/24
// plus non-LTS 23/25/26, so both the LTS filter and the newest-N window matter.
const fakeNodeIndex = `[
	{"version":"v26.5.0","lts":false},
	{"version":"v25.1.0","lts":false},
	{"version":"v24.4.0","lts":"Krypton"},
	{"version":"v23.9.0","lts":false},
	{"version":"v22.11.0","lts":"Jod"},
	{"version":"v20.18.0","lts":"Iron"},
	{"version":"v18.20.0","lts":"Hydrogen"}
]`

func newOperator(t *testing.T, runner Runner) *HostOperator {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(fakeNodeIndex))
	}))
	t.Cleanup(server.Close)
	return newOperatorWithIndex(t, runner, server.URL)
}

func newOperatorWithIndex(t *testing.T, runner Runner, indexURL string) *HostOperator {
	t.Helper()
	return &HostOperator{
		runner:       runner,
		now:          func() time.Time { return time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC) },
		client:       &http.Client{Timeout: 5 * time.Second},
		nodeIndexURL: indexURL,
		catalogTTL:   time.Minute,
	}
}

func TestNormalizeRejectsUnknownApplication(t *testing.T) {
	operator := newOperator(t, newFakeRunner())
	cases := []Change{
		{Action: ActionInstall, App: "php", Version: "9.9"},
		{Action: ActionInstall, App: "redis", Version: "7"},
		{Action: ActionInstall, App: "php", Version: "8.3; rm -rf /"},
		{Action: "package.frobnicate", App: "php", Version: "8.3"},
	}
	for _, change := range cases {
		if _, _, err := operator.normalize(context.Background(), change); err == nil {
			t.Fatalf("expected rejection for %+v", change)
		}
	}
}

func TestPlanDerivesAllowlistedPackages(t *testing.T) {
	operator := newOperator(t, newFakeRunner())
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
	operator := newOperator(t, runner)
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
	operator := newOperator(t, newFakeRunner())
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
	operator := newOperator(t, runner)
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
	operator := newOperator(t, runner)
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
	operator := newOperator(t, runner)
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

// Node.js 23 is published but never an LTS line, so it must stay uninstallable
// even though enumeration now decides the version list.
func TestNodeVersionRejectedOutsideCatalog(t *testing.T) {
	operator := newOperator(t, newFakeRunner())
	if _, _, err := operator.normalize(context.Background(), Change{Action: ActionInstall, App: "nodejs", Version: "23"}); err == nil {
		t.Fatal("expected rejection for an uncatalogued Node.js version")
	}
}

// sign mimics the agent's fingerprint pass-through: Apply itself does not verify
// the signature (the agent handler does), so plans flow through unchanged here.
func sign(plan Plan) Plan { return plan }
