package packages

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// repoKind identifies which fixed, well-known third-party repository (if any) a
// catalog entry needs before its packages can be installed. Repositories are
// never supplied by the caller — this closed set is part of the trust surface.
type repoKind string

const (
	repoNone      repoKind = ""
	repoOndrejPHP repoKind = "ondrej-php"
	repoPGDG      repoKind = "pgdg"
)

// installMethod selects how an entry is installed. The default (apt) covers PHP,
// PostgreSQL, and Composer; Node.js uses nvm so several major versions can
// coexist (apt's single `nodejs` package cannot).
type installMethod string

const (
	methodAPT installMethod = ""
	methodNVM installMethod = "nvm"
)

// catalogEntry is one installable (app, version) with its derived packages and
// the repository/method it requires.
type catalogEntry struct {
	App      string
	Version  string
	Label    string
	Summary  string
	Category string
	Repo     repoKind
	Method   installMethod
	Packages []string
}

// The version literals below are deliberate product floors, not a version list:
// versions themselves are enumerated from the node's own repositories so a newly
// published PHP or Node.js release becomes installable without a code change.
const (
	// phpFloor is the oldest PHP branch Nexa serves; the site renderer refuses
	// anything older, so offering it here would be a dead end.
	phpFloor = "7.4"
	// postgresFloor drops the long-dead majors PGDG still publishes.
	postgresFloor = "13"
	// nodeLTSCount keeps the runtime section to the newest maintained LTS lines.
	// Expressed as a count so a new LTS enters and the oldest leaves on its own.
	nodeLTSCount = 3
)

// Version shapes. Enumeration reads package names from the apt index, but these
// patterns are what actually keep the result safe: a version is interpolated
// into a package name only after matching one of them, so no repository (or
// anything masquerading as one) can smuggle a command line through.
var (
	phpVersionPattern = regexp.MustCompile(`^[0-9]{1,2}\.[0-9]{1,2}$`)
	majorPattern      = regexp.MustCompile(`^[0-9]{1,3}$`)

	phpPackagePattern      = regexp.MustCompile(`^php([0-9]{1,2}\.[0-9]{1,2})-fpm$`)
	postgresPackagePattern = regexp.MustCompile(`^postgresql-([0-9]{1,3})$`)
)

// nodePackageName is the synthetic per-major identifier used to track an
// nvm-installed Node.js version (there is no apt package for it).
func nodePackageName(version string) string { return "nodejs-" + version }

// phpPackages returns the FPM runtime plus a common, safe extension set for a
// PHP branch. Only a pattern-validated version is interpolated.
func phpPackages(version string) []string {
	extensions := []string{"fpm", "cli", "common", "curl", "mbstring", "xml", "zip", "gd", "mysql", "pgsql", "intl", "bcmath"}
	packages := make([]string, 0, len(extensions))
	for _, extension := range extensions {
		packages = append(packages, "php"+version+"-"+extension)
	}
	return packages
}

// catalog returns the installable set for this node, cached briefly: it is read
// on every list, plan, and apply, and each rebuild shells out to apt-cache and
// (for Node.js) fetches nodejs.org. Installing may add a repository, so apply
// invalidates the cache rather than waiting for it to expire.
func (o *HostOperator) catalog(ctx context.Context) ([]catalogEntry, error) {
	o.catalogMu.Lock()
	defer o.catalogMu.Unlock()
	if o.cachedCatalog != nil && o.now().Sub(o.cachedAt) < o.catalogTTL {
		return o.cachedCatalog, nil
	}
	entries, err := o.enumerate(ctx)
	if err != nil {
		return nil, err
	}
	o.cachedCatalog, o.cachedAt = entries, o.now()
	return entries, nil
}

func (o *HostOperator) invalidateCatalog() {
	o.catalogMu.Lock()
	o.cachedCatalog = nil
	o.catalogMu.Unlock()
}

// enumerate builds the catalog from what this node's repositories actually
// offer. A failure to read the local apt index is fatal — it means every apt
// entry would silently vanish — but Node.js degrades to nothing if nodejs.org is
// unreachable, so an offline node still lists everything apt can install.
func (o *HostOperator) enumerate(ctx context.Context) ([]catalogEntry, error) {
	entries := []catalogEntry{}

	phpVersions, err := o.aptVersions(ctx, `^php[0-9]+\.[0-9]+-fpm$`, phpPackagePattern, phpVersionPattern)
	if err != nil {
		return nil, err
	}
	for _, version := range atLeast(phpVersions, phpFloor) {
		summary := "PHP " + version + " runtime (PHP-FPM) with common extensions"
		if version == phpFloor {
			summary = "PHP " + phpFloor + " runtime (PHP-FPM) — end of life, kept for legacy sites"
		}
		entries = append(entries, catalogEntry{
			App: "php", Version: version, Label: "PHP " + version, Summary: summary,
			Category: "php", Repo: repoOndrejPHP, Packages: phpPackages(version),
		})
	}

	postgresVersions, err := o.aptVersions(ctx, `^postgresql-[0-9]+$`, postgresPackagePattern, majorPattern)
	if err != nil {
		return nil, err
	}
	for _, version := range atLeast(postgresVersions, postgresFloor) {
		entries = append(entries, catalogEntry{
			App: "postgresql", Version: version, Label: "PostgreSQL " + version,
			Summary:  "PostgreSQL " + version + " server (provision clusters from the Databases page)",
			Category: "database", Repo: repoPGDG, Packages: []string{"postgresql-" + version},
		})
	}

	for _, version := range o.nodeLTSVersions(ctx) {
		entries = append(entries, catalogEntry{
			App: "nodejs", Version: version, Label: "Node.js " + version,
			Summary:  "Node.js " + version + " LTS runtime and npm (installed with nvm)",
			Category: "runtime", Method: methodNVM, Packages: []string{nodePackageName(version)},
		})
	}

	entries = append(entries, catalogEntry{
		App: "composer", Version: "", Label: "Composer",
		Summary:  "Composer dependency manager for PHP",
		Category: "tooling", Repo: repoNone, Packages: []string{"composer"},
	})
	return entries, nil
}

