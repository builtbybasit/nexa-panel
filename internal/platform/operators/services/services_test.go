package services

import (
	"context"
	"strings"
	"testing"
)

// fakeRunner answers systemctl subcommands from canned state so tests drive the
// operator without a live node. It records mutating verbs for assertions and
// flips a unit's active/enabled state so re-observation after Apply verifies.
type fakeRunner struct {
	// unitFiles is the raw `list-unit-files` output.
	unitFiles string
	// active and enabled are the current is-active / is-enabled words per unit.
	active  map[string]string
	enabled map[string]string
	// failVerb, when set, makes that mutating verb (start/stop/…) return an error.
	failVerb string
	calls    []Command
}

func (f *fakeRunner) Run(_ context.Context, command Command) ([]byte, error) {
	f.calls = append(f.calls, command)
	if command.Name != "systemctl" || len(command.Args) == 0 {
		return nil, nil
	}
	sub := command.Args[0]
	switch sub {
	case "list-unit-files":
		return []byte(f.unitFiles), nil
	case "is-active":
		return []byte(f.active[command.Args[1]] + "\n"), nil
	case "is-enabled":
		return []byte(f.enabled[command.Args[1]] + "\n"), nil
	case "start", "stop", "restart", "enable", "disable":
		unit := command.Args[1]
		if f.failVerb == sub {
			return []byte("Job failed"), errFail
		}
		// Apply the mutation to the fake's state so verify sees the new state.
		switch sub {
		case "start", "restart":
			f.active[unit] = "active"
		case "stop":
			f.active[unit] = "inactive"
		case "enable":
			f.enabled[unit] = "enabled"
		case "disable":
			f.enabled[unit] = "disabled"
		}
		return []byte("ok"), nil
	default:
		return nil, nil
	}
}

type constError string

func (e constError) Error() string { return string(e) }

const errFail = constError("command failed")

func newRunner() *fakeRunner {
	return &fakeRunner{
		unitFiles: strings.Join([]string{
			"nginx.service                enabled",
			"cron.service                 enabled",
			"php8.3-fpm.service           enabled",
			"postgresql@16-main.service   enabled",
			"nexa-agent.service           enabled",  // protected — must never surface
			"nexa-api.service             enabled",  // protected — must never surface
			"bluetooth.service            disabled", // not allow-listed
			"ssh.service                  enabled",  // not allow-listed
		}, "\n") + "\n",
		active: map[string]string{
			"nginx.service":              "active",
			"cron.service":               "active",
			"php8.3-fpm.service":         "inactive",
			"postgresql@16-main.service": "active",
		},
		enabled: map[string]string{
			"nginx.service":              "enabled",
			"cron.service":               "enabled",
			"php8.3-fpm.service":         "disabled",
			"postgresql@16-main.service": "enabled",
		},
	}
}

func newOperator(t *testing.T, runner Runner) *HostOperator {
	t.Helper()
	operator, err := NewHostOperator(runner)
	if err != nil {
		t.Fatal(err)
	}
	return operator
}

func TestDiscoverFiltersToAllowListAndExcludesPanelUnits(t *testing.T) {
	operator := newOperator(t, newRunner())
	services, err := operator.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Service{}
	for _, service := range services {
		got[service.SystemdUnit] = service
	}
	for _, want := range []string{"nginx.service", "cron.service", "php8.3-fpm.service", "postgresql@16-main.service"} {
		if _, ok := got[want]; !ok {
			t.Fatalf("expected %s to be discovered, got %v", want, keys(got))
		}
	}
	for _, forbidden := range []string{"nexa-agent.service", "nexa-api.service", "bluetooth.service", "ssh.service"} {
		if _, ok := got[forbidden]; ok {
			t.Fatalf("%s must never be surfaced", forbidden)
		}
	}
	if got["nginx.service"].Name != "nginx" {
		t.Fatalf("display name = %q, want nginx", got["nginx.service"].Name)
	}
	if !got["nginx.service"].Active || !got["nginx.service"].Enabled {
		t.Fatal("nginx should read active and enabled")
	}
	if got["php8.3-fpm.service"].Active || got["php8.3-fpm.service"].Enabled {
		t.Fatal("php8.3-fpm should read inactive and disabled")
	}
}

func TestPlanRejectsUnknownUnitAndAction(t *testing.T) {
	operator := newOperator(t, newRunner())
	ctx := context.Background()
	if _, err := operator.Plan(ctx, Change{Action: ActionStart, Unit: "ssh.service"}); err == nil {
		t.Fatal("a non-allow-listed unit must be rejected")
	}
	if _, err := operator.Plan(ctx, Change{Action: ActionStart, Unit: "nexa-api.service"}); err == nil {
		t.Fatal("a protected panel unit must be rejected")
	}
	if _, err := operator.Plan(ctx, Change{Action: "service.destroy", Unit: "nginx.service"}); err == nil {
		t.Fatal("an unsupported action must be rejected")
	}
	if _, err := operator.Plan(ctx, Change{Action: ActionRestart, Unit: "missing.service"}); err == nil {
		t.Fatal("a unit not present on the node must be rejected")
	}
}

func TestApplyStartsUnitAndVerifies(t *testing.T) {
	runner := newRunner()
	operator := newOperator(t, runner)
	ctx := context.Background()
	plan, err := operator.Plan(ctx, Change{Action: ActionStart, Unit: "php8.3-fpm.service"})
	if err != nil {
		t.Fatal(err)
	}
	observation, err := operator.Apply(ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	if !observation.Verified || !observation.Service.Active {
		t.Fatalf("expected verified active service, got %+v", observation)
	}
	if !ranVerb(runner, "start", "php8.3-fpm.service") {
		t.Fatal("expected `systemctl start php8.3-fpm.service` to run")
	}
}

func TestApplyRejectsDriftedFingerprint(t *testing.T) {
	runner := newRunner()
	operator := newOperator(t, runner)
	ctx := context.Background()
	plan, err := operator.Plan(ctx, Change{Action: ActionStop, Unit: "nginx.service"})
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a concurrent state change after planning.
	runner.enabled["nginx.service"] = "disabled"
	if _, err := operator.Apply(ctx, plan); err == nil {
		t.Fatal("apply must reject a plan whose observed state drifted")
	}
}

func TestApplyRejectsUnsignedKindAndTamperedUnit(t *testing.T) {
	operator := newOperator(t, newRunner())
	ctx := context.Background()
	plan, err := operator.Plan(ctx, Change{Action: ActionStart, Unit: "php8.3-fpm.service"})
	if err != nil {
		t.Fatal(err)
	}
	// Swapping the unit to a protected one after planning must be caught by
	// re-validation, regardless of the (now-stale) fingerprint.
	tampered := plan
	tampered.Change.Unit = "nexa-api.service"
	if _, err := operator.Apply(ctx, tampered); err == nil {
		t.Fatal("apply must reject a plan whose unit was swapped to a protected unit")
	}
}

func ranVerb(runner *fakeRunner, verb, unit string) bool {
	for _, call := range runner.calls {
		if call.Name == "systemctl" && len(call.Args) == 2 && call.Args[0] == verb && call.Args[1] == unit {
			return true
		}
	}
	return false
}

func keys(m map[string]Service) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
