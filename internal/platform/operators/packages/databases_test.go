package packages

import (
	"context"
	"strings"
	"testing"
)

// installedNames returns the identities Discover reports as installed.
func installedNames(t *testing.T, operator *HostOperator) map[string]string {
	t.Helper()
	items, err := operator.Discover(context.Background())
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	result := map[string]string{}
	for _, item := range items {
		if item.Installed {
			result[item.Name] = item.Version
		}
	}
	return result
}

func planFor(t *testing.T, operator *HostOperator, app, version string) Plan {
	t.Helper()
	plan, err := operator.Plan(context.Background(), Change{Action: ActionInstall, App: app, Version: version})
	if err != nil {
		t.Fatalf("plan %s %s: %v", app, version, err)
	}
	return plan
}

func installDB(t *testing.T, operator *HostOperator, app, version string) (Observation, error) {
	t.Helper()
	return operator.Apply(context.Background(), planFor(t, operator, app, version))
}

// The defect this whole design exists to avoid. Every MySQL series ships a
// package literally called `mysql-server`, so joining observed state on the
// package name reported *every* MySQL card installed the moment any one of them
// was. Only the version distinguishes the series.
func TestOnlyTheSeriesActuallyInstalledIsReportedInstalled(t *testing.T) {
	runner := newFakeRunner()
	runner.installed["mysql-server"] = "8.0.46-0ubuntu0.24.04.3"
	operator := newOperator(t, runner)

	installed := installedNames(t, operator)
	if _, ok := installed[dbIdentity("mysql", "8.0")]; !ok {
		t.Fatalf("MySQL 8.0 is on the node but was not reported installed: %v", installed)
	}
	for _, other := range []string{"8.4", "9.7"} {
		if _, ok := installed[dbIdentity("mysql", other)]; ok {
			t.Fatalf("MySQL %s reported installed, but only 8.0 is on the node: %v", other, installed)
		}
	}
}

// A server the panel did not install still has to be recognised, whatever its
// origin — that is what turns the MySQL page's dead end into a populated view.
func TestAPreexistingServerIsRecognisedByVersion(t *testing.T) {
	runner := newFakeRunner()
	runner.installed["mariadb-server"] = "1:11.4.12+maria~ubu2404"
	operator := newOperator(t, runner)

	installed := installedNames(t, operator)
	if got := installed[dbIdentity("mariadb", "11.4")]; got != "1:11.4.12+maria~ubu2404" {
		t.Fatalf("MariaDB 11.4 = %q, want it recognised from its version", got)
	}
	if _, ok := installed[dbIdentity("mariadb", "10.11")]; ok {
		t.Fatal("MariaDB 10.11 reported installed, but 11.4 is what is on the node")
	}
}

// The series lives in the version string, and the two vendors shape it
// differently: MariaDB carries an epoch and a "+maria" build suffix, MySQL does
// not. Both must reduce to the same major.minor the catalog is keyed by.
func TestSeriesIsReadFromTheVersionAcrossBothVendorShapes(t *testing.T) {
	cases := map[string]string{
		"8.0.46-0ubuntu0.24.04.3":     "8.0",
		"8.4.10-1ubuntu24.04":         "8.4",
		"9.7.1-1ubuntu24.04":          "9.7",
		"1:10.11.14-0ubuntu0.24.04.1": "10.11",
		"1:11.4.12+maria~ubu2404":     "11.4",
		"1:13.0.1+maria~ubu2404":      "13.0",
		"8.4":                         "8.4",
		"":                            "",
		"not-a-version":               "",
		"8":                           "",
	}
	for version, want := range cases {
		if got := dbSeriesOf(version); got != want {
			t.Errorf("dbSeriesOf(%q) = %q, want %q", version, got, want)
		}
	}
}