// aptVersions lists versions offered by the node's configured repositories.
// search narrows the index; capture extracts the version from each package name;
// shape is the final gate — anything that does not match is dropped rather than
// trusted, so a malformed or hostile package name cannot become a version.
func (o *HostOperator) aptVersions(ctx context.Context, search string, capture, shape *regexp.Regexp) ([]string, error) {
	output, err := o.runner.Run(ctx, command("apt-cache", "search", "--names-only", search))
	if err != nil {
		return nil, commandError("search the apt package index", output, err)
	}
	seen := map[string]struct{}{}
	versions := []string{}
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) == 0 {
			continue
		}
		match := capture.FindStringSubmatch(fields[0])
		if match == nil || !shape.MatchString(match[1]) {
			continue
		}
		if _, exists := seen[match[1]]; exists {
			continue
		}
		seen[match[1]] = struct{}{}
		versions = append(versions, match[1])
	}
	sortVersions(versions)
	return versions, nil
}

// atLeast drops versions below floor, comparing numerically so "8.10" sorts
// above "8.9" and "7.4" is not compared as a string.
func atLeast(versions []string, floor string) []string {
	kept := make([]string, 0, len(versions))
	for _, version := range versions {
		if compareVersions(version, floor) >= 0 {
			kept = append(kept, version)
		}
	}
	return kept
}

func sortVersions(versions []string) {
	sort.Slice(versions, func(i, j int) bool { return compareVersions(versions[i], versions[j]) < 0 })
}

// compareVersions orders dotted numeric versions field by field.
func compareVersions(a, b string) int {
	left, right := strings.Split(a, "."), strings.Split(b, ".")
	for index := 0; index < len(left) || index < len(right); index++ {
		var first, second int
		if index < len(left) {
			first, _ = strconv.Atoi(left[index])
		}
		if index < len(right) {
			second, _ = strconv.Atoi(right[index])
		}
		if first != second {
			return first - second
		}
	}
	return 0
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

// Catalog returns the installable catalog this node's repositories offer.
func (o *HostOperator) Catalog(ctx context.Context) ([]CatalogEntry, error) {
	internal, err := o.catalog(ctx)
	if err != nil {
		return nil, err
	}
	entries := make([]CatalogEntry, 0, len(internal))
	for _, entry := range internal {
		entries = append(entries, CatalogEntry{
			App: entry.App, Version: entry.Version, Label: entry.Label,
			Summary: entry.Summary, Category: entry.Category,
			Packages: append([]string(nil), entry.Packages...),
		})
	}
	return entries, nil
}

// lookup finds a catalog entry by app and version.
func (o *HostOperator) lookup(ctx context.Context, app, version string) (catalogEntry, bool, error) {
	entries, err := o.catalog(ctx)
	if err != nil {
		return catalogEntry{}, false, err
	}
	for _, entry := range entries {
		if entry.App == app && entry.Version == version {
			return entry, true, nil
		}
	}
	return catalogEntry{}, false, nil
}

// aptPackageNames returns the deduplicated set of apt package names the catalog
// manages — the exact set dpkg-query discovery inspects. nvm-managed entries
// (Node.js) are excluded; they are discovered from the nvm directory instead.
func (o *HostOperator) aptPackageNames(ctx context.Context) ([]string, error) {
	entries, err := o.catalog(ctx)
	if err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	names := []string{}
	for _, entry := range entries {
		if entry.Method != methodAPT {
			continue
		}
		for _, name := range entry.Packages {
			if _, exists := seen[name]; exists {
				continue
			}
			seen[name] = struct{}{}
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names, nil
}

// normalize validates the request against the catalog. It is the security
// boundary: a version reaches a command line only if it matches its strict shape
// AND this node's repositories actually offer it, so callers can never smuggle an
// arbitrary package name into an install command.
func (o *HostOperator) normalize(ctx context.Context, change Change) (Change, catalogEntry, error) {
	change.App = strings.ToLower(strings.TrimSpace(change.App))
	change.Version = strings.TrimSpace(change.Version)
	if change.Action != ActionInstall && change.Action != ActionRemove {
		return Change{}, catalogEntry{}, errors.New("application action must be install or remove")
	}
	entry, ok, err := o.lookup(ctx, change.App, change.Version)
	if err != nil {
		return Change{}, catalogEntry{}, err
	}
	if !ok {
		return Change{}, catalogEntry{}, fmt.Errorf("application %q version %q is not offered by this node's repositories", change.App, change.Version)
	}
	return change, entry, nil
}
