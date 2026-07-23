// Package selfupdate is the privileged node operator behind the panel's
// self-update feature. Only nexa-agent (User=root, /usr writable, full caps) can
// replace /usr/bin/nexa and restart the units, so this operator lives on the
// agent side; the control plane reaches it over the same authenticated unix
// socket as every other operator.
//
// The operation is deliberately narrow. The only thing a caller may name is a
// target version string, gated to a strict semver shape; the release repository,
// the per-architecture asset names, the download host, and the binary path are
// all derived here, never taken from the caller. A downloaded release is only
// installed after its SHA-256 matches the checksum, its detached OpenSSH
// signature validates against the node's pinned release key, and the binary it
// carries runs and reports the expected version.
//
// A release is a tarball, not a bare binary, because a panel version is more
// than /usr/bin/nexa: it is also the systemd units, the tmpfiles and sysusers
// rules, the nginx template, and the host prerequisites. Swapping only the
// binary half-upgrades a node — a release that adds a tmpfiles directory or
// widens the agent unit's ReadWritePaths would fail at runtime with no signal.
// So the tarball is extracted to a staging tree, the current managed files are
// snapshotted, and its own scripts/install.sh applies packaging before the live
// binary moves. A detached helper then activates and health-checks the result.
//
// The checksum guards accidental corruption; authenticity comes from a
// separately managed signing key whose public half is pinned on the node. The
// release source is also compile-time fixed. The tarball is still treated as
// hostile input during extraction: traversal, links, and oversized members are
// refused before anything is executed as root.
package selfupdate

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"time"
)

// Repository is the trusted release source: github.com/builtbybasit/nexa-panel.
// These stay untyped constants rather than build-time variables — a constant
// cannot be rebound by anything, at any layer, so there is no code path (and no
// ldflags typo) that can point a node's root-privileged download at another
// project. A fork that needs its own releases forks these two lines.
//
// Note this is deliberately not the Go module path, which is a separate
// placeholder; the module path names the import, this names the release host.
const (
	repositoryOwner = "builtbybasit"
	repositoryName  = "nexa-panel"
)

// binaryPath is where the running nexa binary lives on a managed node; the swap
// writes a sibling ".new" file and renames it over this path.
const defaultBinaryPath = "/usr/bin/nexa"

const (
	defaultAllowedSignersPath = "/etc/nexa-panel/release-signers"
	releaseSignerIdentity     = "nexa-panel-release"
	releaseSignatureNamespace = "file"
)

// defaultWorkRoot holds staging trees, rollback snapshots, and the fsynced
// transaction journal. It is deliberately NOT
// under /var/lib/nexa-panel: that directory is owned by the unprivileged nexa
// service account, and this tree contains a scripts/install.sh that the agent
// executes as root. It is created 0700 root-owned, and /var is already in the
// agent unit's ReadWritePaths, so no unit change is needed to use it.
const defaultWorkRoot = "/var/lib/nexa-panel-update"

// defaultControlDatabasePath is the SQLite state whose schema the API may
// migrate when an updated binary first starts. Every transaction captures a
// consistent pre-activation copy so an automatic binary rollback never starts
// the old API against a newer, potentially incompatible schema.
const defaultControlDatabasePath = "/var/lib/nexa-panel/control.db"

// releaseBinaryEntry, releaseInstallerEntry and releaseInstallerFlag name the
// pieces of the release tarball this operator depends on. The installer is
// invoked with --sync-packaging, which re-applies units, tmpfiles, sysusers, and
// Nginx configuration without touching the binary.
// The installer is additionally run with --no-start. On its own,
// --sync-packaging restarts nexa-agent as soon as anything changed — and that
// restart would tear down the cgroup the installer is running in, killing the
// agent, the script, and the RPC that is waiting on it, so the update would
// report a failure it did not have. Activation is instead performed by a
// detached helper with a durable journal and readiness-gated rollback.
const (
	releaseBinaryEntry      = "bin/nexa"
	releaseInstallerEntry   = "scripts/install.sh"
	releaseInstallerFlag    = "--sync-packaging"
	releaseInstallerNoStart = "--no-start"
)

