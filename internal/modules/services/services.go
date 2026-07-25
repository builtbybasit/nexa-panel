// Package services is the control-panel feature module behind the Services page.
// It surfaces the systemd service units nexa manages on the node and orchestrates
// start/stop/restart and boot-enablement toggles as durable jobs through the
// privileged services operator. Each mutation is planned and applied back-to-back
// inside one job: the agent signs the plan it just issued and the module hands it
// straight back to apply, so the one-click UX still travels the signed path.
//
// Stopping a panel-critical unit is the one action that can end the session
// issuing it, so it never travels that path unguarded: the stop is refused
// unless the caller acknowledges the consequence, and an acknowledged stop is
// applied only with a timed automatic revert armed behind it. Units that would
// take the revert mechanism itself down are refused outright. See
// internal/modules/safeguard.
package services

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/nexa-panel/nexa-panel/internal/modules/safeguard"
	"github.com/nexa-panel/nexa-panel/internal/platform/jobs"
	"github.com/nexa-panel/nexa-panel/internal/platform/module"
	servicesoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/services"
)

// lockoutDomain names this module's reverts inside the shared guard.
const lockoutDomain = "services"

type Module struct {
	jobs     *jobs.Module
	operator servicesoperator.Operator
	guard    *safeguard.Guard
}

// Option configures optional module behaviour.
type Option func(*Module)

// WithLockoutGuard arms timed automatic reverts for panel-critical stops.
// Without it the module has nowhere durable to record a pending revert, so it
// refuses those stops rather than applying one it cannot undo.
func WithLockoutGuard(guard *safeguard.Guard) Option {
	return func(m *Module) { m.guard = guard }
}

func New(queue *jobs.Module, operator servicesoperator.Operator, options ...Option) (*Module, error) {
	if queue == nil || operator == nil {
		return nil, errors.New("services jobs and operator are required")
	}
	m := &Module{jobs: queue, operator: operator}
	for _, option := range options {
		option(m)
	}
	if err := queue.RegisterHandler("services.apply", m.applyJob); err != nil {
		return nil, err
	}
	if m.guard != nil {
		if err := m.guard.Register(lockoutDomain, m.executeRevert); err != nil {
			return nil, err
		}
	}
	return m, nil
}

func (m *Module) Descriptor() module.Descriptor {
	return module.Descriptor{
		ID: "services", Name: "Services", Version: "0.1.0",
		Description:        "List systemd services and start, stop, restart, or toggle their boot-time enablement.",
		Dependencies:       []string{"identity", "jobs"},
		EstimatedIdleBytes: 128 * 1024,
	}
}

func (m *Module) Register(registry module.Registry) error { return m.registerHTTP(registry) }

// Start reconciles armed reverts left behind by a previous API process. The
// guard is shared with the firewall module and both start it; the second call is
// a no-op, which is what keeps the reconcile from running twice.
func (m *Module) Start(ctx context.Context) {
	if m.guard != nil {
		m.guard.Start(ctx)
	}
}

// Close stops the countdowns. Anything still armed stays armed in the table and
// is rescheduled by the next Start, so a shutdown cannot silently disarm a
// pending rollback.
func (m *Module) Close() {
	if m.guard != nil {
		m.guard.Close()
	}
}

// List reports the managed service units present on the node, each with its
// current active and boot-enablement state. It is a direct pass-through to the
// operator — there is no control-panel state to protect.
func (m *Module) List(ctx context.Context) ([]servicesoperator.Service, error) {
	return m.operator.Discover(ctx)
}

// actionsByName maps the request's action word to the operator action. Only
// these five verbs are ever queued.
var actionsByName = map[string]servicesoperator.Action{
	"start":   servicesoperator.ActionStart,
	"stop":    servicesoperator.ActionStop,
	"restart": servicesoperator.ActionRestart,
	"enable":  servicesoperator.ActionEnable,
	"disable": servicesoperator.ActionDisable,
}

// Submission is the outcome of an accepted change: the job applying it and, for
// a panel-critical stop, the armed revert that starts the unit again unless the
// operator confirms from a still-working session.
type Submission struct {
	Job    jobs.Job          `json:"job"`
	Revert *safeguard.Revert `json:"revert,omitempty"`
	Window int               `json:"revertWindowSeconds,omitempty"`
}

