package php

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// planExpiry bounds how long a reviewed PHP plan may sit before it must be
// regenerated, matching the other operators' short windows. PHP plans are
// planned and applied back-to-back within one control-plane job, so this is a
// safety bound rather than a review budget.
const planExpiry = 10 * time.Minute

// maxDirectiveValue caps a directive value. It is generous (paths, lists) but
// bounded so a single ini line cannot grow without limit.
const maxDirectiveValue = 1024

// Plan validates a change against the node's real state and returns a reviewable,
// human-readable plan. Signing happens in the agent handler, not here.
func (o *HostOperator) Plan(ctx context.Context, change Change) (Plan, error) {
	change, err := o.normalizeChange(ctx, change)
	if err != nil {
		return Plan{}, err
	}
	fingerprint, err := o.fingerprintFor(ctx, change)
	if err != nil {
		return Plan{}, err
	}
	packages, steps, warnings := o.narrative(change)
	now := o.now().UTC()
	return Plan{
		ID: randomID(), Kind: PlanKind, Change: change, Packages: packages,
		Steps: steps, Warnings: warnings, ObservedFingerprint: fingerprint,
		PlannedAt: now, ExpiresAt: now.Add(planExpiry),
	}, nil
}

// normalizeChange is the security boundary. Every field that will reach a
// command line, a file path, or an ini line is trimmed and re-validated here:
// the branch must be installed, an extension must be one the node offers, and a
// directive must be one PHP actually reports with a safe, single-line value.
func (o *HostOperator) normalizeChange(ctx context.Context, change Change) (Change, error) {
	version, err := o.normalizeVersion(ctx, change.Version)
	if err != nil {
		return Change{}, err
	}
	change.Version = version
	switch change.Action {
	case ActionInstallExtension, ActionRemoveExtension:
		return o.normalizeExtensionChange(ctx, change)
	case ActionSaveSettings:
		return o.normalizeSettingsChange(ctx, change)
	case ActionSaveSiteSettings:
		return o.normalizeSiteSettingsChange(ctx, change)
	default:
		return Change{}, errors.New("PHP action must install/remove an extension or save settings")
	}
}

// normalizeSiteSettingsChange validates a per-site override change: the scope
// against the sites root, and each directive against the editable subset the
// site's branch actually offers, with single-line values.
func (o *HostOperator) normalizeSiteSettingsChange(ctx context.Context, change Change) (Change, error) {
	if change.Site == nil {
		return Change{}, errors.New("a site scope is required for a per-site settings change")
	}
	scope, err := o.normalizeSiteScope(ctx, *change.Site)
	if err != nil {
		return Change{}, err
	}
	// The change's top-level version must agree with the site's branch.
	if change.Version != "" && strings.TrimSpace(change.Version) != scope.Version {
		return Change{}, errors.New("site settings version does not match the site's PHP branch")
	}
	editable, err := o.SiteSettings(ctx, scope)
	if err != nil {
		return Change{}, err
	}
	known := make(map[string]struct{}, len(editable))
	for _, directive := range editable {
		known[directive.Name] = struct{}{}
	}
	set := map[string]string{}
	for key, value := range change.Set {
		key = strings.TrimSpace(key)
		if !directivePattern.MatchString(key) {
			return Change{}, fmt.Errorf("%q is not a valid php.ini directive", key)
		}
		if _, ok := known[key]; !ok {
			return Change{}, fmt.Errorf("php.ini directive %q cannot be overridden per-site on PHP %s", key, scope.Version)
		}
		if err := validateValue(value); err != nil {
			return Change{}, fmt.Errorf("value for %q is invalid: %w", key, err)
		}
		set[key] = value
	}
	reset := []string{}
	seen := map[string]struct{}{}
	for _, key := range change.Reset {
		key = strings.TrimSpace(key)
		if !directivePattern.MatchString(key) {
			return Change{}, fmt.Errorf("%q is not a valid php.ini directive", key)
		}
		if _, dup := seen[key]; dup {
			continue
		}
		if _, overwritten := set[key]; overwritten {
			continue
		}
		seen[key] = struct{}{}
		reset = append(reset, key)
	}
	if len(set) == 0 && len(reset) == 0 {
		return Change{}, errors.New("no per-site php.ini changes were requested")
	}
	change.Site = &scope
	change.Version = scope.Version
	change.Extension = ""
	change.Set, change.Reset = set, reset
	return change, nil
}

