// Package php is the privileged node operator behind the PHP page's two
// features: per-version extension (module) management, and per-version php.ini
// directive editing. Like the other operators it follows the plan -> sign ->
// apply -> verify model: the control plane may only name a PHP branch, an
// extension, and directive values; the package names, file paths, and ini keys
// that reach a command line or the disk are derived and gated here against
// strict shapes, never taken from the caller verbatim.
package php

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/platform/operators/sitefs"
)

// PlanKind identifies plans issued by this operator; the agent binds its HMAC
// signature to a plan of this kind.
const PlanKind = "nexa.php.v1"

// Action is the mutation a PHP change requests.
type Action string

const (
	ActionInstallExtension Action = "extension.install"
	ActionRemoveExtension  Action = "extension.remove"
	ActionSaveSettings     Action = "settings.save"
	ActionSaveSiteSettings Action = "site.settings.save"
)

// SiteScope identifies one site for a per-site directive override. Every field
// is re-validated by the operator against the sites root before a path is
// assembled: a per-site .user.ini is written under the site's own document root,
// owned by the site's unix user, never anywhere else.
type SiteScope struct {
	Slug     string `json:"slug"`
	Version  string `json:"version"`
	RootPath string `json:"rootPath"`
	UnixUser string `json:"unixUser"`
	// DeploymentMode decides which directory nginx serves, and therefore the one
	// directory PHP will scan for a .user.ini. It mirrors the sites module's
	// column; an empty value reads as standard, so a caller that predates the
	// deployer layout still resolves the historical path.
	DeploymentMode string `json:"deploymentMode,omitempty"`
}

// DeploymentModeStandard and DeploymentModeDeployer mirror the sites module's
// constants. They are restated rather than imported because an operator never
// depends on a control-plane module.
const (
	DeploymentModeStandard = "standard"
	DeploymentModeDeployer = "deployer"
)

// Change is the caller's request. Version is always required and validated
// against the node's installed PHP branches; Extension and the Set/Reset entries
// are validated against strict shapes (and, for settings, against the directives
// the node actually reports) before anything is assembled.
type Change struct {
	Action    Action `json:"action"`
	Version   string `json:"version"`
	Extension string `json:"extension,omitempty"`
	// Site, when set, scopes a settings change to one site's .user.ini instead of
	// the branch-wide FPM drop-in.
	Site *SiteScope `json:"site,omitempty"`
	// Set maps a php.ini directive to the value to write into nexa's managed
	// drop-in (or the site .user.ini); Reset lists directives to drop from it,
	// reverting to PHP's default (or, per-site, to the branch-wide value).
	Set   map[string]string `json:"set,omitempty"`
	Reset []string          `json:"reset,omitempty"`
}

// Extension is one installable PHP module for a branch, joined with observed
// install state. Name is the bare suffix (e.g. "redis"); Package is the apt
// package the suffix resolves to for this branch (e.g. "php8.3-redis").
type Extension struct {
	Name      string `json:"name"`
	Package   string `json:"package"`
	Installed bool   `json:"installed"`
}

// Directive is one php.ini setting for a branch: its effective value under the
// FPM SAPI, the extension (module) that registers it, its editability, and
// whether nexa's managed drop-in currently sets it.
type Directive struct {
	Name    string `json:"name"`
	Module  string `json:"module"`
	Value   string `json:"value"`
	Access  string `json:"access"`
	Managed bool   `json:"managed"`
}

// Plan is the reviewed, agent-signed unit of work. Steps and Warnings are
// derived so the operation can be surfaced in the UI; ObservedFingerprint is the
// TOCTOU guard between planning and applying.
type Plan struct {
	ID                  string    `json:"id"`
	Kind                string    `json:"kind"`
	Change              Change    `json:"change"`
	Packages            []string  `json:"packages,omitempty"`
	Steps               []string  `json:"steps"`
	Warnings            []string  `json:"warnings"`
	ObservedFingerprint string    `json:"observedFingerprint"`
	PlannedAt           time.Time `json:"plannedAt"`
	ExpiresAt           time.Time `json:"expiresAt"`
	Signature           string    `json:"signature,omitempty"`
}

// Observation is the verified result of applying a plan.
type Observation struct {
	Action     Action      `json:"action"`
	Version    string      `json:"version"`
	Extension  *Extension  `json:"extension,omitempty"`
	Directives []Directive `json:"directives,omitempty"`
	Verified   bool        `json:"verified"`
}

