package deploy

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

const (
	// phpConfigRoot is where the branches live. A PHP version is only accepted
	// if it has an FPM pool directory here, which is the same test the php
	// operator's discovery makes (operators/php/discovery.go:18-40): a site
	// serving that branch has one, and nothing else can claim to.
	phpConfigRoot = "/etc/php"
	// toolVersionTailBytes bounds a `--version` line before it is reported. The
	// output is a node's own tooling, but it still crosses into a job result and
	// then into a page, so it is capped like every other captured output here.
	toolVersionTailBytes = 200
	// aptEnvironment keeps an install non-interactive. A prompt would hold the
	// agent request open until the job's own timeout killed it.
	aptEnvironment = "DEBIAN_FRONTEND=noninteractive"
)

// The three actions a tool can end a prepare run in. They are the whole
// vocabulary: a tool is either already there, was installed by this run, or
// could not be installed and is carried back with a warning rather than
// failing the run — a node missing one tool should still report the other five.
const (
	ToolPresent   = "present"
	ToolInstalled = "installed"
	ToolFailed    = "failed"
)

// phpVersionPattern is the branch shape, restated from the php operator rather
// than imported: that package imports nothing from this one and the dependency
// must not start here. It is the first of two gates — the branch must also be
// installed — and together they are what keeps a caller's string out of a
// package name.
var phpVersionPattern = regexp.MustCompile(`^[0-9]{1,2}\.[0-9]{1,2}$`)

// prepareMu serializes prepare runs. It is package-level rather than a field on
// the operator because what it protects is not this process's state but the
// node's: apt takes a machine-wide dpkg lock, and two concurrent prepares would
// otherwise race each other into "could not get lock" failures.
var prepareMu sync.Mutex

// PrepareRequest asks the node to make itself deployable. It carries a PHP
// branch and nothing else — in particular no package name and no command — so
// the set of things a prepare can install is fixed by this package.
type PrepareRequest struct {
	PHPVersion string `json:"phpVersion"`
}

func (r PrepareRequest) validate() error {
	if !phpVersionPattern.MatchString(strings.TrimSpace(r.PHPVersion)) {
		return fmt.Errorf("PHP version %q is not a supported branch", r.PHPVersion)
	}
	return nil
}

// ToolStatus is one prerequisite as the node found — or left — it. Version is
// the tool's own first `--version` line, which is what an operator reading the
// job result actually wants to see.
type ToolStatus struct {
	Name      string `json:"name"`
	Path      string `json:"path,omitempty"`
	Version   string `json:"version,omitempty"`
	Installed bool   `json:"installed"`
	Action    string `json:"action"`
}

// PrepareObservation is what the node settled into. The firewall check is
// deliberately not here: it is read-only, it needs the firewall operator rather
// than this one, and the control plane runs it — see the deploy module's
// prepare job.
type PrepareObservation struct {
	Tools    []ToolStatus `json:"tools"`
	Warnings []string     `json:"warnings"`
}

// tool is one entry in the fixed prerequisite table. Binary is what is looked
// up on PATH; Package is what apt is asked for when it is missing.
type tool struct {
	Name    string
	Binary  string
	Package string
}

// baseTools is the hardcoded prerequisite set. It is a literal table and never
// derived from a request: the only thing a caller influences is which PHP
// branch's CLI is added to it, and that branch is validated against the
// branches the node actually has installed before it reaches a package name.
var baseTools = []tool{
	{Name: "git", Binary: "git", Package: "git"},
	{Name: "unzip", Binary: "unzip", Package: "unzip"},
	{Name: "rsync", Binary: "rsync", Package: "rsync"},
	{Name: "setfacl", Binary: "setfacl", Package: "acl"},
	// sudo is a prerequisite of the FPM reload grant, not of a deploy: the
	// grant writes a rule into sudo's include directory and validates it with
	// visudo, so a node without the package cannot be granted the permission.
	// scripts/install.sh installs it on a fresh node; this entry is what makes
	// an already-provisioned node catch up.
	{Name: "sudo", Binary: "sudo", Package: "sudo"},
}

// prepareSystem is the privileged surface Prepare drives. Like deployKeySystem
// and envSystem it is asserted from the operator's SSHNodeSystem rather than
// folded into it, so this surface can be faked on its own.
type prepareSystem interface {
	// LookPath resolves a tool on the node's PATH; a missing tool is reported
	// as an error, exactly as exec.LookPath does.
	LookPath(name string) (string, error)
	// RunPrepareCommand runs one command non-interactively and returns its
	// combined output. The argv is built here, from the fixed table, never from
	// a request value.
	RunPrepareCommand(ctx context.Context, argv []string) ([]byte, error)
	// PHPBranchInstalled reports whether a branch has an FPM pool directory,
	// which is what makes a version safe to interpolate into a package name.
	PHPBranchInstalled(version string) (bool, error)
}

var _ prepareSystem = (*SSHHostSystem)(nil)