func (o *HostOperator) normalizeExtensionChange(ctx context.Context, change Change) (Change, error) {
	name := strings.TrimSpace(change.Extension)
	if !extensionPattern.MatchString(name) {
		return Change{}, fmt.Errorf("PHP extension %q is not a valid module name", change.Extension)
	}
	if _, skip := nonExtensionPackages[name]; skip {
		return Change{}, fmt.Errorf("%q is a PHP runtime package, not an installable module", name)
	}
	available, err := o.Extensions(ctx, change.Version)
	if err != nil {
		return Change{}, err
	}
	for _, extension := range available {
		if extension.Name == name {
			change.Extension = name
			change.Set, change.Reset = nil, nil
			return change, nil
		}
	}
	return Change{}, fmt.Errorf("PHP %s does not offer a %q module in this node's repositories", change.Version, name)
}

func (o *HostOperator) normalizeSettingsChange(ctx context.Context, change Change) (Change, error) {
	directives, err := o.Settings(ctx, change.Version)
	if err != nil {
		return Change{}, err
	}
	known := make(map[string]struct{}, len(directives))
	for _, directive := range directives {
		known[directive.Name] = struct{}{}
	}
	set := map[string]string{}
	for key, value := range change.Set {
		key = strings.TrimSpace(key)
		if !directivePattern.MatchString(key) {
			return Change{}, fmt.Errorf("%q is not a valid php.ini directive", key)
		}
		if _, ok := known[key]; !ok {
			return Change{}, fmt.Errorf("php.ini directive %q is not recognised by PHP %s", key, change.Version)
		}
		if err := validateValue(value); err != nil {
			return Change{}, fmt.Errorf("value for %q is invalid: %w", key, err)
		}
		set[key] = value
	}
	reset := []string{}
	seen := map[string]struct{}{}
	for _, key := range change.Reset {
		key = strings.TrimSpace(key)
		if !directivePattern.MatchString(key) {
			return Change{}, fmt.Errorf("%q is not a valid php.ini directive", key)
		}
		if _, dup := seen[key]; dup {
			continue
		}
		if _, overwritten := set[key]; overwritten {
			// A directive both set and reset in one request is contradictory; the
			// explicit value wins and the reset is dropped.
			continue
		}
		seen[key] = struct{}{}
		reset = append(reset, key)
	}
	if len(set) == 0 && len(reset) == 0 {
		return Change{}, errors.New("no php.ini changes were requested")
	}
	change.Extension = ""
	change.Set, change.Reset = set, reset
	return change, nil
}

// validateValue rejects anything that could break out of a single ini line or
// bloat the file. Values may hold spaces, symbols, and paths, but never a
// newline, carriage return, or NUL.
func validateValue(value string) error {
	if len(value) > maxDirectiveValue {
		return fmt.Errorf("value exceeds %d characters", maxDirectiveValue)
	}
	if strings.ContainsAny(value, "\n\r\x00") {
		return errors.New("value must be a single line")
	}
	return nil
}

// fingerprintFor computes the TOCTOU guard for a change: the installed-extension
// set for extension actions, the current drop-in for settings. Apply refuses if
// this changed since planning.
func (o *HostOperator) fingerprintFor(ctx context.Context, change Change) (string, error) {
	switch change.Action {
	case ActionInstallExtension, ActionRemoveExtension:
		return o.extensionFingerprint(ctx, change.Version)
	case ActionSaveSiteSettings:
		return o.siteSettingsFingerprint(*change.Site)
	default:
		return o.settingsFingerprint(change.Version)
	}
}

func (o *HostOperator) siteSettingsFingerprint(scope SiteScope) (string, error) {
	values, err := readININil(userIniPath(scope.RootPath))
	if err != nil {
		return "", err
	}
	pairs := make([][2]string, 0, len(values))
	for _, key := range sortedKeys(values) {
		pairs = append(pairs, [2]string{key, values[key]})
	}
	return fingerprint(pairs)
}

