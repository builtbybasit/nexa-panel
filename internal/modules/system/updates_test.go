package system

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/nexa-panel/nexa-panel/internal/platform/audit"
	"github.com/nexa-panel/nexa-panel/internal/platform/jobs"
	selfupdateoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/selfupdate"
	"github.com/nexa-panel/nexa-panel/internal/platform/persistence"
)

type fakeUpdateOperator struct {
	availability selfupdateoperator.Availability
	availErr     error
	result       selfupdateoperator.Result
	applyErr     error
	appliedWith  *selfupdateoperator.Change
}

func (f *fakeUpdateOperator) Latest(context.Context) (selfupdateoperator.Availability, error) {
	return f.availability, f.availErr
}

func (f *fakeUpdateOperator) Apply(_ context.Context, change selfupdateoperator.Change) (selfupdateoperator.Result, error) {
	captured := change
	f.appliedWith = &captured
	return f.result, f.applyErr
}

func (f *fakeUpdateOperator) Transaction(context.Context) (selfupdateoperator.TransactionStatus, error) {
	return selfupdateoperator.TransactionStatus{}, nil
}

func (f *fakeUpdateOperator) Rollback(context.Context) (selfupdateoperator.Result, error) {
	return f.result, f.applyErr
}

func TestAvailableUpdateHTTPReportsAvailability(t *testing.T) {
	operator := &fakeUpdateOperator{availability: selfupdateoperator.Availability{
		InstalledVersion: "0.1.0",
		UpdateAvailable:  true,
		Latest:           &selfupdateoperator.Release{Version: "0.2.0"},
	}}
	module := &Module{updates: &updates{operator: operator}}

	response := httptest.NewRecorder()
	module.availableUpdateHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/system/updates", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.Code)
	}
	var body selfupdateoperator.Availability
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !body.UpdateAvailable || body.Latest == nil || body.Latest.Version != "0.2.0" {
		t.Fatalf("unexpected availability: %+v", body)
	}
}

func TestAvailableUpdateHTTPSurfacesAgentFailure(t *testing.T) {
	operator := &fakeUpdateOperator{availErr: errors.New("agent down")}
	module := &Module{updates: &updates{operator: operator}}

	response := httptest.NewRecorder()
	module.availableUpdateHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/system/updates", nil))

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", response.Code)
	}
}

func TestEmptyUpdateRequestPinsTheConcreteLatestVersion(t *testing.T) {
	operator := &fakeUpdateOperator{availability: selfupdateoperator.Availability{
		InstalledVersion: "0.1.0", UpdateAvailable: true,
		Latest: &selfupdateoperator.Release{Version: "0.2.0"},
	}}
	module := &Module{updates: &updates{operator: operator}}
	change, err := module.resolveUpdateChange(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if change.Version != "0.2.0" {
		t.Fatalf("resolved update = %+v, want a durable request pinned to 0.2.0", change)
	}

	operator.availability = selfupdateoperator.Availability{InstalledVersion: "0.2.0", UpdateAvailable: false}
	if _, err := module.resolveUpdateChange(context.Background(), ""); !errors.Is(err, errNoUpdateAvailable) {
		t.Fatalf("no-update resolution error = %v, want errNoUpdateAvailable", err)
	}
}

func TestSystemUpdateJobsRetryAfterTheAPIRestart(t *testing.T) {
	ctx := context.Background()
	database, err := persistence.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := persistence.RunMigrations(ctx, database); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	auditLog, err := audit.New(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	queue, err := jobs.New(ctx, database, auditLog, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(queue.Close)
	operator := &fakeUpdateOperator{}
	module := New(memoryReader{}, containerInspector{}, WithUpdates(queue, operator))
	if module.initErr != nil {
		t.Fatal(module.initErr)
	}
	job, err := queue.Submit(ctx, updateJobKind, selfupdateoperator.Change{Version: "0.2.0"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var policy string
	if err := database.QueryRowContext(ctx, "SELECT recovery_policy FROM jobs WHERE id = ?", job.ID).Scan(&policy); err != nil {
		t.Fatal(err)
	}
	if policy != string(jobs.RecoveryRetry) {
		t.Fatalf("system update recovery policy = %q, want %q", policy, jobs.RecoveryRetry)
	}
}

func TestApplyUpdateJobProxiesToOperator(t *testing.T) {
	operator := &fakeUpdateOperator{result: selfupdateoperator.Result{
		PreviousVersion: "0.1.0", TargetVersion: "0.2.0", Swapped: true, Activated: true,
	}}
	module := &Module{updates: &updates{operator: operator}}

	var progress []int
	report := func(percent int, _ string) error {
		progress = append(progress, percent)
		return nil
	}
	raw, _ := json.Marshal(selfupdateoperator.Change{Version: "0.2.0"})
	out, err := module.applyUpdateJob(context.Background(), raw, report)
	if err != nil {
		t.Fatalf("applyUpdateJob: %v", err)
	}
	if operator.appliedWith == nil || operator.appliedWith.Version != "0.2.0" {
		t.Fatalf("operator was not asked to apply the requested version: %+v", operator.appliedWith)
	}
	result, ok := out.(selfupdateoperator.Result)
	if !ok || result.TargetVersion != "0.2.0" || !result.Activated {
		t.Fatalf("unexpected job result: %+v", out)
	}
	// Success must be reported before the restart fires (progress climbs, ending high).
	if len(progress) == 0 || progress[len(progress)-1] < 90 {
		t.Fatalf("expected progress to reach near-complete before returning, got %v", progress)
	}
}

func TestApplyUpdateJobPropagatesFailure(t *testing.T) {
	operator := &fakeUpdateOperator{applyErr: errors.New("checksum mismatch")}
	module := &Module{updates: &updates{operator: operator}}

	raw, _ := json.Marshal(selfupdateoperator.Change{})
	if _, err := module.applyUpdateJob(context.Background(), raw, func(int, string) error { return nil }); err == nil {
		t.Fatal("expected the job to fail when the agent apply fails")
	}
}
