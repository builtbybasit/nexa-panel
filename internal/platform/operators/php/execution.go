package php

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
)

// Apply re-validates and executes a signed plan. It rejects an expired or
// wrong-kind plan, re-derives the change from the plan (never trusting the
// plan's own package list for the command line), rejects if node state drifted
// since planning, then dispatches on the action. The mutex serialises applies
// because a branch's config and FPM master are shared.
func (o *HostOperator) Apply(ctx context.Context, plan Plan) (Observation, error) {
	o.applyMu.Lock()
	defer o.applyMu.Unlock()

	if plan.ID == "" || plan.Kind != PlanKind || o.now().UTC().After(plan.ExpiresAt) {
		return Observation{}, errors.New("PHP plan is invalid or expired")
	}
	change, err := o.normalizeChange(ctx, plan.Change)
	if err != nil {
		return Observation{}, err
	}
	current, err := o.fingerprintFor(ctx, change)
	if err != nil {
		return Observation{}, err
	}
	if current != plan.ObservedFingerprint {
		return Observation{}, errors.New("PHP state changed after planning; refresh and try again")
	}
	switch change.Action {
	case ActionInstallExtension:
		return o.installExtension(ctx, change)
	case ActionRemoveExtension:
		return o.removeExtension(ctx, change)
	case ActionSaveSettings:
		return o.saveSettings(ctx, change)
	case ActionSaveSiteSettings:
		return o.saveSiteSettings(ctx, change)
	default:
		return Observation{}, errors.New("PHP action is unsupported")
	}
}

// saveSiteSettings merges the requested overrides into a site's .user.ini and
// returns the site's directives as they now read. No FPM reload is needed: PHP
// re-reads .user.ini per-directory on its own user_ini refresh cycle.
func (o *HostOperator) saveSiteSettings(ctx context.Context, change Change) (Observation, error) {
	scope := *change.Site
	values, err := readININil(userIniPath(scope.RootPath))
	if err != nil {
		return Observation{}, err
	}
	for key, value := range change.Set {
		values[key] = value
	}
	for _, key := range change.Reset {
		delete(values, key)
	}
	if err := o.writeUserIni(scope, values); err != nil {
		return Observation{}, err
	}
	directives, err := o.SiteSettings(ctx, scope)
	if err != nil {
		return Observation{}, err
	}
	return Observation{Action: change.Action, Version: scope.Version, Directives: directives, Verified: true}, nil
}

func (o *HostOperator) installExtension(ctx context.Context, change Change) (Observation, error) {
	pkg := extensionPackage(change.Version, change.Extension)
	if err := o.aptUpdate(ctx); err != nil {
		return Observation{}, err
	}
	if output, err := o.runner.Run(ctx, aptCommand("apt-get", "install", "-y", "--no-install-recommends", pkg)); err != nil {
		return Observation{}, commandError("install "+pkg, output, err)
	}
	if err := o.restartFPM(ctx, change.Version); err != nil {
		return Observation{}, err
	}
	return o.verifyExtension(ctx, change, true)
}

func (o *HostOperator) removeExtension(ctx context.Context, change Change) (Observation, error) {
	pkg := extensionPackage(change.Version, change.Extension)
	if output, err := o.runner.Run(ctx, aptCommand("apt-get", "purge", "-y", pkg)); err != nil {
		return Observation{}, commandError("remove "+pkg, output, err)
	}
	_, _ = o.runner.Run(ctx, aptCommand("apt-get", "autoremove", "-y"))
	if err := o.restartFPM(ctx, change.Version); err != nil {
		return Observation{}, err
	}
	return o.verifyExtension(ctx, change, false)
}

