package services

import (
	"context"
	"errors"
	"fmt"
)

// Apply re-validates and executes a signed plan. It rejects an expired or
// wrong-kind plan, re-derives the change from the plan (never trusting an
// arbitrary unit string), rejects if the unit's state drifted since planning,
// runs the systemctl verb, then re-observes and verifies the unit reached the
// intended state. The mutex serialises applies.
func (o *HostOperator) Apply(ctx context.Context, plan Plan) (Observation, error) {
	o.applyMu.Lock()
	defer o.applyMu.Unlock()

	if plan.ID == "" || plan.Kind != PlanKind || o.now().UTC().After(plan.ExpiresAt) {
		return Observation{}, errors.New("service plan is invalid or expired")
	}
	service, err := o.normalizeChange(ctx, plan.Change)
	if err != nil {
		return Observation{}, err
	}
	current, err := fingerprint(service)
	if err != nil {
		return Observation{}, err
	}
	if current != plan.ObservedFingerprint {
		return Observation{}, errors.New("service state changed after planning; refresh and try again")
	}

	verb := systemctlVerb(plan.Change.Action)
	if verb == "" {
		return Observation{}, errors.New("service action is unsupported")
	}
	if output, err := o.runner.Run(ctx, Command{Name: "systemctl", Args: []string{verb, service.SystemdUnit}}); err != nil {
		return Observation{}, commandError(verb+" "+service.SystemdUnit, output, err)
	}
	return o.verify(ctx, plan.Change.Action, service.SystemdUnit)
}

// systemctlVerb maps an action to the systemctl subcommand it runs.
func systemctlVerb(action Action) string {
	switch action {
	case ActionStart:
		return "start"
	case ActionStop:
		return "stop"
	case ActionRestart:
		return "restart"
	case ActionEnable:
		return "enable"
	case ActionDisable:
		return "disable"
	default:
		return ""
	}
}

// verify re-observes the unit and confirms the action's intended state was
// reached: start/restart -> active, stop -> inactive, enable/disable -> the
// matching boot-enablement.
func (o *HostOperator) verify(ctx context.Context, action Action, unit string) (Observation, error) {
	service, ok, err := o.find(ctx, unit)
	if err != nil {
		return Observation{}, err
	}
	if !ok {
		return Observation{}, fmt.Errorf("%s is no longer a managed service on this node", unit)
	}
	if err := expectState(action, service); err != nil {
		return Observation{}, err
	}
	return Observation{Action: action, Service: service, Verified: true}, nil
}

// expectState returns an error if the unit did not reach the state the action
// intended.
func expectState(action Action, service Service) error {
	switch action {
	case ActionStart, ActionRestart:
		if !service.Active {
			return fmt.Errorf("%s did not become active (now %s)", service.SystemdUnit, service.Status)
		}
	case ActionStop:
		if service.Active {
			return fmt.Errorf("%s did not stop (now %s)", service.SystemdUnit, service.Status)
		}
	case ActionEnable:
		if !service.Enabled {
			return fmt.Errorf("%s was not enabled on boot", service.SystemdUnit)
		}
	case ActionDisable:
		if service.Enabled {
			return fmt.Errorf("%s was not disabled on boot", service.SystemdUnit)
		}
	}
	return nil
}