// Prepare verifies, and where it can installs, the tooling a deployer needs on
// this node. It is idempotent: a second run over a prepared node touches apt
// not at all and reports every tool as present.
//
// A tool that cannot be installed is a warning, not an error. The point of the
// run is to tell an operator what the node is missing; failing the whole job on
// the first bad package would hide the other five answers.
func (o *SSHHostOperator) Prepare(ctx context.Context, request PrepareRequest) (PrepareObservation, error) {
	if err := request.validate(); err != nil {
		return PrepareObservation{}, err
	}
	system, err := o.prepares()
	if err != nil {
		return PrepareObservation{}, err
	}
	version := strings.TrimSpace(request.PHPVersion)
	installed, err := system.PHPBranchInstalled(version)
	if err != nil {
		return PrepareObservation{}, fmt.Errorf("inspect installed PHP branches: %w", err)
	}
	if !installed {
		return PrepareObservation{}, fmt.Errorf("PHP %s is not installed on this node", version)
	}
	prepareMu.Lock()
	defer prepareMu.Unlock()

	observation := PrepareObservation{Tools: make([]ToolStatus, 0, len(baseTools)+2), Warnings: []string{}}
	// The index refresh is lazy and happens at most once: a prepared node must
	// not pay for an apt-get update it has no install to make.
	updated := false
	for _, entry := range prepareTools(version) {
		status, warning := o.prepareTool(ctx, system, entry, &updated)
		observation.Tools = append(observation.Tools, status)
		if warning != "" {
			observation.Warnings = append(observation.Warnings, warning)
		}
	}
	return observation, nil
}

// prepareTools is the run's whole allowlist: the fixed table plus the branch's
// own CLI and Composer, in that order. The CLI comes first deliberately —
// Composer's package depends on a php-cli provider, and installing the site's
// own branch first is what stops apt from resolving that dependency by pulling
// in the distribution's default PHP alongside it.
func prepareTools(version string) []tool {
	tools := make([]tool, 0, len(baseTools)+2)
	tools = append(tools, baseTools...)
	tools = append(
		tools,
		tool{Name: "php" + version, Binary: "php" + version, Package: "php" + version + "-cli"},
		tool{Name: "composer", Binary: "composer", Package: "composer"},
	)
	return tools
}

// prepareTool reports one tool, installing it when it is absent. The returned
// warning is empty unless the install was attempted and did not produce a
// working binary.
func (o *SSHHostOperator) prepareTool(ctx context.Context, system prepareSystem, entry tool, updated *bool) (ToolStatus, string) {
	if path, err := system.LookPath(entry.Binary); err == nil {
		return ToolStatus{
			Name: entry.Name, Path: path, Version: o.toolVersion(ctx, system, path),
			Installed: true, Action: ToolPresent,
		}, ""
	}
	if !*updated {
		if output, err := system.RunPrepareCommand(ctx, []string{"apt-get", "update"}); err != nil {
			return ToolStatus{Name: entry.Name, Action: ToolFailed},
				entry.Name + " could not be installed: " + commandError(output, err).Error()
		}
		*updated = true
	}
	if output, err := system.RunPrepareCommand(ctx, []string{"apt-get", "install", "-y", "--no-install-recommends", entry.Package}); err != nil {
		return ToolStatus{Name: entry.Name, Action: ToolFailed},
			entry.Name + " could not be installed: " + commandError(output, err).Error()
	}
	// apt reporting success is not the same as the binary being on PATH — a
	// package can install and still not provide the command this run is for —
	// so the tool is looked up again rather than assumed.
	path, err := system.LookPath(entry.Binary)
	if err != nil {
		return ToolStatus{Name: entry.Name, Action: ToolFailed},
			entry.Package + " installed but " + entry.Binary + " is still not on the node's PATH"
	}
	return ToolStatus{
		Name: entry.Name, Path: path, Version: o.toolVersion(ctx, system, path),
		Installed: true, Action: ToolInstalled,
	}, ""
}

// toolVersion reports the tool's own first version line, or an empty string.
// A tool that will not say what it is is still a tool: the version is reporting
// detail, never a reason to fail the run.
func (o *SSHHostOperator) toolVersion(ctx context.Context, system prepareSystem, path string) string {
	output, err := system.RunPrepareCommand(ctx, []string{path, "--version"})
	if err != nil {
		return ""
	}
	line := strings.TrimSpace(strings.SplitN(string(output), "\n", 2)[0])
	if len(line) > toolVersionTailBytes {
		line = line[:toolVersionTailBytes]
	}
	return line
}

// prepares narrows the operator's node system to the prepare surface, exactly
// as deployKeys() and envs() do for theirs.
func (o *SSHHostOperator) prepares() (prepareSystem, error) {
	system, ok := o.system.(prepareSystem)
	if !ok {
		return nil, errors.New("this node system does not implement node preparation")
	}
	return system, nil
}

func (s *SSHHostSystem) LookPath(name string) (string, error) { return exec.LookPath(name) }

// PHPBranchInstalled mirrors the php operator's own definition of an installed
// branch: a config directory carrying an FPM pool directory. The version is
// already shape-checked by the caller, and it is joined rather than
// concatenated so a value that somehow escaped that check still cannot climb
// out of /etc/php.
func (s *SSHHostSystem) PHPBranchInstalled(version string) (bool, error) {
	if !phpVersionPattern.MatchString(version) {
		return false, errors.New("PHP branch is not a supported version")
	}
	info, err := os.Stat(filepath.Join(phpConfigRoot, version, "fpm"))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return info.IsDir(), nil
}

// RunPrepareCommand runs one prepare command with apt's non-interactive
// environment. The agent runs as root, so nothing here de-escalates: unlike the
// GitHub probes, these commands are the node's own package management.
func (s *SSHHostSystem) RunPrepareCommand(ctx context.Context, argv []string) ([]byte, error) {
	if len(argv) == 0 {
		return nil, errors.New("a prepare command is required")
	}
	process := exec.CommandContext(ctx, argv[0], argv[1:]...)
	process.Env = append(os.Environ(), aptEnvironment)
	return process.CombinedOutput()
}

func (c *UnixClient) Prepare(ctx context.Context, request PrepareRequest) (PrepareObservation, error) {
	var observation PrepareObservation
	err := c.prepareClient.JSON(ctx, http.MethodPost, "/v1/deploy/prepare", request, &observation)
	return observation, err
}
