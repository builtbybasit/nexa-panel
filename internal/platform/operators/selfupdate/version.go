package selfupdate

import (
	"strings"

	"golang.org/x/mod/semver"
)

// normalizeVersion trims a leading "v" and surrounding space from a version
// string, returning "" when it does not match the strict semver shape. It is the
// single gate every version passes through, so neither a release tag nor a
// caller value can smuggle another shape downstream.
func normalizeVersion(raw string) string {
	value := strings.TrimSpace(raw)
	value = strings.TrimPrefix(value, "v")
	// x/mod intentionally accepts shorthand such as v1 and v1.2. Requiring the
	// canonical string to equal the input preserves the release contract's exact
	// MAJOR.MINOR.PATCH shape while delegating every SemVer edge case to the
	// maintained parser.
	candidate := "v" + value
	if strings.Contains(value, "+") || !semver.IsValid(candidate) || semver.Canonical(candidate) != candidate {
		return ""
	}
	return value
}

// isNewer reports whether candidate is a strictly higher version than baseline.
// It compares the numeric major/minor/patch triple; a pre-release build of the
// same triple is treated as not newer, so a node never "updates" sideways onto a
// pre-release of the version it already runs.
func isNewer(candidate, baseline string) bool {
	candidate = normalizeVersion(candidate)
	baseline = normalizeVersion(baseline)
	return candidate != "" && baseline != "" && semver.Compare("v"+candidate, "v"+baseline) > 0
}
