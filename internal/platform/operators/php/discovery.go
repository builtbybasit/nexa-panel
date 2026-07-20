package php

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Versions reports the PHP branches installed on the node: config directories
// under the root that carry an FPM pool directory, matching how the control
// plane's runtimes discovery decides a branch is present.
func (o *HostOperator) Versions(_ context.Context) ([]string, error) {
	entries, err := os.ReadDir(o.configRoot)
	if errors.Is(err, os.ErrNotExist) {
		return []string{}, nil
	}
	if err != nil {
		return nil, err
	}
	versions := []string{}
	for _, entry := range entries {
		name := strings.TrimSpace(entry.Name())
		if !entry.IsDir() || !versionPattern.MatchString(name) {
			continue
		}
		info, statErr := os.Stat(o.fpmConfigDir(name))
		if statErr != nil || !info.IsDir() {
			continue
		}
		versions = append(versions, name)
	}
	sortVersions(versions)
	return versions, nil
}

// normalizeVersion trims and gates a requested branch: it must match the version
// shape AND be a branch the node actually has installed. This is the boundary
// that keeps an arbitrary string out of package names and file paths.
func (o *HostOperator) normalizeVersion(ctx context.Context, version string) (string, error) {
	version = strings.TrimSpace(version)
	if !versionPattern.MatchString(version) {
		return "", fmt.Errorf("PHP version %q is not a supported branch", version)
	}
	installed, err := o.Versions(ctx)
	if err != nil {
		return "", err
	}
	for _, candidate := range installed {
		if candidate == version {
			return version, nil
		}
	}
	return "", fmt.Errorf("PHP %s is not installed on this node", version)
}

// Extensions enumerates the loadable modules the node's repositories offer for a
// branch, joined with observed install state. The available set comes from apt
// (php<ver>-<name>), gated to single-token module suffixes; the runtime and SAPI
// packages are excluded because they are not modules a user installs à la carte.
func (o *HostOperator) Extensions(ctx context.Context, version string) ([]Extension, error) {
	version, err := o.normalizeVersion(ctx, version)
	if err != nil {
		return nil, err
	}
	capture := regexp.MustCompile(`^php` + regexp.QuoteMeta(version) + `-([a-z0-9]+)$`)
	output, err := o.runner.Run(ctx, aptCommand("apt-cache", "search", "--names-only", `^php`+regexp.QuoteMeta(version)+`-`))
	if err != nil {
		return nil, commandError("search the apt package index for PHP extensions", output, err)
	}
	names := []string{}
	seen := map[string]struct{}{}
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) == 0 {
			continue
		}
		match := capture.FindStringSubmatch(fields[0])
		if match == nil {
			continue
		}
		name := match[1]
		if !extensionPattern.MatchString(name) {
			continue
		}
		if _, skip := nonExtensionPackages[name]; skip {
			continue
		}
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	sort.Strings(names)

	installed, err := o.installedExtensions(ctx, version, names)
	if err != nil {
		return nil, err
	}
	extensions := make([]Extension, 0, len(names))
	for _, name := range names {
		pkg := extensionPackage(version, name)
		_, isInstalled := installed[pkg]
		extensions = append(extensions, Extension{Name: name, Package: pkg, Installed: isInstalled})
	}
	return extensions, nil
}

// installedExtensions reports which of the candidate module packages dpkg has in
// the installed state. dpkg-query's non-zero exit for unknown packages is
// expected and ignored; only lines that match a requested name and report
// "installed" count.
func (o *HostOperator) installedExtensions(ctx context.Context, version string, names []string) (map[string]struct{}, error) {
	if len(names) == 0 {
		return map[string]struct{}{}, nil
	}
	packages := make([]string, 0, len(names))
	for _, name := range names {
		packages = append(packages, extensionPackage(version, name))
	}
	args := append([]string{"-W", "-f", "${binary:Package}|${db:Status-Status}\n"}, packages...)
	output, _ := o.runner.Run(ctx, Command{Name: "dpkg-query", Args: args})
	want := make(map[string]struct{}, len(packages))
	for _, pkg := range packages {
		want[pkg] = struct{}{}
	}
	result := map[string]struct{}{}
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Split(strings.TrimSpace(line), "|")
		if len(fields) != 2 {
			continue
		}
		name := strings.TrimSpace(fields[0])
		if _, ok := want[name]; !ok {
			continue
		}
		if strings.TrimSpace(fields[1]) == "installed" {
			result[name] = struct{}{}
		}
	}
	return result, nil
}

