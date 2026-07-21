// Package selfupdate is the privileged node operator behind the panel's
// self-update feature. Only nexa-agent (User=root, /usr writable, full caps) can
// replace /usr/bin/nexa and restart the units, so this operator lives on the
// agent side; the control plane reaches it over the same authenticated unix
// socket as every other operator.
//
// The operation is deliberately narrow. The only thing a caller may name is a
// target version string, gated to a strict semver shape; the release repository,
// the per-architecture asset names, the download host, and the binary path are
// all derived here, never taken from the caller. A downloaded binary is only
// swapped in after its SHA-256 matches the checksum published alongside it and
// after it validates as a runnable nexa binary reporting the expected version.
//
// Trust note: the checksum is fetched from the same release as the binary, so it
// guards integrity (a corrupted or truncated download) rather than authenticity
// (a compromised release). The release source is a compile-time constant so the
// download can never be redirected to an arbitrary host; authenticity rests on
// the trust placed in that repository's releases.
package selfupdate

import (
	"context"
	"errors"
	"os/exec"
	"regexp"
	"runtime"
	"sync"
	"time"
)

// Repository is the trusted release source. It is a compile-time constant so a
// caller can never redirect the download to another host or project.
const (
	repositoryOwner = "nexa-panel"
	repositoryName  = "nexa-panel"
)

// binaryPath is where the running nexa binary lives on a managed node; the swap
// writes a sibling ".new" file and renames it over this path.
const defaultBinaryPath = "/usr/bin/nexa"

// restart is fired detached and slightly delayed so the apply RPC can return
// success to the control plane before the units are bounced out from under it.
const defaultRestartDelay = 3 * time.Second

// managedUnits are restarted together after a successful swap; nexa-api
// Requires=nexa-agent, so both must come back.
var managedUnits = []string{"nexa-agent.service", "nexa-api.service"}

// versionPattern gates a target version to a strict semver shape before it is
// ever turned into a release tag. A pre-release suffix is permitted.
var versionPattern = regexp.MustCompile(`^[0-9]{1,4}\.[0-9]{1,4}\.[0-9]{1,4}(-[0-9A-Za-z.]{1,40})?$`)

// Release describes one downloadable release of the panel for this node's
// architecture. AssetURL and ChecksumURL are derived from the release source and
// are the only URLs the operator will fetch.
type Release struct {
	Version     string    `json:"version"`
	Tag         string    `json:"tag"`
	Notes       string    `json:"notes,omitempty"`
	AssetURL    string    `json:"assetURL"`
	ChecksumURL string    `json:"checksumURL"`
	PublishedAt time.Time `json:"publishedAt,omitempty"`
}

// Availability is the result of a check: what is running now and, if newer, the
// latest release the node could move to.
type Availability struct {
	InstalledVersion string    `json:"installedVersion"`
	Latest           *Release  `json:"latest,omitempty"`
	UpdateAvailable  bool      `json:"updateAvailable"`
	CheckedAt        time.Time `json:"checkedAt"`
}

// Change is the caller's apply request. It selects one of two update sources:
//
//   - BinaryPath: a binary already staged on the host (operator scp/rsync'd it,
//     then ran `nexa self-update --binary PATH`). No download, no checksum, and
//     no newer-than guard — an operator pushing a build is an explicit act, so a
//     same- or dev-version re-deploy is allowed. This is the only source that
//     reads a caller-supplied path, and it is gated by the agent bearer token,
//     which is already root-equivalent.
//   - Version: a release to fetch from the trusted repository. An empty Version
//     targets the latest release; any value is re-validated against
//     versionPattern before use.
//
// BinaryPath takes precedence when both are set.
type Change struct {
	Version    string `json:"version,omitempty"`
	BinaryPath string `json:"binaryPath,omitempty"`
}

// Result is the verified outcome of an apply. RestartScheduled is true once the
// detached restart has been armed; the units are still running when this is
// returned so the enclosing job can record success first.
type Result struct {
	PreviousVersion  string `json:"previousVersion"`
	TargetVersion    string `json:"targetVersion"`
	Swapped          bool   `json:"swapped"`
	RestartScheduled bool   `json:"restartScheduled"`
	RestartDelay     string `json:"restartDelay,omitempty"`
}

// Operator is the interface the control plane depends on (via a unix-socket
// client) and the agent serves.
type Operator interface {
	// Latest reports the installed version and, when a newer release exists, the
	// release the node could update to.
	Latest(context.Context) (Availability, error)
	// Apply downloads, verifies, and swaps in the target release, then schedules
	// the detached restart. It returns before the restart fires.
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
	return process.CombinedOutput()
}

// HostOperator performs self-updates on the local node.
type HostOperator struct {
	source       ReleaseSource
	downloader   Downloader
	runner       Runner
	installed    string
	binaryPath   string
	restartDelay time.Duration
	arch         string
	now          func() time.Time
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
	// RestartDelay overrides how long after a swap the detached restart fires.
	RestartDelay time.Duration
	// Arch overrides the detected architecture (defaults to runtime.GOARCH).
	Arch string
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
	source := config.Source
	if source == nil {
		source = newGitHubSource(nil)
	}
	downloader := config.Downloader
	if downloader == nil {
		downloader = newHTTPDownloader(nil)
	}
	runner := config.Runner
	if runner == nil {
		runner = execRunner{}
	}
	binaryPath := config.BinaryPath
	if binaryPath == "" {
		binaryPath = defaultBinaryPath
	}
	restartDelay := config.RestartDelay
	if restartDelay <= 0 {
		restartDelay = defaultRestartDelay
	}
	return &HostOperator{
		source:       source,
		downloader:   downloader,
		runner:       runner,
		installed:    installed,
		binaryPath:   binaryPath,
		restartDelay: restartDelay,
		arch:         arch,
		now:          time.Now,
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
