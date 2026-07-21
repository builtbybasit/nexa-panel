// Package services is the control-plane feature module behind the Services page.
// It surfaces the systemd service units nexa manages on the node and orchestrates
// start/stop/restart and boot-enablement toggles as durable jobs through the
// privileged services operator. Each mutation is planned and applied back-to-back
// inside one job: the agent signs the plan it just issued and the module hands it
// straight back to apply, so the one-click UX still travels the signed path.
package services

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/nexa-panel/nexa-panel/internal/platform/jobs"
	"github.com/nexa-panel/nexa-panel/internal/platform/module"
	servicesoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/services"
)

type Module struct {
	jobs     *jobs.Module
	operator servicesoperator.Operator
}

func New(queue *jobs.Module, operator servicesoperator.Operator) (*Module, error) {
	if queue == nil || operator == nil {
		return nil, errors.New("services jobs and operator are required")
	}
	m := &Module{jobs: queue, operator: operator}
	if err := queue.RegisterHandler("services.apply", m.applyJob); err != nil {
		return nil, err
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

// List reports the managed service units present on the node, each with its
// current active and boot-enablement state. It is a direct pass-through to the
// operator — there is no control-plane state to protect.
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

// Toggle queues one service action against one unit. The action string is
// validated here; the operator re-validates both the action and the unit
// against the node's real state on plan and apply.
func (m *Module) Toggle(ctx context.Context, unit, action string, actor *string) (jobs.Job, error) {
	unit = strings.TrimSpace(unit)
	action = strings.TrimSpace(strings.ToLower(action))
	if unit == "" {
		return jobs.Job{}, errors.New("a service unit is required")
	}
	operatorAction, ok := actionsByName[action]
	if !ok {
		return jobs.Job{}, errors.New("service action must be start, stop, restart, enable, or disable")
	}
	change := servicesoperator.Change{Action: operatorAction, Unit: unit}
	title := actionTitle(action) + " " + strings.TrimSuffix(unit, ".service")
	return m.jobs.SubmitTitled(ctx, "services.apply", title, change, actor)
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
