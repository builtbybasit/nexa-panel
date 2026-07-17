package packages

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// repoKind identifies which fixed, well-known third-party repository (if any) a
// catalog entry needs before its packages can be installed. Repositories are
// never supplied by the caller — this closed set is the entire trust surface.
type repoKind string

const (
	repoNone       repoKind = ""
	repoOndrejPHP  repoKind = "ondrej-php"
	repoNodeSource repoKind = "nodesource"
	repoPGDG       repoKind = "pgdg"
)

// catalogEntry is one installable (app, version) with its derived apt packages
// and the repository it requires. This table is the allowlist: normalize()
// refuses anything not present here, so no attacker-controlled string ever
// reaches the package-install command line.
type catalogEntry struct {
	App      string
	Version  string
	Label    string
	Summary  string
	Category string
	Repo     repoKind
	Packages []string
}

var (
	phpVersions  = []string{"7.4", "8.1", "8.2", "8.3", "8.4"}
	pgVersions   = []string{"16", "17", "18"}
	nodeVersions = []string{"18", "20", "22"}

	nodeMajorPattern = regexp.MustCompile(`^(?:18|20|22)$`)
)

// phpPackages returns the FPM runtime plus a common, safe extension set for a
// PHP branch. Only the validated version is interpolated.
func phpPackages(version string) []string {
	extensions := []string{"fpm", "cli", "common", "curl", "mbstring", "xml", "zip", "gd", "mysql", "pgsql", "intl", "bcmath"}
	packages := make([]string, 0, len(extensions))
	for _, extension := range extensions {
		packages = append(packages, "php"+version+"-"+extension)
	}
	return packages
}

// catalog returns the full installable set. It is a pure function so tests and
// discovery share exactly the table that execution uses.
func catalog() []catalogEntry {
	entries := make([]catalogEntry, 0, len(phpVersions)+len(pgVersions)+len(nodeVersions)+1)
	for _, version := range phpVersions {
		summary := "PHP " + version + " runtime (PHP-FPM) with common extensions"
		if version == "7.4" {
			summary = "PHP 7.4 runtime (PHP-FPM) — end of life, kept for legacy sites"
		}
		entries = append(entries, catalogEntry{
			App: "php", Version: version, Label: "PHP " + version, Summary: summary,
			Category: "php", Repo: repoOndrejPHP, Packages: phpPackages(version),
		})
	}
	for _, version := range pgVersions {
		entries = append(entries, catalogEntry{
			App: "postgresql", Version: version, Label: "PostgreSQL " + version,
			Summary:  "PostgreSQL " + version + " server (provision clusters from the Databases page)",
			Category: "database", Repo: repoPGDG, Packages: []string{"postgresql-" + version},
		})
	}
	for _, version := range nodeVersions {
		entries = append(entries, catalogEntry{
			App: "nodejs", Version: version, Label: "Node.js " + version,
			Summary:  "Node.js " + version + " LTS runtime and npm",
			Category: "runtime", Repo: repoNodeSource, Packages: []string{"nodejs"},
		})
	}
	entries = append(entries, catalogEntry{
		App: "composer", Version: "", Label: "Composer",
		Summary:  "Composer dependency manager for PHP",
		Category: "tooling", Repo: repoNone, Packages: []string{"composer"},
	})
	return entries
}

// CatalogEntry is the public description of one installable application, used by
// the control-plane module to render the catalog without duplicating the table.
type CatalogEntry struct {
	App      string   `json:"app"`
	Version  string   `json:"version"`
	Label    string   `json:"label"`
	Summary  string   `json:"summary"`
	Category string   `json:"category"`
	Packages []string `json:"packages"`
}

// Catalog returns the public installable catalog.
func Catalog() []CatalogEntry {
	internal := catalog()
	entries := make([]CatalogEntry, 0, len(internal))
	for _, entry := range internal {
		entries = append(entries, CatalogEntry{
			App: entry.App, Version: entry.Version, Label: entry.Label,
			Summary: entry.Summary, Category: entry.Category,
			Packages: append([]string(nil), entry.Packages...),
		})
	}
	return entries
}

// lookup finds a catalog entry by app and version.
func lookup(app, version string) (catalogEntry, bool) {
	for _, entry := range catalog() {
		if entry.App == app && entry.Version == version {
			return entry, true
		}
	}
	return catalogEntry{}, false
}

// allPackageNames returns the deduplicated set of every package the catalog can
// manage — the exact set discovery queries.
func allPackageNames() []string {
	seen := map[string]struct{}{}
	names := []string{}
	for _, entry := range catalog() {
		for _, name := range entry.Packages {
			if _, exists := seen[name]; exists {
				continue
			}
			seen[name] = struct{}{}
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// normalize validates the request against the catalog. It is the security
// boundary: it returns an error for any app/version not in the table, so
// callers can never smuggle an arbitrary package name into an install command.
func normalize(change Change) (Change, catalogEntry, error) {
	change.App = strings.ToLower(strings.TrimSpace(change.App))
	change.Version = strings.TrimSpace(change.Version)
	if change.Action != ActionInstall && change.Action != ActionRemove {
		return Change{}, catalogEntry{}, errors.New("application action must be install or remove")
	}
	entry, ok := lookup(change.App, change.Version)
	if !ok {
		return Change{}, catalogEntry{}, fmt.Errorf("application %q version %q is not in the installable catalog", change.App, change.Version)
	}
	return change, entry, nil
}
