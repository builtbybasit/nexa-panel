package packages

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

// MySQL and MariaDB are the one family in this catalog that is a compiled-in
// table rather than enumerated from the node's repositories. Both vendors encode
// the series in the *repository* — MariaDB in the URL path (/repo/11.4/ubuntu),
// MySQL in the apt component (mysql-8.4-lts) — and never in the package name, so
// a series only becomes installable once its repository is configured, and a
// repository is only trustworthy if its URL and signing key were pinned ahead of
// time. A series discovered at runtime cannot be. It is the same reason the
// operator pins ppa:ondrej/php and clones nvm at a fixed tag rather than
// curl|bash. Adding a future series must stay a one-line entry in dbCatalog.

// dbVendor is the per-vendor half of the trust surface: where the signing key
// lives and which package identifies an installed series.
type dbVendor struct {
	// probePackage is the package whose installed version says which series is on
	// the node. Every series of a vendor ships this same name — the version is the
	// only thing that tells them apart — so discovery reads the version, never the
	// name (verified against the live repository indexes, 2026-07-17).
	probePackage string
	// listPath is nexa's own sources file for this vendor. Exactly one series is
	// ever written to it: leaving another series' repository configured would let
	// a later `apt upgrade` silently cross series (8.0 -> 8.4).
	listPath    string
	keyringPath string
	keyURL      string
	// fingerprint is the trust anchor. It is checked against the freshly
	// downloaded key before that key is allowed to sign anything.
	fingerprint string
}

var dbVendors = map[string]dbVendor{
	"mysql": {
		probePackage: "mysql-server",
		listPath:     "/etc/apt/sources.list.d/nexa-mysql.list",
		keyringPath:  "/usr/share/keyrings/nexa-mysql.gpg",
		// MySQL renews the *same* key under a new year-stamped URL rather than
		// rotating to a new key. The -2023 URL now serves a copy that expired on
		// 2025-10-22, which apt rejects as "the repository is not signed" — a
		// misleading error for an expiry. The fingerprint below survives renewal;
		// only this URL moves, so bump it when the key expires (currently
		// 2027-10-23).
		keyURL:      "https://repo.mysql.com/RPM-GPG-KEY-mysql-2025",
		fingerprint: "BCA43417C3B485DD128EC6D4B7B3B788A8D3785C",
	},
	"mariadb": {
		probePackage: "mariadb-server",
		listPath:     "/etc/apt/sources.list.d/nexa-mariadb.list",
		keyringPath:  "/usr/share/keyrings/nexa-mariadb.gpg",
		keyURL:       "https://mariadb.org/mariadb_release_signing_key.pgp",
		fingerprint:  "177F4010FE56CA3336300305F1656F24C74CD1D8",
	},
}

// dbSeries is one declared (app, series) pair and the exact repository that
// publishes it.
type dbSeries struct {
	App     string
	Version string
	Summary string
	// repoURL empty means Ubuntu's own archive already carries this series and no
	// vendor repository is needed; nexa's sources file for the vendor is then
	// removed rather than written.
	repoURL   string
	component string
	// arches restricts the entry to the dpkg architectures the repository actually
	// publishes; empty means unrestricted. MySQL's vendor repository is
	// amd64/i386 only — on arm64 it adds nothing and apt silently falls back to
	// the base repo's 8.0, so those series must not be offered there at all.
	arches   []string
	packages []string
}

const mysqlRepoURL = "https://repo.mysql.com/apt/ubuntu/"

// mysqlVendorArches are the only architectures repo.mysql.com publishes
// (dists/noble/InRelease declares "Architectures: i386 amd64"; every
// binary-arm64 index 404s).
var mysqlVendorArches = []string{"amd64", "i386"}

// mariadbSeries builds a MariaDB row: everything except the summary follows from
// the series, and the vendor publishes every live series for both amd64 and
// arm64. The version is a compiled-in literal from dbCatalog, never a caller
// value, so interpolating it into the URL is safe.
//
// mirror.mariadb.org redirects to a regional mirror rather than serving the
// packages itself, so availability varies; integrity does not, because the
// pinned signing key still has to have signed whatever the mirror returns.
func mariadbSeries(version, summary string) dbSeries {
	return dbSeries{
		App: "mariadb", Version: version, Summary: summary,
		repoURL:   "https://mirror.mariadb.org/repo/" + version + "/ubuntu",
		component: "main",
		packages:  []string{"mariadb-server"},
	}
}

