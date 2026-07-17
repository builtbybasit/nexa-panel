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
	change, entry, err := normalize(change)
	if err != nil {
		return Plan{}, err
	}
	installed, err := o.Discover(ctx)
	if err != nil {
		return Plan{}, err
	}
	fingerprint, err := fingerprintPackages(installed)
	if err != nil {
		return Plan{}, err
	}
	steps, warnings := planNarrative(change, entry)
	now := o.now().UTC()
	return Plan{
		ID: randomID(), Kind: PlanKind, Change: change, Packages: entry.Packages,
		Steps: steps, Warnings: warnings, ObservedFingerprint: fingerprint,
		PlannedAt: now, ExpiresAt: now.Add(planExpiry),
	}, nil
}

// planNarrative builds the operator-facing step list and warnings.
func planNarrative(change Change, entry catalogEntry) ([]string, []string) {
	steps := []string{}
	warnings := []string{}
	if change.Action == ActionRemove {
		steps = append(steps,
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
	case repoNodeSource:
		steps = append(steps, fmt.Sprintf("Ensure the NodeSource node_%s.x apt repository is configured.", entry.Version))
		warnings = append(warnings, "This adds the third-party NodeSource repository and signing key to apt sources.")
	}
	if entry.Repo != repoNone {
		steps = append(steps, "Update the apt package index.")
	}
	steps = append(steps,
		fmt.Sprintf("Install %d %s package(s): %s.", len(entry.Packages), entry.Label, strings.Join(entry.Packages, ", ")),
		"Verify the packages report as installed.",
	)
	if entry.App == "php" && entry.Version == "7.4" {
		warnings = append(warnings, "PHP 7.4 is end of life and receives no upstream security fixes.")
	}
	if entry.Category == "database" {
		warnings = append(warnings, "A database engine has an ongoing memory cost; review the node's capacity profile before installing on a compact server.")
	}
	return steps, warnings
}

// nodeMajor returns the numeric major for a validated Node.js catalog version.
func nodeMajor(version string) (int, error) {
	if !nodeMajorPattern.MatchString(version) {
		return 0, fmt.Errorf("unsupported Node.js version %q", version)
	}
	return strconv.Atoi(version)
}
