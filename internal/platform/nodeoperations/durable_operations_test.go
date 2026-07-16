package nodeoperations

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/platform/audit"
	"github.com/nexa-panel/nexa-panel/internal/platform/jobs"
	nodeoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/nodes"
	"github.com/nexa-panel/nexa-panel/internal/platform/persistence"
)

func TestApplyAndRollbackRunThroughDurableJobs(t *testing.T) {
	ctx := context.Background()
	database, err := persistence.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	auditLog, err := audit.New(ctx, database)
	if err != nil {
		t.Fatalf("create audit module: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	jobQueue, err := jobs.NewWithConfig(ctx, database, auditLog, logger, jobs.Config{PollInterval: 10 * time.Millisecond})
	if err != nil {
		t.Fatalf("create jobs module: %v", err)
	}
	jobQueue.Start(context.Background())
	t.Cleanup(jobQueue.Close)
	operator, _ := nodeoperator.NewFileOperator(filepath.Join(t.TempDir(), "etc", "nexa-panel", "probe.conf"))
	if _, err := New(operator, jobQueue); err != nil {
		t.Fatalf("create node operations module: %v", err)
	}
	plan, err := operator.Plan(ctx, nodeoperator.Change{Present: true, Content: "managed=true\n"})
	if err != nil {
		t.Fatalf("plan operation: %v", err)
	}
	actor := "admin-1"
	applyJob, err := jobQueue.Submit(ctx, "node.probe.apply", plan, &actor)
	if err != nil {
		t.Fatalf("submit apply: %v", err)
	}
	waitForJob(t, jobQueue, applyJob.ID, jobs.StateSucceeded)
	observation, err := operator.Observe(ctx)
	if err != nil || !observation.Exists || observation.Digest != plan.Desired.Digest {
		t.Fatalf("observation after apply = %+v, %v", observation, err)
	}

	rollbackJob, err := jobQueue.Submit(ctx, "node.probe.rollback", plan, &actor)
	if err != nil {
		t.Fatalf("submit rollback: %v", err)
	}
	waitForJob(t, jobQueue, rollbackJob.ID, jobs.StateSucceeded)
	observation, err = operator.Observe(ctx)
	if err != nil || observation.Exists {
		t.Fatalf("observation after rollback = %+v, %v", observation, err)
	}
}

func waitForJob(t *testing.T, queue *jobs.Module, id int64, wanted jobs.State) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		job, err := queue.Get(context.Background(), id)
		if err != nil {
			t.Fatalf("get job: %v", err)
		}
		if job.State == wanted {
			return
		}
		if job.State.Terminal() && job.State != wanted {
			t.Fatalf("job ended in %s: %s", job.State, job.Failure)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("job %d did not reach %s", id, wanted)
}
