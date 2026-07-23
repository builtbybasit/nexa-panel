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
	// Configuring a repository needs tooling of its own — gpg and curl, or
	// software-properties-common — and on a minimal image that cleaned
	// /var/lib/apt/lists there is no index to install it from ("Package 'gnupg'
	// has no installation candidate"). Refresh before touching the repository,
	// not only before the install below.
	if entry.Repo != repoNone {
		if err := o.aptUpdate(ctx); err != nil {
			return Observation{}, err
		}
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
	if err := o.aptUpdate(ctx); err != nil {
		return Observation{}, err
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
	// A vendor repository outlives the server it was added for unless it is
	// dropped here: nothing else on the node uses it, but every apt-get update
	// would keep fetching from a third-party mirror, and it would still be sitting
	// there offering packages if a different engine were installed later.
	if entry.Repo == repoDeclaredDB {
		if err := o.removeDatabaseRepo(ctx, entry); err != nil {
			return Observation{}, err
		}
	}
	return o.verify(ctx, change, entry)
}

// aptUpdate refreshes the package index.
func (o *HostOperator) aptUpdate(ctx context.Context) error {
	if output, err := o.runner.Run(ctx, command("apt-get", "update")); err != nil {
		return commandError("update the apt package index", output, err)
	}
	return nil
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
		// add-apt-repository resolves the PPA through launchpadlib, which
		// insists on caching into $HOME/.launchpadlib. Under the agent's
		// ProtectHome=true that is /root and read-only, so it aborts before it
		// reaches the network; give it a home it is allowed to write.
		home, err := o.ensureToolHome()
		if err != nil {
			return err
		}
		add := command("add-apt-repository", "-y", "ppa:ondrej/php")
		add.Env = append(add.Env, "HOME="+home)
		if output, err := o.runner.Run(ctx, add); err != nil {
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
	case repoDeclaredDB:
		return o.ensureDatabaseRepo(ctx, entry)
	default:
		return fmt.Errorf("unknown repository for %q", entry.App)
	}
}

// ensureDatabaseRepo configures exactly the repository the requested series
// needs. The vendor's sources file is rewritten — or removed, for a series
// Ubuntu itself ships — so that it always names a single series: leaving another
// series' repository configured would let a later `apt upgrade` walk the server
// across a major version on its own.
func (o *HostOperator) ensureDatabaseRepo(ctx context.Context, entry catalogEntry) error {
	vendor, ok := dbVendors[entry.App]
	if !ok {
		return fmt.Errorf("unknown database vendor %q", entry.App)
	}
	series, ok := dbSeriesFor(entry.App, entry.Version)
	if !ok {
		return fmt.Errorf("unknown %s series %q", entry.App, entry.Version)
	}
	if series.repoURL == "" {
		return o.removeDatabaseRepo(ctx, entry)
	}
	codename, err := o.codename(ctx)
	if err != nil {
		return err
	}
	if output, err := o.runner.Run(ctx, command("apt-get", "install", "-y", "--no-install-recommends", "curl", "ca-certificates", "gnupg")); err != nil {
		return commandError("install repository tooling", output, err)
	}
	script := command("sh", "-c", addRepoScript, "nexa-add-repo",
		vendor.keyURL, vendor.fingerprint, vendor.keyringPath,
		series.repoURL, codename, series.component, vendor.listPath)
	if output, err := o.runner.Run(ctx, script); err != nil {
		return commandError("configure the "+entry.Label+" repository", output, err)
	}
	return nil
}

// removeDatabaseRepo drops nexa's sources file for a vendor, leaving the node
// with only Ubuntu's own archive for that engine.
func (o *HostOperator) removeDatabaseRepo(ctx context.Context, entry catalogEntry) error {
	vendor, ok := dbVendors[entry.App]
	if !ok {
		return fmt.Errorf("unknown database vendor %q", entry.App)
	}
	if output, err := o.runner.Run(ctx, command("rm", "-f", vendor.listPath)); err != nil {
		return commandError("remove the "+entry.App+" vendor repository", output, err)
	}
	return nil
}

// addRepoScript fetches a vendor signing key and refuses it unless it matches the
// pinned fingerprint and has not expired, then writes the keyring and a sources
// file naming exactly one series. The fingerprint check is the trust anchor —
// everything apt subsequently accepts from this repository rests on it, so the
// key is verified before it is dearmored, never after. The expiry check is
// separate and equally load-bearing: a key can match the pin and still be
// expired (MySQL's published key has been), and apt reports that only as the
// misleading "the repository is not signed".
//
// Every value is either compiled in or the node's own codename; nothing from the
// caller reaches this script, and each is passed positionally rather than
// interpolated.
const addRepoScript = `
set -eu
key_url="$1"; want_fpr="$2"; keyring="$3"; repo_url="$4"; codename="$5"; component="$6"; list="$7"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
# gpg insists on a writable home directory even to read a key out of a file,
# and the agent unit sets ProtectHome=true, so $HOME/.gnupg cannot be created:
# every gpg call died with "Fatal: can't create directory '/root/.gnupg'". That
# emptied got_fpr, so the failure surfaced as the misleading "downloaded none"
# fingerprint mismatch. Give gpg a private home inside the work directory
# rather than widening the unit's sandbox for it.
GNUPGHOME="$work/gnupg"
export GNUPGHOME
mkdir -m 0700 "$GNUPGHOME"
tmp="$work/key"
curl -fsSL --proto '=https' --tlsv1.2 -o "$tmp" "$key_url"
got_fpr="$(gpg --show-keys --with-colons --with-fingerprint "$tmp" | awk -F: '/^fpr/{print $10; exit}')"
if [ "$got_fpr" != "$want_fpr" ]; then
  echo "signing key fingerprint mismatch: downloaded ${got_fpr:-none}, pinned ${want_fpr}" >&2
  exit 1
fi
if [ "$(gpg --show-keys --with-colons "$tmp" | awk -F: '/^pub/{print $2; exit}')" = "e" ]; then
  echo "signing key ${want_fpr} has expired; the vendor renews it under a new URL" >&2
  exit 1
fi
install -d -m 0755 "$(dirname "$keyring")"
rm -f "$keyring"
gpg --batch --yes --dearmor -o "$keyring" "$tmp"
chmod 0644 "$keyring"
install -d -m 0755 "$(dirname "$list")"
printf 'deb [signed-by=%s] %s %s %s\n' "$keyring" "$repo_url" "$codename" "$component" > "$list"
chmod 0644 "$list"
`

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
	if err := o.aptUpdate(ctx); err != nil {
		return err
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
		// For MySQL/MariaDB the package name proves nothing: every series ships
		// the same one, so apt can report success having installed a different
		// series entirely — which is exactly what happens when the vendor
		// repository contributes nothing (it may not publish this architecture)
		// and the base repo's older server satisfies the request instead. Only
		// the landed version distinguishes them, and Discover reports the
		// identity solely when that version is in this series.
		if entry.Repo == repoDeclaredDB {
			if _, ok := byName[entry.identity()]; !ok {
				return Observation{}, fmt.Errorf(
					"%s reported installed, but the server is version %q, which is not the %s series",
					entry.Label, installedVersion, entry.Version,
				)
			}
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