// verifyExtension re-discovers the branch's modules and confirms the target
// reached the intended install state.
func (o *HostOperator) verifyExtension(ctx context.Context, change Change, wantInstalled bool) (Observation, error) {
	extensions, err := o.Extensions(ctx, change.Version)
	if err != nil {
		return Observation{}, err
	}
	for _, extension := range extensions {
		if extension.Name != change.Extension {
			continue
		}
		if extension.Installed != wantInstalled {
			state := "install"
			if !wantInstalled {
				state = "removal"
			}
			return Observation{}, fmt.Errorf("%s did not reach the expected state after %s", extension.Package, state)
		}
		found := extension
		return Observation{Action: change.Action, Version: change.Version, Extension: &found, Verified: true}, nil
	}
	return Observation{}, fmt.Errorf("php%s-%s is no longer offered by this node", change.Version, change.Extension)
}

// saveSettings merges the requested directives into nexa's managed drop-in,
// reloads FPM, and returns the branch's directives as they now read. A drop-in
// that ends up empty is removed so the branch falls back cleanly to its defaults.
func (o *HostOperator) saveSettings(ctx context.Context, change Change) (Observation, error) {
	values, err := o.readDropIn(change.Version)
	if err != nil {
		return Observation{}, err
	}
	for key, value := range change.Set {
		values[key] = value
	}
	for _, key := range change.Reset {
		delete(values, key)
	}
	if err := o.writeDropIn(change.Version, values); err != nil {
		return Observation{}, err
	}
	if err := o.reloadFPM(ctx, change.Version); err != nil {
		return Observation{}, err
	}
	directives, err := o.Settings(ctx, change.Version)
	if err != nil {
		return Observation{}, err
	}
	return Observation{Action: change.Action, Version: change.Version, Directives: directives, Verified: true}, nil
}

// writeDropIn rewrites nexa's managed conf.d file from the full desired override
// set. Keys are sorted so the file is stable across saves. An empty set removes
// the file entirely rather than leaving an empty stub behind.
func (o *HostOperator) writeDropIn(version string, values map[string]string) error {
	path := o.managedDropInPath(version)
	if len(values) == 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove managed PHP drop-in: %w", err)
		}
		return nil
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var builder strings.Builder
	builder.WriteString("; Managed by Nexa Panel. Do not edit by hand; changes are overwritten.\n")
	for _, key := range keys {
		builder.WriteString(key)
		builder.WriteString(" = ")
		builder.WriteString(values[key])
		builder.WriteByte('\n')
	}
	if err := os.MkdirAll(o.fpmConfigDir(version), 0o755); err != nil {
		return fmt.Errorf("ensure PHP %s conf.d exists: %w", version, err)
	}
	if err := os.WriteFile(path, []byte(builder.String()), 0o644); err != nil {
		return fmt.Errorf("write managed PHP drop-in: %w", err)
	}
	return nil
}

// aptUpdate refreshes the package index before an install: an image that cleaned
// /var/lib/apt/lists otherwise fails with "Unable to locate package".
func (o *HostOperator) aptUpdate(ctx context.Context) error {
	if output, err := o.runner.Run(ctx, aptCommand("apt-get", "update")); err != nil {
		return commandError("update the apt package index", output, err)
	}
	return nil
}

// restartFPM restarts a branch's FPM master. A newly installed or removed module
// only takes effect on a full restart, since extensions load at startup.
func (o *HostOperator) restartFPM(ctx context.Context, version string) error {
	if output, err := o.runner.Run(ctx, Command{Name: "systemctl", Args: []string{"restart", fpmUnit(version)}}); err != nil {
		return commandError("restart "+fpmUnit(version), output, err)
	}
	return nil
}

// reloadFPM reloads a branch's FPM master. A reload re-reads the ini files
// gracefully, which is enough for a directive change (no new module to load).
func (o *HostOperator) reloadFPM(ctx context.Context, version string) error {
	if output, err := o.runner.Run(ctx, Command{Name: "systemctl", Args: []string{"reload", fpmUnit(version)}}); err != nil {
		return commandError("reload "+fpmUnit(version), output, err)
	}
	return nil
}
