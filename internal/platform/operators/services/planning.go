package services

import (
	"context"
	"fmt"
	"time"
)

// planExpiry bounds how long a reviewed plan may sit before it must be
// regenerated. Service plans are planned and applied back-to-back within one
// control-plane job, so this is a safety bound rather than a review budget.
const planExpiry = 10 * time.Minute

// Plan validates a change against the node's real state and returns a
// reviewable plan. Signing happens in the agent handler, not here.
func (o *HostOperator) Plan(ctx context.Context, change Change) (Plan, error) {
	service, err := o.normalizeChange(ctx, change)
	if err != nil {
		return Plan{}, err
	}
	print, err := fingerprint(service)
	if err != nil {
		return Plan{}, err
	}
	steps, warnings := narrative(change.Action, service)
	now := o.now().UTC()
	return Plan{
		ID: randomID(), Kind: PlanKind,
		Change:              Change{Action: change.Action, Unit: service.SystemdUnit},
		Steps:               steps,
		Warnings:            warnings,
		ObservedFingerprint: print,
		PlannedAt:           now,
		ExpiresAt:           now.Add(planExpiry),
	}, nil
}

// normalizeChange is the security boundary: the action must be one of the five
// allowed verbs, and the unit must be a managed, present service on this node.
// It returns the discovered service so the fingerprint reflects real state.
func (o *HostOperator) normalizeChange(ctx context.Context, change Change) (Service, error) {
	switch change.Action {
	case ActionStart, ActionStop, ActionRestart, ActionEnable, ActionDisable:
	default:
		return Service{}, fmt.Errorf("service action %q is not supported", change.Action)
	}
	service, ok, err := o.find(ctx, change.Unit)
	if err != nil {
		return Service{}, err
	}
	if !ok {
		return Service{}, fmt.Errorf("%q is not a service nexa manages on this node", change.Unit)
	}
	return service, nil
}

// narrative builds the operator-facing step list and warnings for an action.
func narrative(action Action, service Service) ([]string, []string) {
	unit := service.SystemdUnit
	switch action {
	case ActionStart:
		return []string{"Start " + unit + ".", "Verify the unit reports active."}, nil
	case ActionStop:
		return []string{"Stop " + unit + ".", "Verify the unit reports inactive."},
			[]string{"Stopping " + service.Name + " takes it offline until it is started again."}
	case ActionRestart:
		return []string{"Restart " + unit + ".", "Verify the unit reports active."},
			[]string{"Restarting " + service.Name + " briefly interrupts it while it comes back up."}
	case ActionEnable:
		return []string{"Enable " + unit + " to start on boot.", "Verify the unit reports enabled."}, nil
	case ActionDisable:
		return []string{"Disable " + unit + " from starting on boot.", "Verify the unit reports disabled."},
			[]string{"Disabling " + service.Name + " means it will not come back after a reboot."}
	default:
		return nil, nil
	}
}
