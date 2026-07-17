package packages

import (
	"context"
	"errors"
	"fmt"
	"strconv"
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
	change, entry, err := o.normalize(ctx, plan.Change)
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
	if entry.Method == methodNVM {
		return o.installNode(ctx, change, entry)
	}
	if err := o.ensureRepo(ctx, entry); err != nil {
		return Observation{}, err
	}
	// Adding a repository changes what this node can install, so the enumerated
	// catalog is stale from here on.
	o.invalidateCatalog()
	// Always refresh the index before installing: the package list may be empty
	// or stale (e.g. a minimal image that cleaned /var/lib/apt/lists), which
	// otherwise surfaces as "Unable to locate package" even for base-repo apps.
	if output, err := o.runner.Run(ctx, command("apt-get", "update")); err != nil {
		return Observation{}, commandError("update the apt package index", output, err)
	}
	args := append([]string{"install", "-y", "--no-install-recommends"}, entry.Packages...)
	if output, err := o.runner.Run(ctx, command("apt-get", args...)); err != nil {
		return Observation{}, commandError("install "+entry.Label, output, err)
	}
	return o.verify(ctx, change, entry)
}

func (o *HostOperator) remove(ctx context.Context, change Change, entry catalogEntry) (Observation, error) {
	if entry.Method == methodNVM {
		return o.removeNode(ctx, change, entry)
	}
	args := append([]string{"purge", "-y"}, entry.Packages...)
	if output, err := o.runner.Run(ctx, command("apt-get", args...)); err != nil {
		return Observation{}, commandError("remove "+entry.Label, output, err)
	}
	_, _ = o.runner.Run(ctx, command("apt-get", "autoremove", "-y"))
	return o.verify(ctx, change, entry)
}

// ensureRepo configures the fixed repository an apt entry requires. Every
// command is typed with fixed arguments; nothing from the request reaches it.
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
	default:
		return fmt.Errorf("unknown repository for %q", entry.App)
	}
}

const (
	// nvmDir is the node-wide nvm home the agent manages. nvmVersion pins the
	// nvm release so the installer is reproducible and not a live curl|bash.
	nvmDir     = "/opt/nvm"
	nvmVersion = "v0.40.3"
	// nvmLoad sources nvm; the validated major is passed as $1 (never
	// interpolated into the script) to keep the shell call injection-safe.
	nvmLoad = `export NVM_DIR="` + nvmDir + `"; . "$NVM_DIR/nvm.sh"`
)

func (o *HostOperator) installNode(ctx context.Context, change Change, entry catalogEntry) (Observation, error) {
	major, err := nodeMajor(entry.Version)
	if err != nil {
		return Observation{}, err
	}
	if err := o.ensureNVM(ctx); err != nil {
		return Observation{}, err
	}
	script := nvmLoad + ` && nvm install "$1"`
	if output, err := o.runner.Run(ctx, nvmCommand(script, strconv.Itoa(major))); err != nil {
		return Observation{}, commandError("install "+entry.Label+" via nvm", output, err)
	}
	return o.verify(ctx, change, entry)
}

func (o *HostOperator) removeNode(ctx context.Context, change Change, entry catalogEntry) (Observation, error) {
	major, err := nodeMajor(entry.Version)
	if err != nil {
		return Observation{}, err
	}
	script := nvmLoad + ` && v="$(nvm version "$1")" && [ "$v" != "N/A" ] && nvm uninstall "$v"`
	if output, err := o.runner.Run(ctx, nvmCommand(script, strconv.Itoa(major))); err != nil {
		return Observation{}, commandError("remove "+entry.Label+" via nvm", output, err)
	}
	return o.verify(ctx, change, entry)
}

// ensureNVM installs the pinned nvm release into nvmDir if absent, after its
// git/curl prerequisites. nvm then downloads prebuilt Node.js runtimes.
func (o *HostOperator) ensureNVM(ctx context.Context) error {
	if _, err := o.runner.Run(ctx, command("test", "-s", nvmDir+"/nvm.sh")); err == nil {
		return nil
	}
	if output, err := o.runner.Run(ctx, command("apt-get", "update")); err != nil {
		return commandError("update the apt package index", output, err)
	}
	if output, err := o.runner.Run(ctx, command("apt-get", "install", "-y", "--no-install-recommends", "git", "curl", "ca-certificates")); err != nil {
		return commandError("install nvm prerequisites", output, err)
	}
	if output, err := o.runner.Run(ctx, command("git", "clone", "--depth", "1", "--branch", nvmVersion, "https://github.com/nvm-sh/nvm.git", nvmDir)); err != nil {
		return commandError("install nvm", output, err)
	}
	return nil
}

// nvmCommand builds a bash invocation that sources nvm and runs script with the
// given positional argument as $1.
func nvmCommand(script, arg string) Command {
	cmd := command("bash", "-c", script, "nvm", arg)
	cmd.Env = append(cmd.Env, "NVM_DIR="+nvmDir)
	return cmd
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