func (o *HostOperator) extensionFingerprint(ctx context.Context, version string) (string, error) {
	extensions, err := o.Extensions(ctx, version)
	if err != nil {
		return "", err
	}
	installed := []string{}
	for _, extension := range extensions {
		if extension.Installed {
			installed = append(installed, extension.Package)
		}
	}
	sort.Strings(installed)
	return fingerprint(installed)
}

func (o *HostOperator) settingsFingerprint(version string) (string, error) {
	values, err := o.readDropIn(version)
	if err != nil {
		return "", err
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	pairs := make([][2]string, 0, len(keys))
	for _, key := range keys {
		pairs = append(pairs, [2]string{key, values[key]})
	}
	return fingerprint(pairs)
}

// narrative builds the operator-facing package list, step list, and warnings.
func (o *HostOperator) narrative(change Change) ([]string, []string, []string) {
	switch change.Action {
	case ActionInstallExtension:
		pkg := extensionPackage(change.Version, change.Extension)
		return []string{pkg},
			[]string{
				"Refresh the apt package index.",
				"Install " + pkg + ".",
				"Restart " + fpmUnit(change.Version) + " so the module loads.",
				"Verify the module reports as installed.",
			}, nil
	case ActionRemoveExtension:
		pkg := extensionPackage(change.Version, change.Extension)
		return []string{pkg},
			[]string{
				"Purge " + pkg + ".",
				"Restart " + fpmUnit(change.Version) + ".",
				"Verify the module is no longer installed.",
			}, []string{"Removing " + pkg + " may break sites or tools that rely on it."}
	case ActionSaveSiteSettings:
		steps := []string{}
		if len(change.Set) > 0 {
			keys := sortedKeys(change.Set)
			steps = append(steps, "Set "+strconv.Itoa(len(keys))+" per-site directive(s): "+strings.Join(keys, ", ")+".")
		}
		if len(change.Reset) > 0 {
			reset := append([]string(nil), change.Reset...)
			sort.Strings(reset)
			steps = append(steps, "Clear "+strconv.Itoa(len(reset))+" per-site override(s): "+strings.Join(reset, ", ")+".")
		}
		steps = append(
			steps,
			"Write "+change.Site.Slug+"'s .user.ini in its document root.",
			"Overrides take effect on the next PHP-FPM user_ini refresh (within its cache TTL).",
		)
		return nil, steps, settingsWarnings(change)
	default:
		steps := []string{}
		if len(change.Set) > 0 {
			keys := sortedKeys(change.Set)
			steps = append(steps, "Set "+strconv.Itoa(len(keys))+" php.ini directive(s): "+strings.Join(keys, ", ")+".")
		}
		if len(change.Reset) > 0 {
			reset := append([]string(nil), change.Reset...)
			sort.Strings(reset)
			steps = append(steps, "Reset "+strconv.Itoa(len(reset))+" directive(s) to their default: "+strings.Join(reset, ", ")+".")
		}
		steps = append(
			steps,
			"Write nexa's managed drop-in for "+fpmUnit(change.Version)+".",
			"Reload "+fpmUnit(change.Version)+" to apply.",
		)
		return nil, steps, settingsWarnings(change)
	}
}

// settingsWarnings flags directive changes an operator should weigh before
// saving. The list is deliberately short: values are free text, so only a few
// well-known safety switches are called out.
func settingsWarnings(change Change) []string {
	warnings := []string{}
	for key, value := range change.Set {
		normalized := strings.ToLower(strings.TrimSpace(value))
		switch key {
		case "allow_url_fopen", "allow_url_include":
			if normalized == "1" || normalized == "on" || normalized == "true" {
				warnings = append(warnings, "Enabling "+key+" widens the surface for remote-file attacks.")
			}
		case "display_errors":
			if normalized == "1" || normalized == "on" || normalized == "true" {
				warnings = append(warnings, "Enabling display_errors can leak sensitive information to visitors; prefer logging.")
			}
		}
	}
	sort.Strings(warnings)
	return warnings
}

func sortedKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
