package packages

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func versionsOf(t *testing.T, operator *HostOperator, app string) []string {
	t.Helper()
	entries, err := operator.Catalog(context.Background())
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	versions := []string{}
	for _, entry := range entries {
		if entry.App == app {
			versions = append(versions, entry.Version)
		}
	}
	return versions
}

func joined(versions []string) string {
	result := ""
	for index, version := range versions {
		if index > 0 {
			result += ","
		}
		result += version
	}
	return result
}

// The catalog must follow the node's repositories rather than a table in the
// source: a PHP branch the repository publishes is installable without a code
// change, and one below the product floor is not offered at all.
func TestCatalogEnumeratesPHPFromTheRepositoryAboveTheFloor(t *testing.T) {
	operator := newOperator(t, newFakeRunner())
	if got := joined(versionsOf(t, operator, "php")); got != "7.4,8.1,8.3,8.4,8.5" {
		t.Fatalf("php versions = %s, want the repository's branches at or above %s", got, phpFloor)
	}
}

// A branch published after this code was written must appear on its own.
func TestCatalogPicksUpANewlyPublishedPHPBranch(t *testing.T) {
	runner := newFakeRunner()
	runner.phpRepo = append(runner.phpRepo, "8.6", "9.0")
	operator := newOperator(t, runner)
	got := joined(versionsOf(t, operator, "php"))
	if got != "7.4,8.1,8.3,8.4,8.5,8.6,9.0" {
		t.Fatalf("php versions = %s, want newly published branches included", got)
	}
}

func TestCatalogDropsPostgresVersionsBelowTheFloor(t *testing.T) {
	operator := newOperator(t, newFakeRunner())
	if got := joined(versionsOf(t, operator, "postgresql")); got != "16,17,18" {
		t.Fatalf("postgresql versions = %s, want 9.6 dropped by the %s floor", got, postgresFloor)
	}
}

// Only LTS lines are offered, and only the newest few: 18 has aged out, 23/25/26
// are not LTS. Nothing here is a version literal in the operator.
func TestCatalogOffersOnlyTheNewestNodeLTSMajors(t *testing.T) {
	operator := newOperator(t, newFakeRunner())
	if got := joined(versionsOf(t, operator, "nodejs")); got != "20,22,24" {
		t.Fatalf("node versions = %s, want the newest %d LTS majors", got, nodeLTSCount)
	}
}

// The apt half of the catalog must survive a node that cannot reach nodejs.org;
// installing Node.js would need that network anyway.
func TestCatalogDegradesWhenTheNodeIndexIsUnreachable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)
	operator := newOperatorWithIndex(t, newFakeRunner(), server.URL)
	if got := versionsOf(t, operator, "nodejs"); len(got) != 0 {
		t.Fatalf("node versions = %v, want none when the index is unreachable", got)
	}
	if got := versionsOf(t, operator, "php"); len(got) == 0 {
		t.Fatal("php must stay installable when only nodejs.org is unreachable")
	}
}

// Enumeration reads package names off the apt index, so the version shape — not
// the index — is what keeps a version safe to put on a command line.
func TestCatalogIgnoresMalformedPackageNamesFromTheIndex(t *testing.T) {
	runner := newFakeRunner()
	runner.phpRepo = []string{"8.3", "8.3; rm -rf /", "$(id)", "../../etc/passwd", "99999.99999"}
	operator := newOperator(t, runner)
	for _, version := range versionsOf(t, operator, "php") {
		if !phpVersionPattern.MatchString(version) {
			t.Fatalf("catalog admitted a malformed version %q from the apt index", version)
		}
	}
}

// The catalog is read on every list/plan/apply, so it must not shell out to
// apt-cache each time.
func TestCatalogIsCachedBetweenReads(t *testing.T) {
	runner := newFakeRunner()
	operator := newOperator(t, runner)
	for i := 0; i < 3; i++ {
		if _, err := operator.Catalog(context.Background()); err != nil {
			t.Fatalf("catalog: %v", err)
		}
	}
	searches := 0
	for _, call := range runner.calls {
		if call[0] == "apt-cache" {
			searches++
		}
	}
	// One search per apt-backed family (php, postgresql), not per read.
	if searches != 2 {
		t.Fatalf("apt-cache searches = %d, want 2 (the catalog should be cached)", searches)
	}
}
