package packages

import (
	"context"
	"errors"
	"fmt"
)

// Apply installs or removes a catalog application after re-validating the plan.
// It rejects an expired, wrong-kind, or empty plan; re-discovers state and
// rejects if the installed set drifted since planning; then runs apt and
// verifies the result. Package names are re-derived from the catalog via
// normalize — the plan's own package list is never trusted for the command line.
func (o *HostOperator) Apply(ctx context.Context, plan Plan) (Observation, error) {
	if plan.ID == "" || plan.Kind != PlanKind || o.now().UTC().After(plan.ExpiresAt) {
		return Observation{}, errors.New("application plan is invalid or expired")
	}
	change, entry, err := normalize(plan.Change)
	if err != nil {
		return Observation{}, err
	}
	installed, err := o.Discover(ctx)
	if err != nil {
		return Observation{}, err
	}
	current, err := fingerprintPackages(installed)
	if err != nil {
		return Observation{}, err
	}
	if current != plan.ObservedFingerprint {
		return Observation{}, errors.New("installed package state changed after planning")
	}
	switch change.Action {
	case ActionInstall:
		return o.install(ctx, change, entry)
	case ActionRemove:
		return o.remove(ctx, change, entry)
	default:
		return Observation{}, errors.New("application action is unsupported")
	}
}

func (o *HostOperator) install(ctx context.Context, change Change, entry catalogEntry) (Observation, error) {
	if err := o.ensureRepo(ctx, entry); err != nil {
		return Observation{}, err
	}
	if entry.Repo != repoNone {
		if output, err := o.runner.Run(ctx, command("apt-get", "update")); err != nil {
			return Observation{}, commandError("update the apt package index", output, err)
		}
	}
	args := append([]string{"install", "-y", "--no-install-recommends"}, entry.Packages...)
	if output, err := o.runner.Run(ctx, command("apt-get", args...)); err != nil {
		return Observation{}, commandError("install "+entry.Label, output, err)
	}
	return o.verify(ctx, change, entry)
}

func (o *HostOperator) remove(ctx context.Context, change Change, entry catalogEntry) (Observation, error) {
	args := append([]string{"purge", "-y"}, entry.Packages...)
	if output, err := o.runner.Run(ctx, command("apt-get", args...)); err != nil {
		return Observation{}, commandError("remove "+entry.Label, output, err)
	}
	_, _ = o.runner.Run(ctx, command("apt-get", "autoremove", "-y"))
	return o.verify(ctx, change, entry)
}

// ensureRepo configures the fixed repository an entry requires. Every command
// is typed with fixed arguments; only the validated Node.js major is
// interpolated, and only after re-validation.
func (o *HostOperator) ensureRepo(ctx context.Context, entry catalogEntry) error {
	switch entry.Repo {
	case repoNone:
		return nil
	case repoOndrejPHP:
		if output, err := o.runner.Run(ctx, command("apt-get", "install", "-y", "--no-install-recommends", "software-properties-common")); err != nil {
			return commandError("install repository tooling", output, err)
		}
		if output, err := o.runner.Run(ctx, command("add-apt-repository", "-y", "ppa:ondrej/php")); err != nil {
			return commandError("add the ondrej/php repository", output, err)
		}
		return nil
	case repoPGDG:
		if output, err := o.runner.Run(ctx, command("apt-get", "install", "-y", "--no-install-recommends", "postgresql-common")); err != nil {
			return commandError("install PostgreSQL repository tooling", output, err)
		}
		if output, err := o.runner.Run(ctx, command("/usr/share/postgresql-common/pgdg/apt.postgresql.org.sh", "-y")); err != nil {
			return commandError("add the PostgreSQL PGDG repository", output, err)
		}
		return nil
	case repoNodeSource:
		return o.ensureNodeSource(ctx, entry)
	default:
		return fmt.Errorf("unknown repository for %q", entry.App)
	}
}

const nodeKeyringPath = "/etc/apt/keyrings/nodesource.asc"

func (o *HostOperator) ensureNodeSource(ctx context.Context, entry catalogEntry) error {
	major, err := nodeMajor(entry.Version)
	if err != nil {
		return err
	}
	if output, err := o.runner.Run(ctx, command("apt-get", "install", "-y", "--no-install-recommends", "curl", "ca-certificates", "gnupg")); err != nil {
		return commandError("install repository tooling", output, err)
	}
	if output, err := o.runner.Run(ctx, command("install", "-d", "-m", "0755", "/etc/apt/keyrings")); err != nil {
		return commandError("prepare the apt keyring directory", output, err)
	}
	if output, err := o.runner.Run(ctx, command("curl", "-fsSL", "https://deb.nodesource.com/gpgkey/nodesource-repo.gpg.key", "-o", nodeKeyringPath)); err != nil {
		return commandError("download the NodeSource signing key", output, err)
	}
	list := fmt.Sprintf("deb [signed-by=%s] https://deb.nodesource.com/node_%d.x nodistro main\n", nodeKeyringPath, major)
	if err := secureWrite("/etc/apt/sources.list.d/nodesource.list", []byte(list), 0o644); err != nil {
		return fmt.Errorf("write the NodeSource repository: %w", err)
	}
	return nil
}

// verify re-discovers state and confirms the entry's packages reached the
// intended install state.
func (o *HostOperator) verify(ctx context.Context, change Change, entry catalogEntry) (Observation, error) {
	installed, err := o.Discover(ctx)
	if err != nil {
		return Observation{}, err
	}
	byName := make(map[string]InstalledPackage, len(installed))
	for _, item := range installed {
		byName[item.Name] = item
	}
	present := []InstalledPackage{}
	installedVersion := ""
	allInstalled := true
	for _, name := range entry.Packages {
		item, ok := byName[name]
		if ok && item.Installed {
			present = append(present, item)
			if installedVersion == "" {
				installedVersion = item.Version
			}
		} else {
			allInstalled = false
		}
	}
	observation := Observation{App: entry.App, Version: change.Version, Packages: present}
	switch change.Action {
	case ActionInstall:
		if !allInstalled {
			return Observation{}, fmt.Errorf("%s did not report all packages installed after apt", entry.Label)
		}
		if installedVersion != "" {
			observation.Version = installedVersion
		}
		observation.Installed = true
		observation.Verified = true
	case ActionRemove:
		if allInstalled {
			return Observation{}, fmt.Errorf("%s remained installed after removal", entry.Label)
		}
		observation.Installed = false
		observation.Verified = true
	}
	return observation, nil
}