// Release describes one downloadable release of the panel for this node's
// architecture. All three asset URLs are derived from the fixed release source.
type Release struct {
	Version string `json:"version"`
	Tag     string `json:"tag"`
	Notes   string `json:"notes,omitempty"`
	// AssetName is the published file name the archive URL resolves to. It is
	// carried rather than re-derived so the agreement check can compare what the
	// release actually published against what this architecture expects.
	AssetName    string    `json:"assetName"`
	AssetURL     string    `json:"assetURL"`
	ChecksumURL  string    `json:"checksumURL"`
	SignatureURL string    `json:"signatureURL"`
	PublishedAt  time.Time `json:"publishedAt,omitempty"`
}

// Availability is the result of a check: what is running now and, if newer, the
// latest release the node could move to.
type Availability struct {
	InstalledVersion string    `json:"installedVersion"`
	Latest           *Release  `json:"latest,omitempty"`
	UpdateAvailable  bool      `json:"updateAvailable"`
	CheckedAt        time.Time `json:"checkedAt"`
}

// Change is the network-facing apply request. A caller may select only a
// release version from the compile-time-pinned repository. Host paths are
// intentionally absent: installing a local file is a root-only CLI operation
// and never crosses the agent RPC trust boundary.
type Change struct {
	Version string `json:"version,omitempty"`
}

// Result is the durable outcome of an apply. Successful calls return
// Activated=true only after readiness.
type Result struct {
	PreviousVersion string `json:"previousVersion"`
	TargetVersion   string `json:"targetVersion"`
	Swapped         bool   `json:"swapped"`
	// Activated is true only after the detached activation helper has restarted
	// the packaged services and the control plane's readiness endpoint has
	// authenticated the newly started agent, confirmed its schema is migrated,
	// and reported the target version as the one now serving.
	Activated bool `json:"activated"`
	// RolledBack reports that activation failed and the transaction restored its
	// exact pre-update binary and packaging snapshot.
	RolledBack bool `json:"rolledBack"`
	// PackagingSynced reports whether the release's packaging — systemd units,
	// tmpfiles and sysusers rules, the nginx template, host prerequisites — was
	// applied alongside the binary. It is false only for a root-only local binary
	// push; release rollback restores the exact captured host files.
	PackagingSynced bool `json:"packagingSynced"`
	// PackagingNote states, in the operator's language, why packaging was not
	// synced. It is the honest half of a binary-only change: a node whose binary
	// moved but whose units did not is a node someone has to look at.
	PackagingNote string `json:"packagingNote,omitempty"`
}

// Operator is the interface the control plane depends on (via a unix-socket
// client) and the agent serves.
type Operator interface {
	// Latest reports the installed version and, when a newer release exists, the
	// release the node could update to.
	Latest(context.Context) (Availability, error)
	// Apply verifies and prepares the target, then waits for its detached
	// activation journal to reach readiness-verified success or rollback.
	Apply(context.Context, Change) (Result, error)
}

// ReleaseSource resolves releases from the trusted repository. It is an
// interface so the network-facing GitHub implementation can be replaced in
// tests.
type ReleaseSource interface {
	// Latest returns the newest published release for the given architecture.
	Latest(ctx context.Context, arch string) (Release, error)
	// ByVersion returns the release for an explicit version (without the leading
	// "v") for the given architecture.
	ByVersion(ctx context.Context, arch, version string) (Release, error)
}