// repo.mysql.com publishes i386 and amd64 only. On arm64 its repository adds
// nothing, so offering those cards would promise an install apt could only
// satisfy from the base repo's 8.0. MariaDB publishes both, and Ubuntu's own
// MySQL 8.0 is everywhere.
func TestMySQLVendorSeriesAreOfferedOnlyWhereTheRepositoryPublishes(t *testing.T) {
	arm := newFakeRunner()
	arm.arch = "arm64"
	if got := joined(versionsOf(t, newOperator(t, arm), "mysql")); got != "8.0" {
		t.Fatalf("mysql on arm64 = %s, want only the base repo's 8.0", got)
	}
	if got := joined(versionsOf(t, newOperator(t, arm), "mariadb")); got != "10.11,11.4,11.8,12.3,13.0" {
		t.Fatalf("mariadb on arm64 = %s, want every series (the vendor publishes arm64)", got)
	}
	if got := joined(versionsOf(t, newOperator(t, newFakeRunner()), "mysql")); got != "8.0,8.4,9.7" {
		t.Fatalf("mysql on amd64 = %s, want the vendor's LTS series too", got)
	}
}

// The failure mode the architecture gate exists to prevent, forced open: if a
// vendor repository ever contributes nothing, apt satisfies `mysql-server` from
// Ubuntu's archive and exits 0. Every package-name check calls that success.
// Only the landed version says otherwise, so verification must refuse it.
func TestInstallRefusesWhenAptLandsADifferentSeries(t *testing.T) {
	runner := newFakeRunner()
	runner.repoAddsNothing = true
	operator := newOperator(t, runner)

	_, err := installDB(t, operator, "mysql", "8.4")
	if err == nil {
		t.Fatal("install reported success while apt actually installed the base repo's 8.0")
	}
	if !strings.Contains(err.Error(), "8.4") {
		t.Fatalf("error should name the series that was asked for, got: %v", err)
	}
}

func TestInstallVerifiesTheRequestedSeriesLanded(t *testing.T) {
	operator := newOperator(t, newFakeRunner())
	observation, err := installDB(t, operator, "mysql", "8.4")
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if !observation.Installed || !observation.Verified {
		t.Fatalf("observation = %+v, want installed and verified", observation)
	}
	if dbSeriesOf(observation.Version) != "8.4" {
		t.Fatalf("observed version %q is not in the 8.4 series", observation.Version)
	}
	if _, ok := installedNames(t, operator)[dbIdentity("mysql", "8.4")]; !ok {
		t.Fatal("MySQL 8.4 did not report installed after a successful install")
	}
}

// apt resolves a conflict between the two engines by purging the incumbent, and
// `-y` means it would not ask. A live server and an unreadable data directory is
// too much to risk on a warning someone can click past.
func TestPlanRefusesAnEngineThatWouldEvictTheOther(t *testing.T) {
	runner := newFakeRunner()
	runner.installed["mariadb-server"] = "1:11.4.12+maria~ubu2404"
	operator := newOperator(t, runner)

	_, err := operator.Plan(context.Background(), Change{Action: ActionInstall, App: "mysql", Version: "8.4"})
	if err == nil {
		t.Fatal("planned a MySQL install on a node already running MariaDB")
	}
	if !strings.Contains(err.Error(), "MariaDB 11.4") {
		t.Fatalf("refusal should name the engine in the way, got: %v", err)
	}
}

// Same vendor, different series is a real upgrade path rather than an eviction,
// so it plans — but it rewrites the data directory irreversibly and has to say so.
func TestPlanWarnsWhenReplacingOneSeriesWithAnother(t *testing.T) {
	runner := newFakeRunner()
	runner.installed["mariadb-server"] = "1:10.11.14-0ubuntu0.24.04.1"
	operator := newOperator(t, runner)

	plan := planFor(t, operator, "mariadb", "12.3")
	if !strings.Contains(strings.Join(plan.Warnings, " "), "MariaDB 10.11") {
		t.Fatalf("warnings should flag the in-place series change, got: %v", plan.Warnings)
	}
}

// The sources file must name exactly the series being installed. Leaving a
// second series configured would let a routine `apt upgrade` walk the server
// across a major version with nobody asking for it.
func TestVendorSourcesNameOnlyTheRequestedSeries(t *testing.T) {
	runner := newFakeRunner()
	operator := newOperator(t, runner)

	if _, err := installDB(t, operator, "mariadb", "11.4"); err != nil {
		t.Fatalf("install 11.4: %v", err)
	}
	list := runner.files[dbVendors["mariadb"].listPath]
	if !strings.Contains(list, "/repo/11.4/") {
		t.Fatalf("sources = %q, want the 11.4 repository", list)
	}

	if _, err := installDB(t, operator, "mariadb", "12.3"); err != nil {
		t.Fatalf("install 12.3: %v", err)
	}
	list = runner.files[dbVendors["mariadb"].listPath]
	if strings.Contains(list, "/repo/11.4/") || !strings.Contains(list, "/repo/12.3/") {
		t.Fatalf("sources = %q, want 11.4 replaced by 12.3, not both", list)
	}
}

