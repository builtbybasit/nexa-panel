package nodes

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestPlanApplyObserveAndRollback(t *testing.T) {
	target := filepath.Join(t.TempDir(), "etc", "nexa-panel", "probe.conf")
	operator, err := NewFileOperator(target)
	if err != nil {
		t.Fatalf("NewFileOperator returned an error: %v", err)
	}
	plan, err := operator.Plan(context.Background(), Change{Present: true, Content: "managed=true\n"})
	if err != nil {
		t.Fatalf("Plan returned an error: %v", err)
	}
	if plan.Action != "create" || !plan.Changed || plan.Before.Exists || !plan.Desired.Exists {
		t.Fatalf("unexpected plan: %+v", plan)
	}
	applied, err := operator.Apply(context.Background(), plan)
	if err != nil || !applied.Exists || applied.Content != "managed=true\n" {
		t.Fatalf("Apply = %+v, %v", applied, err)
	}
	rolledBack, err := operator.Rollback(context.Background(), plan)
	if err != nil || rolledBack.Exists {
		t.Fatalf("Rollback = %+v, %v", rolledBack, err)
	}
}

func TestApplyRejectsStateChangedAfterPlan(t *testing.T) {
	target := filepath.Join(t.TempDir(), "probe.conf")
	operator, _ := NewFileOperator(target)
	plan, err := operator.Plan(context.Background(), Change{Present: true, Content: "planned"})
	if err != nil {
		t.Fatalf("Plan returned an error: %v", err)
	}
	if err := os.WriteFile(target, []byte("unexpected"), 0o644); err != nil {
		t.Fatalf("write unexpected state: %v", err)
	}
	if _, err := operator.Apply(context.Background(), plan); err == nil {
		t.Fatal("Apply should reject changed state")
	}
}

func TestObserveRejectsSymlinkTarget(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "probe.conf")
	if err := os.Symlink(filepath.Join(directory, "elsewhere"), target); err != nil {
		t.Fatalf("create symlink: %v", err)
	}
	operator, _ := NewFileOperator(target)
	if _, err := operator.Observe(context.Background()); err == nil {
		t.Fatal("Observe should reject a symlink target")
	}
}