// Command is a single privileged process invocation.
type Command struct {
	Name string
	Args []string
	// Env adds narrowly scoped variables to the inherited process environment.
	Env []string
	// ExtraFiles are inherited as descriptors 3+n. Self-update uses descriptor
	// 3 to prove lifecycle-lock ownership to the packaging installer without a
	// second flock acquisition or a bypass flag.
	ExtraFiles []*os.File
	// Stdin carries bounded input that must not be exposed in argv. Release
	// signature verification uses it for the exact archive bytes.
	Stdin []byte
}

// Runner executes commands. Production uses execRunner; tests inject a fake so
// the validation exec and the systemd-run/systemctl calls can be observed
// without touching the host.
type Runner interface {
	Run(context.Context, Command) ([]byte, error)
}

type execRunner struct{}

func (execRunner) Run(ctx context.Context, command Command) ([]byte, error) {
	process := exec.CommandContext(ctx, command.Name, command.Args...)
	if len(command.Env) > 0 {
		process.Env = append(os.Environ(), command.Env...)
	}
	process.ExtraFiles = command.ExtraFiles
	process.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	process.Cancel = func() error {
		if process.Process == nil {
			return os.ErrProcessDone
		}
		err := syscall.Kill(-process.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	if len(command.Stdin) > 0 {
		process.Stdin = bytes.NewReader(command.Stdin)
	}
	return process.CombinedOutput()
}

// HostOperator performs self-updates on the local node.
type HostOperator struct {
	source                 ReleaseSource
	downloader             Downloader
	runner                 Runner
	installed              string
	binaryPath             string
	databasePath           string
	workRoot               string
	lockPath               string
	allowedSignersPath     string
	activationPollInterval time.Duration
	activate               func(context.Context, string) error
	readiness              func(context.Context, string) error
	managedPaths           []string
	// packageStateDir is the dpkg state directory whose presence gates
	// package-manager recovery.
	packageStateDir string
	arch            string
	now             func() time.Time
	// applyMu serialises applies: two concurrent swaps of the same binary would
	// race on the sibling ".new" file and the rename.
	applyMu sync.Mutex
}

// HostConfig configures the operator. Only InstalledVersion is required; the
// remaining fields default to production values and are overridden in tests.
type HostConfig struct {
	// InstalledVersion is the version the running agent was built as; it is the
	// baseline every availability check compares against.
	InstalledVersion string
	// Source resolves releases; a nil Source uses the GitHub release source for
	// the trusted repository.
	Source ReleaseSource
	// Downloader fetches release assets; a nil Downloader uses an HTTPS client.
	Downloader Downloader
	// Runner executes privileged commands; a nil Runner uses the real exec runner.
	Runner Runner
	// BinaryPath overrides the swap target (defaults to /usr/bin/nexa).
	BinaryPath string
	// ControlDatabasePath overrides the SQLite state protected by update
	// rollback (defaults to /var/lib/nexa-panel/control.db).
	ControlDatabasePath string
	// WorkRoot overrides where releases and the durable transaction journal are
	// staged (defaults to /var/lib/nexa-panel-update).
	WorkRoot string
	// LockPath serializes self-update with install and uninstall operations
	// (defaults to /run/lock/nexa-panel-lifecycle.lock).
	LockPath string
	// ReleaseTokenPath overrides the credential file read for the private
	// release repository (defaults to /etc/nexa-panel/release.token).
	ReleaseTokenPath string
	// AllowedSignersPath is an OpenSSH allowed-signers file containing the
	// pinned release signing key (defaults to /etc/nexa-panel/release-signers).
	AllowedSignersPath string
	// Arch overrides the detected architecture (defaults to runtime.GOARCH).
	Arch                   string
	activationPollInterval time.Duration
	activate               func(context.Context, string) error
	// readiness proves the control plane that is live after a phase change is
	// both healthy and running the version that phase intended. The string is
	// that expected version; an empty or non-semver value asserts health only.
	readiness       func(context.Context, string) error
	managedPaths    []string
	packageStateDir string
}

// NewHostOperator builds the operator, filling in production defaults for any
// field HostConfig leaves zero.
func NewHostOperator(config HostConfig) (*HostOperator, error) {
	installed := normalizeVersion(config.InstalledVersion)
	if installed == "" {
		return nil, errors.New("self-update requires the installed panel version")
	}
	arch := config.Arch
	if arch == "" {
		arch = runtime.GOARCH
	}
	if _, ok := assetArch(arch); !ok {
		return nil, errors.New("self-update is unsupported on this architecture")
	}
	// One token reader is shared by the metadata and asset paths: the private
	// repository 404s both without a credential, and both must see a rotated
	// token without an agent restart.
	tokens := newReleaseTokens(config.ReleaseTokenPath)
	source := config.Source
	if source == nil {
		source = newGitHubSource(nil, tokens)
	}
	downloader := config.Downloader
	if downloader == nil {
		downloader = newHTTPDownloader(nil, tokens)
	}
	runner := config.Runner
	if runner == nil {
		runner = execRunner{}
	}
	binaryPath := config.BinaryPath
	if binaryPath == "" {
		binaryPath = defaultBinaryPath
	}
	databasePath := config.ControlDatabasePath
	if databasePath == "" {
		databasePath = defaultControlDatabasePath
	}
	if !filepath.IsAbs(databasePath) {
		return nil, errors.New("self-update control database path must be absolute")
	}
	workRoot := config.WorkRoot
	if workRoot == "" {
		workRoot = defaultWorkRoot
	}
	lockPath := config.LockPath
	if lockPath == "" {
		lockPath = defaultLifecycleLock
	}
	if !filepath.IsAbs(lockPath) {
		return nil, errors.New("self-update lifecycle lock path must be absolute")
	}
	allowedSignersPath := config.AllowedSignersPath
	if allowedSignersPath == "" {
		allowedSignersPath = defaultAllowedSignersPath
	}
	pollInterval := config.activationPollInterval
	if pollInterval <= 0 {
		pollInterval = 250 * time.Millisecond
	}
	managedPaths := config.managedPaths
	if managedPaths == nil {
		managedPaths = append([]string(nil), defaultManagedPackagingPaths...)
	} else {
		managedPaths = append([]string(nil), managedPaths...)
	}
	packageStateDir := config.packageStateDir
	if packageStateDir == "" {
		packageStateDir = defaultPackageStateDir
	}
	readiness := config.readiness
	if readiness == nil {
		readiness = func(ctx context.Context, expectVersion string) error {
			return waitAPIReady(ctx, apiSocketPath, expectVersion, 45*time.Second)
		}
	}
	return &HostOperator{
		source:                 source,
		downloader:             downloader,
		runner:                 runner,
		installed:              installed,
		binaryPath:             binaryPath,
		databasePath:           databasePath,
		workRoot:               workRoot,
		lockPath:               lockPath,
		allowedSignersPath:     allowedSignersPath,
		activationPollInterval: pollInterval,
		activate:               config.activate,
		readiness:              readiness,
		managedPaths:           managedPaths,
		packageStateDir:        packageStateDir,
		arch:                   arch,
		now:                    time.Now,
	}, nil
}

// Latest resolves the newest release and compares it to the installed version.
func (o *HostOperator) Latest(ctx context.Context) (Availability, error) {
	release, err := o.source.Latest(ctx, o.arch)
	if err != nil {
		return Availability{}, err
	}
	availability := Availability{
		InstalledVersion: o.installed,
		CheckedAt:        o.now().UTC(),
	}
	if release.Version != "" {
		latest := release
		availability.Latest = &latest
		availability.UpdateAvailable = isNewer(release.Version, o.installed)
	}
	return availability, nil
}

// assetArch maps a Go architecture to the release asset suffix, reporting
// whether the architecture is supported at all.
func assetArch(goarch string) (string, bool) {
	switch goarch {
	case "amd64":
		return "amd64", true
	case "arm64":
		return "arm64", true
	default:
		return "", false
	}
}