// settingsScript reads every registered ini directive under the FPM SAPI. It
// maps each directive to the extension that registers it (so the UI can group by
// module), reports the effective global value, and reports the access mask so
// the UI can show which directives are editable.
const settingsScript = `$module = array();
foreach (get_loaded_extensions() as $ext) {
  $entries = @ini_get_all($ext, false);
  if (is_array($entries)) { foreach ($entries as $key => $value) { $module[$key] = $ext; } }
}
$out = array();
foreach (ini_get_all(null, true) as $key => $info) {
  $out[] = array(
    "name" => $key,
    "value" => (string)$info["global_value"],
    "module" => isset($module[$key]) ? $module[$key] : "core",
    "access" => isset($info["access"]) ? (int)$info["access"] : 7,
  );
}
echo json_encode($out);`

// rawDirective is the shape the settings script emits per directive.
type rawDirective struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Module string `json:"module"`
	Access int    `json:"access"`
}

// Settings reports every php.ini directive for a branch as the FPM SAPI sees it,
// annotated with whether nexa's managed drop-in currently overrides it. The
// values reflect the drop-in because the CLI is invoked with the pool's own
// scan directory, so what an operator saved is what they see back.
func (o *HostOperator) Settings(ctx context.Context, version string) ([]Directive, error) {
	version, err := o.normalizeVersion(ctx, version)
	if err != nil {
		return nil, err
	}
	command := Command{
		Name: phpBinary(version),
		Args: []string{"-c", o.fpmMainIni(version), "-d", "error_reporting=0", "-d", "display_errors=0", "-r", settingsScript},
		Env:  []string{"PHP_INI_SCAN_DIR=" + o.fpmConfigDir(version)},
	}
	output, err := o.runner.Run(ctx, command)
	if err != nil {
		return nil, commandError("read PHP "+version+" settings (is the "+phpBinary(version)+" CLI installed?)", output, err)
	}
	var raw []rawDirective
	if err := json.Unmarshal(trimToJSON(output), &raw); err != nil {
		return nil, fmt.Errorf("parse PHP %s settings: %w", version, err)
	}
	managed, err := o.readDropIn(version)
	if err != nil {
		return nil, err
	}
	directives := make([]Directive, 0, len(raw))
	for _, item := range raw {
		_, isManaged := managed[item.Name]
		directives = append(directives, Directive{
			Name:    item.Name,
			Module:  item.Module,
			Value:   item.Value,
			Access:  accessLabel(item.Access),
			Managed: isManaged,
		})
	}
	sort.Slice(directives, func(i, j int) bool { return directives[i].Name < directives[j].Name })
	return directives, nil
}

// readDropIn parses nexa's managed conf.d file for a branch into a directive
// map. A missing file just means nothing has been overridden yet.
func (o *HostOperator) readDropIn(version string) (map[string]string, error) {
	return readININil(o.managedDropInPath(version))
}

// readININil reads a "key = value" ini file into a map. A missing file is not an
// error. Only well-shaped, single-line directives are honoured; comments and
// blanks are ignored, so re-reading a file nexa wrote round-trips exactly.
func readININil(path string) (map[string]string, error) {
	content, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	values := map[string]string{}
	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, ";") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if !directivePattern.MatchString(key) {
			continue
		}
		values[key] = strings.TrimSpace(value)
	}
	return values, nil
}

// trimToJSON drops any leading warning noise PHP may print before the JSON
// payload (a startup notice can precede -r output), keeping from the first
// bracket onward.
func trimToJSON(output []byte) []byte {
	if index := strings.IndexByte(string(output), '['); index > 0 {
		return output[index:]
	}
	return output
}

func sortVersions(versions []string) {
	sort.Slice(versions, func(i, j int) bool { return comparePHPVersions(versions[i], versions[j]) < 0 })
}

// comparePHPVersions orders dotted numeric versions field by field so "8.10"
// sorts above "8.9".
func comparePHPVersions(a, b string) int {
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
