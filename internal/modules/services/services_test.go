package services

import (
	"context"
	"testing"

	"github.com/nexa-panel/nexa-panel/internal/platform/operators/services"
)

// stubOperator records the last change queued through Toggle so the module's
// action validation and change assembly can be asserted without a live agent.
type stubOperator struct{}

func (stubOperator) Discover(context.Context) ([]services.Service, error) { return nil, nil }
func (stubOperator) Plan(context.Context, services.Change) (services.Plan, error) {
	return services.Plan{}, nil
}

func (stubOperator) Apply(context.Context, services.Plan) (services.Observation, error) {
	return services.Observation{}, nil
}

func TestToggleRejectsUnknownActionAndEmptyUnit(t *testing.T) {
	m := &Module{operator: stubOperator{}}
	ctx := context.Background()
	if _, err := m.Toggle(ctx, "", "start", nil, false); err == nil {
		t.Fatal("an empty unit must be rejected")
	}
	if _, err := m.Toggle(ctx, "nginx.service", "reboot", nil, false); err == nil {
		t.Fatal("an unsupported action word must be rejected")
	}
}

func TestActionsByNameCoversEveryOperatorAction(t *testing.T) {
	want := map[services.Action]struct{}{
		services.ActionStart: {}, services.ActionStop: {}, services.ActionRestart: {},
		services.ActionEnable: {}, services.ActionDisable: {},
	}
	got := map[services.Action]struct{}{}
	for _, action := range actionsByName {
		got[action] = struct{}{}
	}
	if len(got) != len(want) {
		t.Fatalf("actionsByName maps to %d distinct actions, want %d", len(got), len(want))
	}
	for action := range want {
		if _, ok := got[action]; !ok {
			t.Fatalf("action %q is not reachable through actionsByName", action)
		}
	}
}
