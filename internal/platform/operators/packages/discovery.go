package packages

import (
	"context"
	"sort"
	"strings"
)

// Discover reports the installed state of every package the catalog manages,
// using a single dpkg-query. dpkg-query exits non-zero when some queried
// packages are unknown; that is expected here (most catalog packages are
// usually absent), so the exit status is ignored and the output is parsed
// defensively against the known package names.
func (o *HostOperator) Discover(ctx context.Context) ([]InstalledPackage, error) {
	names := allPackageNames()
	args := append([]string{"-W", "-f", "${binary:Package}|${Version}|${db:Status-Status}\n"}, names...)
	output, _ := o.runner.Run(ctx, Command{Name: "dpkg-query", Args: args})
	return parseDpkg(string(output), names), nil
}

// parseDpkg extracts pipe-delimited entries for known package names. Any stderr
// noise dpkg-query merges into the output (e.g. "no packages found matching X")
// does not match the delimiter/name filter and is skipped.
func parseDpkg(output string, names []string) []InstalledPackage {
	want := make(map[string]struct{}, len(names))
	for _, name := range names {
		want[name] = struct{}{}
	}
	seen := map[string]struct{}{}
	result := make([]InstalledPackage, 0, len(names))
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Split(strings.TrimSpace(line), "|")
		if len(fields) != 3 {
			continue
		}
		name := strings.TrimSpace(fields[0])
		if _, ok := want[name]; !ok {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		result = append(result, InstalledPackage{
			Name:      name,
			Version:   strings.TrimSpace(fields[1]),
			Installed: strings.TrimSpace(fields[2]) == "installed",
		})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

// fingerprintPackages hashes only the installed subset, so the fingerprint
// changes exactly when the managed install state changes — the TOCTOU guard
// between planning and applying.
func fingerprintPackages(items []InstalledPackage) (string, error) {
	installed := make([]InstalledPackage, 0, len(items))
	for _, item := range items {
		if item.Installed {
			installed = append(installed, item)
		}
	}
	return fingerprint(installed)
}
