package selfupdate

import (
	"strconv"
	"strings"
)

// normalizeVersion trims a leading "v" and surrounding space from a version
// string, returning "" when it does not match the strict semver shape. It is the
// single gate every version passes through, so neither a release tag nor a
// caller value can smuggle another shape downstream.
func normalizeVersion(raw string) string {
	value := strings.TrimSpace(raw)
	value = strings.TrimPrefix(value, "v")
	if !versionPattern.MatchString(value) {
		return ""
	}
	return value
}

// isNewer reports whether candidate is a strictly higher version than baseline.
// It compares the numeric major/minor/patch triple; a pre-release build of the
// same triple is treated as not newer, so a node never "updates" sideways onto a
// pre-release of the version it already runs.
func isNewer(candidate, baseline string) bool {
	candidateCore, candidatePre := splitPreRelease(candidate)
	baselineCore, baselinePre := splitPreRelease(baseline)
	for index := 0; index < 3; index++ {
		if candidateCore[index] != baselineCore[index] {
			return candidateCore[index] > baselineCore[index]
		}
	}
	// Equal cores: a release (no pre-release) outranks a pre-release of the same
	// triple; two builds that are otherwise equal are not "newer".
	return baselinePre != "" && candidatePre == ""
}

func splitPreRelease(version string) ([3]int, string) {
	core := version
	pre := ""
	if index := strings.IndexByte(version, '-'); index >= 0 {
		core = version[:index]
		pre = version[index+1:]
	}
	var triple [3]int
	for index, part := range strings.SplitN(core, ".", 3) {
		if index > 2 {
			break
		}
		triple[index], _ = strconv.Atoi(part)
	}
	return triple, pre
}