// Operator is the interface the control plane depends on (via a Unix-socket
// client) and the agent serves.
type Operator interface {
	// Versions reports the PHP branches installed on the node, so the control
	// plane can gate every request against real, installed runtimes.
	Versions(context.Context) ([]string, error)
	Extensions(context.Context, string) ([]Extension, error)
	Settings(context.Context, string) ([]Directive, error)
	// SiteSettings reports the directives a site may override in its .user.ini,
	// each showing the branch-wide effective value plus any per-site override.
	SiteSettings(context.Context, SiteScope) ([]Directive, error)
	Plan(context.Context, Change) (Plan, error)
	Apply(context.Context, Plan) (Observation, error)
}

// Command is a single privileged process invocation. Env is prepended to the
// inherited environment.
type Command struct {
	Name string
	Args []string
	Env  []string
}

// Runner executes commands. Production uses execRunner; tests inject a fake.
type Runner interface {
	Run(context.Context, Command) ([]byte, error)
}

type execRunner struct{}

// Run executes the command, merging Env onto the inherited environment. Env is
// used because reading FPM's effective ini needs PHP_INI_SCAN_DIR pointed at the
// pool's conf.d, and apt runs non-interactively.
func (execRunner) Run(ctx context.Context, command Command) ([]byte, error) {
	process := exec.CommandContext(ctx, command.Name, command.Args...)
	process.Env = append(os.Environ(), command.Env...)
	return capped(process.CombinedOutput())
}

// HostOperator manages PHP extensions and settings on the local node.
type HostOperator struct {
	runner     Runner
	now        func() time.Time
	configRoot string
	sitesRoot  string
	owner      sitefs.Ownership
	// applyMu serialises applies: extension installs and drop-in rewrites both
	// mutate a branch's shared config and reload its FPM master.
	applyMu sync.Mutex
}

// HostConfig configures the operator; a zero ConfigRoot defaults to /etc/php and
// a zero SitesRoot to /srv/nexa/sites.
type HostConfig struct {
	ConfigRoot string
	SitesRoot  string
	// Owner sets a written .user.ini to the site's unix user; a nil Owner uses
	// the real host ownership (uid of the site user, gid of www-data).
	Owner sitefs.Ownership
}

// NewHostOperator builds the operator; a nil runner uses the real exec runner.
func NewHostOperator(runner Runner, config HostConfig) (*HostOperator, error) {
	if runner == nil {
		runner = execRunner{}
	}
	if config.ConfigRoot == "" {
		config.ConfigRoot = "/etc/php"
	}
	if config.SitesRoot == "" {
		config.SitesRoot = "/srv/nexa/sites"
	}
	if !filepath.IsAbs(config.ConfigRoot) || !filepath.IsAbs(config.SitesRoot) {
		return nil, errors.New("PHP config root and sites root must be absolute")
	}
	if config.Owner == nil {
		config.Owner = sitefs.HostOwnership{}
	}
	return &HostOperator{
		runner: runner, now: time.Now,
		configRoot: filepath.Clean(config.ConfigRoot),
		sitesRoot:  filepath.Clean(config.SitesRoot),
		owner:      config.Owner,
	}, nil
}

// Shapes below are the trust surface: a value reaches a command line, a file
// path, or an ini line only after matching one of them. Nothing a repository,
// PHP itself, or the caller reports can smuggle something else through.
var (
	// versionPattern gates the shape only; a branch is trusted only after
	// Versions confirms the node actually has it installed.
	versionPattern = regexp.MustCompile(`^[0-9]{1,2}\.[0-9]{1,2}$`)
	// extensionPattern is the bare module suffix: a single lowercase token, which
	// is also why multi-hyphen apt packages (php8.3-all-dev) never enumerate.
	extensionPattern = regexp.MustCompile(`^[a-z0-9]{1,40}$`)
	// directivePattern gates php.ini keys: an identifier with dotted sub-keys
	// (opcache.enable, allow_url_fopen), never leading with a digit or dot.
	directivePattern = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_.]{0,63}$`)
)

// managedDropIn is nexa's own conf.d file for a branch's FPM pool. A high prefix
// loads it after the distro's extension files, so its directives win; it holds
// exactly the set an operator has overridden, and nothing else touches it.
const managedDropInName = "99-nexa-panel.ini"

// nonExtensionPackages are the php<ver>-* apt packages that are the runtime and
// SAPIs, not loadable modules, so the extensions list excludes them.
var nonExtensionPackages = map[string]struct{}{
	"fpm": {}, "cli": {}, "common": {}, "dev": {},
	"phpdbg": {}, "cgi": {}, "embed": {},
}