// Toggle queues one service action against one unit. The action string is
// validated here; the operator re-validates both the action and the unit
// against the node's real state on plan and apply.
//
// A stop that can end the operator's own access is refused with a
// *safeguard.RiskError unless acknowledged, and an acknowledged one arms a timed
// revert that starts the unit again.
func (m *Module) Toggle(ctx context.Context, unit, action string, actor *string, acknowledged bool) (Submission, error) {
	unit = strings.TrimSpace(unit)
	action = strings.TrimSpace(strings.ToLower(action))
	if unit == "" {
		return Submission{}, errors.New("a service unit is required")
	}
	operatorAction, ok := actionsByName[action]
	if !ok {
		return Submission{}, errors.New("service action must be start, stop, restart, enable, or disable")
	}
	reasons, irreversible := assessLockout(unit, action)
	if err := m.checkLockout(reasons, irreversible, acknowledged); err != nil {
		return Submission{}, err
	}
	change := servicesoperator.Change{Action: operatorAction, Unit: unit}
	title := actionTitle(action) + " " + strings.TrimSuffix(unit, ".service")
	job, err := m.jobs.SubmitTitled(ctx, "services.apply", title, change, actor)
	if err != nil {
		return Submission{}, err
	}
	submission := Submission{Job: job}
	if len(reasons) == 0 {
		return submission, nil
	}
	revert, err := m.guard.Arm(ctx, safeguard.ArmRequest{
		Domain: lockoutDomain, Subject: unit, Reasons: reasons,
		Summary: "The service is started again automatically unless you confirm the panel and your server session still work.",
		Payload: servicesoperator.Change{Action: servicesoperator.ActionStart, Unit: unit},
		JobID:   job.ID, ActorUserID: actor,
	})
	if err != nil {
		return Submission{}, err
	}
	submission.Revert = &revert
	submission.Window = int(m.guard.Window().Seconds())
	return submission, nil
}

// checkLockout refuses a risky stop that is unacknowledged, one whose unit can
// never be reverted from inside the panel, and one the module has no durable
// place to record a revert for.
func (m *Module) checkLockout(reasons []string, irreversible, acknowledged bool) error {
	if len(reasons) == 0 {
		return nil
	}
	if irreversible {
		return errors.New(strings.Join(reasons, " ") + " Stop it from a root console instead, where you can start it again.")
	}
	if !acknowledged {
		return &safeguard.RiskError{Reasons: reasons}
	}
	if m.guard == nil {
		return errors.New("this stop can lock you out and automatic rollback is unavailable on this node, so it was refused")
	}
	return nil
}

// executeRevert starts the stopped unit again through an ordinary services job,
// so the rollback travels the same planned, agent-signed path as the stop.
func (m *Module) executeRevert(ctx context.Context, revert safeguard.Revert) (int64, error) {
	var change servicesoperator.Change
	if err := json.Unmarshal(revert.Payload, &change); err != nil || change.Action == "" || change.Unit == "" {
		return 0, errors.New("the stored revert instruction could not be read")
	}
	job, err := m.jobs.SubmitTitled(ctx, "services.apply",
		"Automatic rollback: start "+strings.TrimSuffix(change.Unit, ".service"), change, revert.ActorUserID)
	if err != nil {
		return 0, err
	}
	return job.ID, nil
}

// Reverts lists this module's recent armed and settled reverts for the UI.
func (m *Module) Reverts(ctx context.Context) ([]safeguard.Revert, error) {
	if m.guard == nil {
		return nil, nil
	}
	return m.guard.Recent(ctx, lockoutDomain, 20)
}

// ConfirmRevert disarms one revert; the authenticated request is itself the
// proof that the operator's access survived the stop.
func (m *Module) ConfirmRevert(ctx context.Context, id string) (safeguard.Revert, error) {
	if m.guard == nil {
		return safeguard.Revert{}, safeguard.ErrNotFound
	}
	return m.guard.Confirm(ctx, lockoutDomain, id)
}

// RevertWindowSeconds is how long an armed revert waits, for the UI copy.
func (m *Module) RevertWindowSeconds() int {
	if m.guard == nil {
		return 0
	}
	return int(m.guard.Window().Seconds())
}

func actionTitle(action string) string {
	switch action {
	case "start":
		return "Start"
	case "stop":
		return "Stop"
	case "restart":
		return "Restart"
	case "enable":
		return "Enable"
	case "disable":
		return "Disable"
	default:
		return "Update"
	}
}

// applyJob plans and applies one service change. The operator validates the
// change against the node's real state on both the plan and apply calls; the
// agent signs the plan in between, so an unsigned or drifted change never
// applies.
func (m *Module) applyJob(ctx context.Context, raw json.RawMessage, report func(int, string) error) (any, error) {
	var change servicesoperator.Change
	if err := json.Unmarshal(raw, &change); err != nil || change.Action == "" || change.Unit == "" {
		return nil, errors.New("invalid service change request")
	}
	_ = report(20, "Planning the service change.")
	plan, err := m.operator.Plan(ctx, change)
	if err != nil {
		return nil, err
	}
	_ = report(60, "Applying the signed service plan.")
	observation, err := m.operator.Apply(ctx, plan)
	if err != nil {
		return nil, err
	}
	_ = report(95, "Service change is verified.")
	return observation, nil
}
