package services

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/modules/safeguard"
	"github.com/nexa-panel/nexa-panel/internal/platform/audit"
	"github.com/nexa-panel/nexa-panel/internal/platform/jobs"
	servicesoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/services"
	"github.com/nexa-panel/nexa-panel/internal/platform/persistence"
)

// newGuardedModule builds a services module whose reverts are armed against a
// real control database, so the stored revert can be asserted end to end.
func newGuardedModule(t *testing.T, window time.Duration) (*Module, *safeguard.Guard) {
	t.Helper()
	ctx := context.Background()
	database, err := persistence.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := persistence.RunMigrations(ctx, database); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	auditLog, err := audit.New(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	queue, err := jobs.NewWithConfig(ctx, database, auditLog, logger, jobs.Config{PollInterval: 5 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	guard, err := safeguard.New(database, logger, safeguard.WithWindow(window))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(guard.Close)
	module, err := New(queue, stubOperator{}, WithLockoutGuard(guard))
	if err != nil {
		t.Fatal(err)
	}
	return module, guard
}

// Stopping Nginx takes the panel's own ingress down, so a typed browser
// confirmation is not enough: the server refuses until the risk is acknowledged.
func TestStoppingNginxIsRefusedUntilTheRiskIsAcknowledged(t *testing.T) {
	module, _ := newGuardedModule(t, time.Hour)
	ctx := context.Background()

	_, err := module.Toggle(ctx, "nginx.service", "stop", nil, false)
	var risk *safeguard.RiskError
	if !errors.As(err, &risk) {
		t.Fatalf("Toggle err = %v, want a safeguard.RiskError", err)
	}
	queued, err := module.jobs.List(ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(queued) != 0 {
		t.Fatalf("a refused stop was queued anyway: %+v", queued)
	}
}

// Acknowledging arms a revert that starts the unit again, and the revert is a
// durable record the page can poll while it counts down.
func TestAnAcknowledgedCriticalStopArmsAStartRevert(t *testing.T) {
	module, guard := newGuardedModule(t, time.Hour)
	ctx := context.Background()

	submission, err := module.Toggle(ctx, "nginx.service", "stop", nil, true)
	if err != nil {
		t.Fatalf("Toggle err = %v", err)
	}
	if submission.Revert == nil || !submission.Revert.Armed() {
		t.Fatalf("submission = %+v, want an armed revert", submission)
	}
	stored, err := guard.Get(ctx, "services", submission.Revert.ID)
	if err != nil {
		t.Fatal(err)
	}
	var undo servicesoperator.Change
	if err := json.Unmarshal(stored.Payload, &undo); err != nil {
		t.Fatal(err)
	}
	if undo.Action != servicesoperator.ActionStart || undo.Unit != "nginx.service" {
		t.Fatalf("stored undo = %+v, want a start of nginx.service", undo)
	}
}

// Stopping the API or the agent destroys the mechanism that would undo it, so
// no acknowledgement can buy that stop through the panel.
func TestStoppingTheUnitsCarryingTheRollbackIsAlwaysRefused(t *testing.T) {
	module, _ := newGuardedModule(t, time.Hour)
	ctx := context.Background()
	for _, unit := range []string{"nexa-api.service", "nexa-agent.service"} {
		_, err := module.Toggle(ctx, unit, "stop", nil, true)
		if err == nil {
			t.Fatalf("stopping %s was accepted", unit)
		}
		var risk *safeguard.RiskError
		if errors.As(err, &risk) {
			t.Fatalf("stopping %s offered an acknowledgeable risk: %v", unit, err)
		}
	}
}

// Restarting a critical unit comes back on its own, and stopping an ordinary
// unit cannot cut anyone off, so neither is gated.
func TestNonLockoutActionsStayUngated(t *testing.T) {
	module, _ := newGuardedModule(t, time.Hour)
	ctx := context.Background()
	for _, testCase := range []struct{ unit, action string }{
		{"nginx.service", "restart"},
		{"cron.service", "stop"},
		{"nginx.service", "disable"},
	} {
		submission, err := module.Toggle(ctx, testCase.unit, testCase.action, nil, false)
		if err != nil {
			t.Fatalf("%s %s err = %v", testCase.action, testCase.unit, err)
		}
		if submission.Revert != nil {
			t.Fatalf("%s %s armed a revert: %+v", testCase.action, testCase.unit, submission.Revert)
		}
	}
}

// A bare unit name must not slip past the guard the suffixed one is caught by.
func TestTheGuardNormalisesABareUnitName(t *testing.T) {
	reasons, irreversible := assessLockout("NGINX", "stop")
	if len(reasons) == 0 || irreversible {
		t.Fatalf("assessLockout(NGINX, stop) = %q, %v", reasons, irreversible)
	}
}

// The executor is what the timer calls; it must queue the start as a real job so
// the rollback travels the same planned, signed path as the stop.
func TestExecuteRevertQueuesTheStartAsAJob(t *testing.T) {
	module, _ := newGuardedModule(t, time.Hour)
	ctx := context.Background()
	payload, err := json.Marshal(servicesoperator.Change{Action: servicesoperator.ActionStart, Unit: "nginx.service"})
	if err != nil {
		t.Fatal(err)
	}
	jobID, err := module.executeRevert(ctx, safeguard.Revert{Domain: "services", Payload: payload})
	if err != nil || jobID == 0 {
		t.Fatalf("executeRevert = %d, %v", jobID, err)
	}
	queued, err := module.jobs.Get(ctx, jobID)
	if err != nil {
		t.Fatal(err)
	}
	if queued.Title != "Automatic rollback: start nginx" {
		t.Fatalf("revert job title = %q", queued.Title)
	}
}

// Without a durable place to record the pending revert, the critical stop must
// be refused rather than applied with no way back.
func TestACriticalStopIsRefusedWithoutAGuard(t *testing.T) {
	module := &Module{operator: stubOperator{}}
	if _, err := module.Toggle(context.Background(), "nginx.service", "stop", nil, true); err == nil {
		t.Fatal("a critical stop was accepted with no guard to arm a revert")
	}
}
