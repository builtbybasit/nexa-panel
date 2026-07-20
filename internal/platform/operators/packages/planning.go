package packages

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// planExpiry bounds how long a reviewed application plan may sit before it must
// be regenerated, matching the other operators' short windows.
const planExpiry = 10 * time.Minute

// Plan discovers current package state, validates the change against the
// catalog, and returns a reviewable, human-readable plan. Signing happens in
// the agent handler, not here.
func (o *HostOperator) Plan(ctx context.Context, change Change) (Plan, error) {
	change, entry, err := o.normalize(ctx, change)
	if err != nil {
		return Plan{}, err
	}
	installed, err := o.Discover(ctx)
	if err != nil {
		return Plan{}, err
	}
	entries, err := o.catalog(ctx)
	if err != nil {
		return Plan{}, err
	}
	present := installedDatabases(entries, installed)
	if err := rejectConflictingEngine(change, entry, present); err != nil {
		return Plan{}, err
	}
	fingerprint, err := fingerprintPackages(installed)
	if err != nil {
		return Plan{}, err
	}
	steps, warnings := planNarrative(change, entry, present)
	now := o.now().UTC()
	return Plan{
		ID: randomID(), Kind: PlanKind, Change: change, Packages: entry.Packages,
		Steps: steps, Warnings: warnings, ObservedFingerprint: fingerprint,
		PlannedAt: now, ExpiresAt: now.Add(planExpiry),
	}, nil
}

// installedDatabases reports the declared MySQL/MariaDB series currently on the
// node, read from the identities Discover resolved from the observed version.
func installedDatabases(entries []catalogEntry, installed []InstalledPackage) []catalogEntry {
	byName := make(map[string]InstalledPackage, len(installed))
	for _, item := range installed {
		byName[item.Name] = item
	}
	present := []catalogEntry{}
	for _, entry := range entries {
		if entry.Repo != repoDeclaredDB {
			continue
		}
		if item, ok := byName[entry.identity()]; ok && item.Installed {
			present = append(present, entry)
		}
	}
	return present
}

// rejectConflictingEngine refuses an install that apt could only satisfy by
// removing the engine already running. MySQL and MariaDB conflict at the package
// level, so `apt-get install -y` resolves the conflict by purging the incumbent —
// taking a live database server with it and orphaning a data directory the
// survivor cannot read. That is far too destructive to express as a warning on a
// plan someone may click through, so it stops here with an explanation instead.
func rejectConflictingEngine(change Change, entry catalogEntry, present []catalogEntry) error {
	if change.Action != ActionInstall || entry.Repo != repoDeclaredDB {
		return nil
	}
	for _, other := range present {
		if other.App != entry.App {
			return fmt.Errorf(
				"%s is already installed on this node, and %s cannot be installed alongside it — remove %s first, backing up its databases beforehand",
				other.Label, entry.Label, other.Label,
			)
		}
	}
	return nil
}

// planNarrative builds the operator-facing step list and warnings.
func planNarrative(change Change, entry catalogEntry, present []catalogEntry) ([]string, []string) {
	if entry.Method == methodNVM {
		return nodeNarrative(change, entry)
	}
	steps := []string{}
	warnings := []string{}
	if change.Action == ActionRemove {
		steps = append(
			steps,
			fmt.Sprintf("Purge %d %s package(s): %s.", len(entry.Packages), entry.Label, strings.Join(entry.Packages, ", ")),
			"Autoremove now-unused dependencies.",
			"Verify the packages are no longer installed.",
		)
		warnings = append(warnings, fmt.Sprintf("Removing %s may break sites or services that depend on it.", entry.Label))
		return steps, warnings
	}
	switch entry.Repo {
	case repoOndrejPHP:
		steps = append(steps, "Ensure the ondrej/php PPA is configured.")
		warnings = append(warnings, "This adds the third-party ondrej/php PPA to apt sources.")
	case repoPGDG:
		steps = append(steps, "Ensure the PostgreSQL PGDG apt repository is configured.")
		warnings = append(warnings, "This adds the official PostgreSQL PGDG repository to apt sources.")
	case repoDeclaredDB:
		steps, warnings = append(steps, databaseRepoSteps(entry)...), append(warnings, databaseRepoWarnings(entry, present)...)
	}
	steps = append(steps, "Update the apt package index.")
	steps = append(
		steps,
		fmt.Sprintf("Install %d %s package(s): %s.", len(entry.Packages), entry.Label, strings.Join(entry.Packages, ", ")),
		"Verify the packages report as installed.",
	)
	if entry.App == "php" && entry.Version == phpFloor {
		warnings = append(warnings, "PHP "+phpFloor+" is end of life and receives no upstream security fixes.")
	}
	if entry.Category == "database" {
		warnings = append(warnings, "A database engine has an ongoing memory cost; review the node's capacity profile before installing on a compact server.")
	}
	return steps, warnings
}

// databaseRepoSteps describes configuring the pinned per-series repository, or
// standing down when Ubuntu's own archive already carries the series.
func databaseRepoSteps(entry catalogEntry) []string {
	series, ok := dbSeriesFor(entry.App, entry.Version)
	if !ok {
		return nil
	}
	vendor := dbVendors[entry.App]
	if series.repoURL == "" {
		return []string{"Install from Ubuntu's own archive; remove any " + entry.App + " vendor repository nexa configured for another series."}
	}
	return []string{
		"Download the " + entry.App + " signing key and verify it against the fingerprint pinned in nexa (" + vendor.fingerprint + ").",
		"Configure " + series.repoURL + " (" + series.component + ") as the only " + entry.App + " repository, so apt cannot cross series.",
	}
}

// databaseRepoWarnings surfaces what an operator must weigh before approving:
// the third-party repository, and — the sharp one — an in-place series change on
// an existing server.
func databaseRepoWarnings(entry catalogEntry, present []catalogEntry) []string {
	warnings := []string{}
	if series, ok := dbSeriesFor(entry.App, entry.Version); ok && series.repoURL != "" {
		warnings = append(warnings, "This adds the official "+entry.Label+" vendor repository to apt sources, pinned to its signing key.")
	}
	for _, other := range present {
		if other.App == entry.App && other.Version != entry.Version {
			warnings = append(warnings, fmt.Sprintf(
				"%s is already installed. This replaces it in place with %s and the server upgrades its data directory on first start — a change that cannot be undone by reinstalling %s. Back up every database first.",
				other.Label, entry.Label, other.Label,
			))
		}
	}
	return warnings
}

// nodeNarrative describes the nvm-based Node.js install/remove flow.
func nodeNarrative(change Change, entry catalogEntry) ([]string, []string) {
	if change.Action == ActionRemove {
		return []string{
				fmt.Sprintf("Uninstall %s via nvm.", entry.Label),
				"Verify the version is no longer available.",
			}, []string{
				fmt.Sprintf("Removing %s affects anything using that Node.js version.", entry.Label),
			}
	}
	return []string{
			"Ensure nvm (Node Version Manager) is installed.",
			fmt.Sprintf("Install %s via nvm (downloads the prebuilt runtime from nodejs.org).", entry.Label),
			"Verify the version is available.",
		}, []string{
			"Node.js versions are managed per-node with nvm under " + nvmDir + ".",
		}
}

// nodeMajor returns the numeric major for a Node.js catalog version. The entry
// already came from the catalog, but the shape is re-checked here because this
// value is what reaches nvm's command line.
func nodeMajor(version string) (int, error) {
	if !majorPattern.MatchString(version) {
		return 0, fmt.Errorf("unsupported Node.js version %q", version)
	}
	return strconv.Atoi(version)
}
