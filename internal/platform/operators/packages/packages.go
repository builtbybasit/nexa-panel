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
	"os"
	"os/exec"
	"time"
)

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

// Operator is the interface the control plane depends on (via a Unix-socket
// client) and the agent serves.
type Operator interface {
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
}

// NewHostOperator builds the operator; a nil runner uses the real exec runner.
func NewHostOperator(runner Runner) (*HostOperator, error) {
	if runner == nil {
		runner = execRunner{}
	}
	return &HostOperator{runner: runner, now: time.Now}, nil
}

// command builds a non-interactive apt-safe invocation.
func command(name string, args ...string) Command {
	return Command{Name: name, Args: args, Env: []string{"DEBIAN_FRONTEND=noninteractive"}}
}