// A series Ubuntu ships needs no vendor repository — and must actively drop one
// left behind by an earlier install, or apt would upgrade straight back off it.
func TestInstallingABaseRepoSeriesRemovesTheVendorRepository(t *testing.T) {
	runner := newFakeRunner()
	operator := newOperator(t, runner)

	if _, err := installDB(t, operator, "mysql", "8.4"); err != nil {
		t.Fatalf("install 8.4: %v", err)
	}
	if _, ok := runner.files[dbVendors["mysql"].listPath]; !ok {
		t.Fatal("installing 8.4 should have configured the vendor repository")
	}

	if _, err := installDB(t, operator, "mysql", "8.0"); err != nil {
		t.Fatalf("install 8.0: %v", err)
	}
	if list, ok := runner.files[dbVendors["mysql"].listPath]; ok {
		t.Fatalf("vendor repository %q still configured after installing the base repo's 8.0", list)
	}
}

// Configuring a repository needs gpg and curl, and a minimal image that cleaned
// /var/lib/apt/lists has no index to install them from — apt answers "Package
// 'gnupg' has no installation candidate" and the install dies before it reaches
// the repository. The index refresh must therefore come before the repository is
// touched, not only before the package install.
func TestRepositorySetupRefreshesTheIndexBeforeInstallingItsTooling(t *testing.T) {
	runner := newFakeRunner()
	operator := newOperator(t, runner)
	if _, err := installDB(t, operator, "mariadb", "11.4"); err != nil {
		t.Fatalf("install: %v", err)
	}
	for _, call := range runner.calls {
		if call[0] != "apt-get" {
			continue
		}
		switch call[1] {
		case "update":
			return // the index was refreshed first
		case "install":
			t.Fatalf("apt-get install ran before any apt-get update: %v", call)
		}
	}
	t.Fatal("no apt-get update ran at all")
}

// Removing the engine has to take its vendor repository with it. Left behind, it
// makes every apt-get update on the node fetch from a third-party mirror nothing
// uses any more — and it is still there offering packages if a different engine
// is installed later.
func TestRemovingTheEngineDropsItsVendorRepository(t *testing.T) {
	runner := newFakeRunner()
	operator := newOperator(t, runner)
	if _, err := installDB(t, operator, "mariadb", "11.4"); err != nil {
		t.Fatalf("install: %v", err)
	}
	if _, ok := runner.files[dbVendors["mariadb"].listPath]; !ok {
		t.Fatal("install should have configured the vendor repository")
	}

	plan, err := operator.Plan(context.Background(), Change{Action: ActionRemove, App: "mariadb", Version: "11.4"})
	if err != nil {
		t.Fatalf("plan remove: %v", err)
	}
	if _, err := operator.Apply(context.Background(), plan); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if list, ok := runner.files[dbVendors["mariadb"].listPath]; ok {
		t.Fatalf("vendor repository %q survived the engine it was added for", list)
	}
}

// The pinned fingerprint is the trust anchor for everything apt then accepts
// from the repository, so it has to reach the command that verifies the key.
func TestRepositorySetupPassesThePinnedFingerprint(t *testing.T) {
	runner := newFakeRunner()
	operator := newOperator(t, runner)
	if _, err := installDB(t, operator, "mariadb", "11.4"); err != nil {
		t.Fatalf("install: %v", err)
	}
	for _, call := range runner.calls {
		if call[0] != "sh" || len(call) < 5 || !strings.Contains(call[2], "want_fpr") {
			continue
		}
		if call[4] != dbVendors["mariadb"].keyURL || call[5] != dbVendors["mariadb"].fingerprint {
			t.Fatalf("repository setup got key %q / fingerprint %q, want the pinned pair", call[4], call[5])
		}
		return
	}
	t.Fatal("no repository setup call verified a signing key")
}
