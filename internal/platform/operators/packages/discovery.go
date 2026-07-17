package packages

import (
	"context"
	"sort"
	"strings"
)

// Discover reports the installed state of every package the catalog manages.
// apt packages come from a single dpkg-query (its non-zero exit for unknown
// packages is expected and ignored); nvm-managed Node.js versions come from the
// nvm directory. The two are merged into one installed set.
func (o *HostOperator) Discover(ctx context.Context) ([]InstalledPackage, error) {
	names := aptPackageNames()
	args := append([]string{"-W", "-f", "${binary:Package}|${Version}|${db:Status-Status}\n"}, names...)
	output, _ := o.runner.Run(ctx, Command{Name: "dpkg-query", Args: args})
	result := parseDpkg(string(output), names)
	return append(result, o.discoverNode(ctx)...), nil
}

// discoverNode lists the Node.js majors nvm has installed under nvmDir and maps
// each to its synthetic catalog identifier. Runs through the command runner so
// tests can inject the directory listing.
func (o *HostOperator) discoverNode(ctx context.Context) []InstalledPackage {
	command := command("sh", "-c", `ls -1 "$NVM_DIR/versions/node" 2>/dev/null || true`)
	command.Env = append(command.Env, "NVM_DIR="+nvmDir)
	output, err := o.runner.Run(ctx, command)
	if err != nil {
		return nil
	}
	byMajor := map[string]string{}
	for _, line := range strings.Split(string(output), "\n") {
		name := strings.TrimSpace(line)
		if !strings.HasPrefix(name, "v") {
			continue
		}
		full := strings.TrimPrefix(name, "v")
		major := full
		if dot := strings.Index(full, "."); dot > 0 {
			major = full[:dot]
		}
		byMajor[major] = full
	}
	items := []InstalledPackage{}
	for _, version := range nodeVersions {
		if full, ok := byMajor[version]; ok {
			items = append(items, InstalledPackage{Name: nodePackageName(version), Version: full, Installed: true})
		}
	}
	return items
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
