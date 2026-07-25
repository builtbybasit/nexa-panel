// Package services is the privileged node operator behind the Services page: it
// lists the systemd service units nexa manages and starts, stops, restarts, and
// toggles the boot-time enablement of them. Like the other operators it follows
// the plan -> sign -> apply -> verify model. The control panel may only name a
// unit and one of a fixed set of actions; the unit is re-validated against the
// node's actually-discovered service list on both plan and apply, so an
// arbitrary string can never reach a systemctl command line, and the panel's own
// units are never exposed for control.
package services

import (
	"context"
	"os"
	"os/exec"
	"regexp"
	"sync"
	"time"
)

// PlanKind identifies plans issued by this operator; the agent binds its HMAC
// signature to a plan of this kind.
const PlanKind = "nexa.services.v1"

// Action is the mutation a service change requests. The five values map directly
// to the systemctl verbs the operator is willing to run; nothing else is.
type Action string

const (
	ActionStart   Action = "service.start"
	ActionStop    Action = "service.stop"
	ActionRestart Action = "service.restart"
	ActionEnable  Action = "service.enable"
	ActionDisable Action = "service.disable"
)

// Service is one managed systemd service unit joined with its observed runtime
// and boot-enablement state. Name is the unit with the ".service" suffix
// stripped for display; SystemdUnit is the full unit name commands use.
type Service struct {
	Name        string `json:"name"`
	SystemdUnit string `json:"systemdUnit"`
	Status      string `json:"status"`
	Active      bool   `json:"active"`
	Enabled     bool   `json:"enabled"`
}

// Change is the caller's request: an action against one unit. The unit is always
// re-validated against the discovered service list before anything is assembled.
type Change struct {
	Action Action `json:"action"`
	Unit   string `json:"unit"`
}

// Plan is the reviewed, agent-signed unit of work. Steps and Warnings are
// derived for the UI; ObservedFingerprint is the TOCTOU guard between planning
// and applying.
type Plan struct {
	ID                  string    `json:"id"`
	Kind                string    `json:"kind"`
	Change              Change    `json:"change"`
	Steps               []string  `json:"steps"`
	Warnings            []string  `json:"warnings"`
	ObservedFingerprint string    `json:"observedFingerprint"`
	PlannedAt           time.Time `json:"plannedAt"`
	ExpiresAt           time.Time `json:"expiresAt"`
	Signature           string    `json:"signature,omitempty"`
}

// Observation is the verified result of applying a plan: the unit's state as it
// now reads, and whether it reached the intended state.
type Observation struct {
	Action   Action  `json:"action"`
	Service  Service `json:"service"`
	Verified bool    `json:"verified"`
}

// Operator is the interface the control panel depends on (via a Unix-socket
// client) and the agent serves.
type Operator interface {
	// Discover reports the managed service units present on the node, each with
	// its current active and boot-enablement state.
	Discover(context.Context) ([]Service, error)
	Plan(context.Context, Change) (Plan, error)
	Apply(context.Context, Plan) (Observation, error)
}

// Command is a single privileged process invocation.
type Command struct {
	Name string
	Args []string
}

// Runner executes commands. Production uses execRunner; tests inject a fake.
type Runner interface {
	Run(context.Context, Command) ([]byte, error)
}

type execRunner struct{}

func (execRunner) Run(ctx context.Context, command Command) ([]byte, error) {
	process := exec.CommandContext(ctx, command.Name, command.Args...)
	process.Env = os.Environ()
	return capped(process.CombinedOutput())
}

// HostOperator manages systemd services on the local node.
type HostOperator struct {
	runner Runner
	now    func() time.Time
	// applyMu serialises applies so two concurrent toggles of the same unit can't
	// interleave their plan/verify windows.
	applyMu sync.Mutex
}

// NewHostOperator builds the operator; a nil runner uses the real exec runner.
func NewHostOperator(runner Runner) (*HostOperator, error) {
	if runner == nil {
		runner = execRunner{}
	}
	return &HostOperator{runner: runner, now: time.Now}, nil
}

// managedUnitPatterns is the allow-list that decides which of the node's service
// units the Services page exposes. It is deliberately shaped to match whatever
// real services a node running nexa's stack would carry — a web server, the
// scheduler, the database engines, mail/FTP daemons, and every installed
// PHP-FPM branch and PostgreSQL cluster — without hardcoding a fixed unit list
// or coupling to the php/mysql/postgres modules. A unit reaches a systemctl
// command line only after matching one of these AND being confirmed present by
// Discover.
var managedUnitPatterns = []*regexp.Regexp{
	regexp.MustCompile(`^nginx\.service$`),
	regexp.MustCompile(`^apache2\.service$`),
	regexp.MustCompile(`^cron\.service$`),
	regexp.MustCompile(`^mysql\.service$`),
	regexp.MustCompile(`^mariadb\.service$`),
	regexp.MustCompile(`^dovecot\.service$`),
	regexp.MustCompile(`^exim4\.service$`),
	regexp.MustCompile(`^proftpd\.service$`),
	regexp.MustCompile(`^php[0-9]+\.[0-9]+-fpm\.service$`),
	regexp.MustCompile(`^postgresql@.+\.service$`),
}

// protectedUnits are nexa's own units. They run the panel itself, so exposing
// stop/restart for them here would let an operator take the panel offline with
// no recovery path from the UI. They are filtered out even if a future
// allow-list pattern would otherwise match them.
var protectedUnits = map[string]struct{}{
	"nexa-agent.service": {},
	"nexa-api.service":   {},
}

// managed reports whether a unit name is one this operator will surface and act
// on: allow-listed in shape and not one of nexa's own protected units.
func managed(unit string) bool {
	if _, protected := protectedUnits[unit]; protected {
		return false
	}
	for _, pattern := range managedUnitPatterns {
		if pattern.MatchString(unit) {
			return true
		}
	}
	return false
}
