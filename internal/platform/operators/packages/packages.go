// Package packages is the privileged node operator that installs and removes a
// fixed, allowlisted catalog of optional server software (PHP versions,
// PostgreSQL, Node.js, Composer) through apt. It follows the same
// plan -> sign -> apply -> verify model as the other operators: the control
// plane may only ask for a catalog entry by (app, version); the package names
// and repository steps that reach the command line are derived here, never
// supplied by the caller.
package packages

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"time"
)

// errUnexpectedNodeIndex reports a non-2xx response from the Node.js release index.
func errUnexpectedNodeIndex(status int) error {
	return fmt.Errorf("node.js release index returned status %d", status)
}

// PlanKind identifies plans issued by this operator; the agent binds its HMAC
// signature to a plan of this kind.
const PlanKind = "nexa.package.v1"

// Action is the mutation an application change requests.
type Action string

const (
	ActionInstall Action = "package.install"
	ActionRemove  Action = "package.remove"
)

// Change is the caller's request. Only App+Version are trusted inputs, and both
// are validated against the catalog before any package name is assembled.
type Change struct {
	Action  Action `json:"action"`
	App     string `json:"app"`
	Version string `json:"version"`
}

// Plan is the reviewed, agent-signed unit of work. Packages and Steps are
// derived from the catalog so the operator can be reviewed in the UI.
type Plan struct {
	ID                  string    `json:"id"`
	Kind                string    `json:"kind"`
	Change              Change    `json:"change"`
	Packages            []string  `json:"packages"`
	Steps               []string  `json:"steps"`
	Warnings            []string  `json:"warnings"`
	ObservedFingerprint string    `json:"observedFingerprint"`
	PlannedAt           time.Time `json:"plannedAt"`
	ExpiresAt           time.Time `json:"expiresAt"`
	Signature           string    `json:"signature,omitempty"`
}

// InstalledPackage is one observed dpkg entry from the catalog's package set.
type InstalledPackage struct {
	Name      string `json:"name"`
	Version   string `json:"version"`
	Installed bool   `json:"installed"`
}

// Observation is the verified result of applying a plan.
type Observation struct {
	App       string             `json:"app"`
	Version   string             `json:"version"`
	Packages  []InstalledPackage `json:"packages"`
	Installed bool               `json:"installed"`
	Verified  bool               `json:"verified"`
}

// Operator is the interface the control panel depends on (via a Unix-socket
// client) and the agent serves.
type Operator interface {
	// Catalog reports what this node's repositories can install. It is a node
	// call rather than a local table because only the node knows which versions
	// its configured repositories actually offer.
	Catalog(context.Context) ([]CatalogEntry, error)
	Discover(context.Context) ([]InstalledPackage, error)
	Plan(context.Context, Change) (Plan, error)
	Apply(context.Context, Plan) (Observation, error)
}

// Command is a single privileged process invocation. Env is prepended to the
// inherited environment; apt operations always run non-interactively.
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

// Run executes the command, merging Env onto the inherited environment. Unlike
// the other operators' runners this one sets environment variables, because apt
// requires DEBIAN_FRONTEND=noninteractive to avoid blocking on prompts.
func (execRunner) Run(ctx context.Context, command Command) ([]byte, error) {
	process := exec.CommandContext(ctx, command.Name, command.Args...)
	process.Env = append(os.Environ(), command.Env...)
	return capped(process.CombinedOutput())
}

// HostOperator installs and removes catalog packages on the local node.
type HostOperator struct {
	runner Runner
	now    func() time.Time
	// client and nodeIndexURL fetch the Node.js release index; the URL is a
	// field so tests can point it at a local server.
	client       *http.Client
	nodeIndexURL string
	// toolHome is a writable HOME handed to repository tooling that insists on
	// one. It is a field so tests can point it somewhere disposable.
	toolHome string
	// The catalog is enumerated from apt and nodejs.org, so it is cached: it is
	// read on every list, plan, and apply.
	catalogMu     sync.Mutex
	cachedCatalog []catalogEntry
	cachedAt      time.Time
	catalogTTL    time.Duration
}

// NewHostOperator builds the operator; a nil runner uses the real exec runner.
func NewHostOperator(runner Runner) (*HostOperator, error) {
	if runner == nil {
		runner = execRunner{}
	}
	return &HostOperator{
		runner:       runner,
		now:          time.Now,
		client:       &http.Client{Timeout: 15 * time.Second},
		nodeIndexURL: NodeIndexURL,
		toolHome:     defaultToolHome,
		catalogTTL:   5 * time.Minute,
	}, nil
}

// defaultToolHome is a writable HOME for repository tooling. The agent unit
// sets ProtectHome=true, so $HOME is /root and unwritable; add-apt-repository
// resolves a PPA through launchpadlib, which caches into $HOME/.launchpadlib
// and dies on the read-only filesystem before it ever reaches the network. It
// sits beside the self-update work root rather than under /var/lib/nexa-panel,
// which belongs to the unprivileged nexa account, and /var is already writable
// to the unit so no unit change is needed.
const defaultToolHome = "/var/lib/nexa-panel-apt"

// ensureToolHome creates the tooling home and returns it. The mode is set
// explicitly after creation: the agent runs with UMask=0177, which masks a
// MkdirAll(0700) down to 0600 -- a directory with no execute bit, which nothing
// can enter, and which a root-run test would never notice.
func (o *HostOperator) ensureToolHome() (string, error) {
	if o.toolHome == "" {
		return "", errors.New("no repository tooling home is configured")
	}
	if err := os.MkdirAll(o.toolHome, 0o700); err != nil {
		return "", fmt.Errorf("create the repository tooling home: %w", err)
	}
	if err := os.Chmod(o.toolHome, 0o700); err != nil {
		return "", fmt.Errorf("secure the repository tooling home: %w", err)
	}
	return o.toolHome, nil
}

// command builds a non-interactive apt-safe invocation.
func command(name string, args ...string) Command {
	return Command{Name: name, Args: args, Env: []string{"DEBIAN_FRONTEND=noninteractive"}}
}
