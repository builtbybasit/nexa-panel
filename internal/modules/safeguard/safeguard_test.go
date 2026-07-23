package safeguard

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/platform/persistence"
	"github.com/uptrace/bun"
)

func newGuard(t *testing.T, window time.Duration) (*Guard, *bun.DB) {
	t.Helper()
	database := newDatabase(t)
	guard, err := New(database, slog.New(slog.NewTextHandler(io.Discard, nil)), WithWindow(window))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(guard.Close)
	return guard, database
}

func newDatabase(t *testing.T) *bun.DB {
	t.Helper()
	database, err := persistence.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := persistence.RunMigrations(context.Background(), database); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	return database
}

// waitFor polls a condition so a timer-driven assertion does not depend on a
// fixed sleep being long enough on a loaded machine.
func waitFor(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// The whole point of a server-side timer: nobody confirms, so the change is
// undone without any further contact from the browser that made it.
func TestAnUnconfirmedRevertFiresWhenTheWindowExpires(t *testing.T) {
	guard, _ := newGuard(t, 30*time.Millisecond)
	var executed atomic.Int64
	if err := guard.Register("firewall", func(_ context.Context, revert Revert) (int64, error) {
		var payload map[string]string
		_ = json.Unmarshal(revert.Payload, &payload)
		if payload["undo"] != "allow 22/tcp" {
			t.Errorf("executor received payload %v", payload)
		}
		executed.Add(1)
		return 77, nil
	}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	armed, err := guard.Arm(ctx, ArmRequest{
		Domain: "firewall", Subject: "22/tcp", Reasons: []string{"only rule admitting SSH"},
		Payload: map[string]string{"undo": "allow 22/tcp"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !armed.Armed() || armed.ExpiresAt.Before(armed.ArmedAt) {
		t.Fatalf("armed revert = %+v", armed)
	}

	waitFor(t, "the revert to execute", func() bool { return executed.Load() == 1 })
	waitFor(t, "the revert to be recorded", func() bool {
		stored, err := guard.Get(ctx, "firewall", armed.ID)
		return err == nil && stored.State == StateReverted && stored.RevertJobID == 77
	})
}

// Confirming from a working session is the operator's proof of access; the
// revert must then never fire.
func TestConfirmingDisarmsTheRevert(t *testing.T) {
	guard, _ := newGuard(t, 40*time.Millisecond)
	var executed atomic.Int64
	if err := guard.Register("services", func(context.Context, Revert) (int64, error) {
		executed.Add(1)
		return 0, nil
	}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	armed, err := guard.Arm(ctx, ArmRequest{Domain: "services", Subject: "nginx.service", Payload: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	confirmed, err := guard.Confirm(ctx, "services", armed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if confirmed.State != StateConfirmed || confirmed.ConfirmedAt == nil {
		t.Fatalf("confirmed revert = %+v", confirmed)
	}
	time.Sleep(120 * time.Millisecond)
	if executed.Load() != 0 {
		t.Fatalf("a confirmed revert executed %d times", executed.Load())
	}
	stored, err := guard.Get(ctx, "services", armed.ID)
	if err != nil || stored.State != StateConfirmed {
		t.Fatalf("stored revert = %+v, err = %v", stored, err)
	}
}

// The revert has to survive the API process that armed it. A second guard over
// the same database stands in for a restart: the row is still armed, its
// deadline has already passed, and Start must fire it immediately.
func TestStartFiresARevertLeftBehindByARestart(t *testing.T) {
	database := newDatabase(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	first, err := New(database, logger, WithWindow(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Register("firewall", func(context.Context, Revert) (int64, error) {
		t.Error("the first guard must not fire the revert itself")
		return 0, nil
	}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	armed, err := first.Arm(ctx, ArmRequest{Domain: "firewall", Subject: "22/tcp", Payload: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	// The process dies with the revert still armed and, by the time it comes
	// back, overdue.
	first.Close()
	if _, err := database.NewUpdate().Model((*revertModel)(nil)).
		Set("expires_at = ?", time.Now().UTC().Add(-time.Minute)).Where("id = ?", armed.ID).Exec(ctx); err != nil {
		t.Fatal(err)
	}

	second, err := New(database, logger, WithWindow(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(second.Close)
	var executed atomic.Int64
	if err := second.Register("firewall", func(context.Context, Revert) (int64, error) {
		executed.Add(1)
		return 9, nil
	}); err != nil {
		t.Fatal(err)
	}
	second.Start(ctx)

	waitFor(t, "the reconciled revert to execute", func() bool { return executed.Load() == 1 })
	waitFor(t, "the reconciled revert to be recorded", func() bool {
		stored, err := second.Get(ctx, "firewall", armed.ID)
		return err == nil && stored.State == StateReverted
	})
}

// A revert whose undo fails must say so rather than being recorded as a
// successful rollback the operator can trust.
func TestAFailedRevertIsRecordedAsFailed(t *testing.T) {
	guard, _ := newGuard(t, 20*time.Millisecond)
	if err := guard.Register("firewall", func(context.Context, Revert) (int64, error) {
		return 0, errors.New("agent unreachable")
	}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	armed, err := guard.Arm(ctx, ArmRequest{Domain: "firewall", Subject: "22/tcp", Payload: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the failure to be recorded", func() bool {
		stored, err := guard.Get(ctx, "firewall", armed.ID)
		return err == nil && stored.State == StateRevertFailed && stored.Failure == "agent unreachable"
	})
}

// Reverts are scoped by domain so the Services page can never confirm away the
// firewall's rollback, or the other way round.
func TestRevertsAreScopedToTheirDomain(t *testing.T) {
	guard, _ := newGuard(t, time.Hour)
	ctx := context.Background()
	armed, err := guard.Arm(ctx, ArmRequest{Domain: "firewall", Subject: "22/tcp", Payload: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := guard.Confirm(ctx, "services", armed.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-domain confirm err = %v, want ErrNotFound", err)
	}
	listed, err := guard.Recent(ctx, "services", 10)
	if err != nil || len(listed) != 0 {
		t.Fatalf("services domain listed %+v (err %v)", listed, err)
	}
	if listed, err = guard.Recent(ctx, "firewall", 10); err != nil || len(listed) != 1 {
		t.Fatalf("firewall domain listed %+v (err %v)", listed, err)
	}
}