// dbCatalog is the declared set. MySQL 8.0 comes from Ubuntu's archive so it is
// installable on every architecture; the newer MySQL LTS series exist only in
// the vendor repository and so only on amd64/i386. Every live MariaDB series is
// published for both amd64 and arm64.
var dbCatalog = []dbSeries{
	{
		App: "mysql", Version: "8.0",
		Summary:  "MySQL 8.0 server (Ubuntu's own build; manage databases from the MySQL page)",
		packages: []string{"mysql-server"},
	},
	{
		App: "mysql", Version: "8.4",
		Summary:   "MySQL 8.4 LTS server from Oracle's repository (manage databases from the MySQL page)",
		repoURL:   mysqlRepoURL,
		component: "mysql-8.4-lts",
		arches:    mysqlVendorArches,
		packages:  []string{"mysql-server"},
	},
	{
		App: "mysql", Version: "9.7",
		Summary:   "MySQL 9.7 LTS server from Oracle's repository (manage databases from the MySQL page)",
		repoURL:   mysqlRepoURL,
		component: "mysql-9.7-lts",
		arches:    mysqlVendorArches,
		packages:  []string{"mysql-server"},
	},
	// MariaDB 10.6 is still listed at mirror.mariadb.org/repo/ but has no noble
	// build, so it is deliberately absent.
	mariadbSeries("10.11", "MariaDB 10.11 LTS server (manage databases from the MySQL page)"),
	mariadbSeries("11.4", "MariaDB 11.4 LTS server (manage databases from the MySQL page)"),
	mariadbSeries("11.8", "MariaDB 11.8 LTS server (manage databases from the MySQL page)"),
	mariadbSeries("12.3", "MariaDB 12.3 server (manage databases from the MySQL page)"),
	mariadbSeries("13.0", "MariaDB 13.0 server (manage databases from the MySQL page)"),
}

// supports reports whether this series' repository publishes the node's
// architecture.
func (s dbSeries) supports(arch string) bool {
	if len(s.arches) == 0 {
		return true
	}
	for _, candidate := range s.arches {
		if candidate == arch {
			return true
		}
	}
	return false
}

// label is the display name, e.g. "MariaDB 11.4".
func (s dbSeries) label() string {
	if s.App == "mysql" {
		return "MySQL " + s.Version
	}
	return "MariaDB " + s.Version
}

// dbIdentity is the synthetic name discovery reports an installed series under.
// The vendors' real package names cannot serve here: every series ships the same
// `mysql-server`/`mariadb-server`, so joining on the package name would light up
// every series' card at once. This mirrors the Node.js identifiers, which are
// likewise synthetic because nvm has no apt package.
func dbIdentity(app, version string) string { return app + "-" + version }

// dbVersionPattern extracts the leading major.minor from a dpkg version, past any
// epoch and before any packaging suffix. One rule covers both vendors:
// "8.4.10-1ubuntu24.04" -> "8.4", "1:11.4.12+maria~ubu2404" -> "11.4",
// "8.0.46-0ubuntu0.24.04.3" -> "8.0".
var dbVersionPattern = regexp.MustCompile(`^(?:[0-9]+:)?([0-9]{1,3}\.[0-9]{1,3})(?:[.+~-]|$)`)

// dbSeriesOf maps an installed version back to its series, or "" if the version
// has an unrecognised shape.
func dbSeriesOf(version string) string {
	match := dbVersionPattern.FindStringSubmatch(strings.TrimSpace(version))
	if match == nil {
		return ""
	}
	return match[1]
}

// dbEntries returns the declared series this node's architecture can actually
// install, as catalog entries.
func dbEntries(arch string) []catalogEntry {
	entries := []catalogEntry{}
	for _, series := range dbCatalog {
		if !series.supports(arch) {
			continue
		}
		entries = append(entries, catalogEntry{
			App: series.App, Version: series.Version, Label: series.label(),
			Summary: series.Summary, Category: "database", Repo: repoDeclaredDB,
			Identity: dbIdentity(series.App, series.Version),
			Packages: append([]string(nil), series.packages...),
		})
	}
	return entries
}

// dbSeriesFor finds the declared series behind a catalog entry.
func dbSeriesFor(app, version string) (dbSeries, bool) {
	for _, series := range dbCatalog {
		if series.App == app && series.Version == version {
			return series, true
		}
	}
	return dbSeries{}, false
}

// architecture reports the node's dpkg architecture, which gates the series
// whose repositories do not publish for it.
func (o *HostOperator) architecture(ctx context.Context) (string, error) {
	output, err := o.runner.Run(ctx, command("dpkg", "--print-architecture"))
	if err != nil {
		return "", commandError("read the node's package architecture", output, err)
	}
	arch := strings.TrimSpace(string(output))
	if arch == "" {
		return "", fmt.Errorf("the node reported no package architecture")
	}
	return arch, nil
}

// codename reports the distribution codename (e.g. "noble") that the vendor
// sources line needs.
func (o *HostOperator) codename(ctx context.Context) (string, error) {
	output, err := o.runner.Run(ctx, command("sh", "-c", `. /etc/os-release; printf '%s' "$VERSION_CODENAME"`))
	if err != nil {
		return "", commandError("read the node's distribution codename", output, err)
	}
	codename := strings.TrimSpace(string(output))
	if !codenamePattern.MatchString(codename) {
		return "", fmt.Errorf("the node reported an unusable distribution codename %q", codename)
	}
	return codename, nil
}

// codenamePattern gates the one value in the sources line that is read off the
// node rather than compiled in.
var codenamePattern = regexp.MustCompile(`^[a-z][a-z0-9]{1,30}$`)
