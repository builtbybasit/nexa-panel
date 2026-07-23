package firewall

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/modules/safeguard"
	firewalloperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/firewall"
	"github.com/nexa-panel/nexa-panel/internal/platform/persistence"
)

// nodeRunner runs ufw inside the disposable nexa-node container, so the guard
// drives the same binary a real node runs rather than a fake. It skips when the
// container is not available, which is every environment except a developer
// machine with the test node up.
type nodeRunner struct{}

func (nodeRunner) Run(ctx context.Context, command firewalloperator.Command) ([]byte, error) {
	args := append([]string{"exec", "nexa-node", command.Name}, command.Args...)
	return exec.CommandContext(ctx, "docker", args...).CombinedOutput()
}

func requireNode(t *testing.T) firewalloperator.Runner {
	t.Helper()
	runner := nodeRunner{}
	if _, err := runner.Run(context.Background(), firewalloperator.Command{Name: "ufw", Args: []string{"version"}}); err != nil {
		t.Skip("the nexa-node container with ufw is not available")
	}
	return runner
}

// This is the proof that the timed rollback is real: a rule is deleted from a
// live ufw, nobody confirms, and the guard's own timer puts it back on the node.
// Everything after Arm happens with no further contact from any client.
func TestTheArmedRevertRestoresTheRuleOnTheNode(t *testing.T) {
	runner := requireNode(t)
	ctx := context.Background()
	operator, err := firewalloperator.NewHostOperator(runner)
	if err != nil {
		t.Fatal(err)
	}

	// The test node's panel port is allowed first so enabling the firewall
	// cannot strand the container's own published port, and the node is left
	// inactive again however the test ends.
	guardRule := firewalloperator.Rule{Port: "8888", Protocol: "tcp", Action: firewalloperator.ActionAllow}
	if _, err := operator.Apply(ctx, firewalloperator.Change{Action: firewalloperator.ActionAllow, Rule: guardRule}); err != nil {
		t.Fatal(err)
	}
	if _, err := operator.Apply(ctx, firewalloperator.Change{Action: firewalloperator.ActionEnable}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = operator.Apply(context.Background(), firewalloperator.Change{Action: firewalloperator.ActionDisable})
	})

	target := firewalloperator.Rule{Port: "22", Protocol: "tcp", Action: firewalloperator.ActionAllow}
	if _, err := operator.Apply(ctx, firewalloperator.Change{Action: firewalloperator.ActionAllow, Rule: target}); err != nil {
		t.Fatal(err)
	}

	database, err := persistence.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := persistence.RunMigrations(ctx, database); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	guard, err := safeguard.New(database, slog.New(slog.NewTextHandler(io.Discard, nil)), safeguard.WithWindow(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(guard.Close)
	if err := guard.Register(lockoutDomain, func(ctx context.Context, revert safeguard.Revert) (int64, error) {
		var undo firewalloperator.Change
		if err := json.Unmarshal(revert.Payload, &undo); err != nil {
			return 0, err
		}
		_, err := operator.Apply(ctx, undo)
		return 0, err
	}); err != nil {
		t.Fatal(err)
	}

	// The change the operator would have to acknowledge, and its undo.
	change := firewalloperator.Change{Action: firewalloperator.ActionDelete, Rule: target}
	undo, err := revertFor(change)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := operator.Apply(ctx, change); err != nil {
		t.Fatal(err)
	}
	if admits(t, operator, "22") {
		t.Fatal("the rule survived the delete, so the test proves nothing")
	}
	if _, err := guard.Arm(ctx, safeguard.ArmRequest{
		Domain: lockoutDomain, Subject: "22/tcp", Payload: undo,
		Reasons: []string{"It is the only remaining rule admitting SSH (port 22)."},
	}); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(30 * time.Second)
	for !admits(t, operator, "22") {
		if time.Now().After(deadline) {
			t.Fatal("the armed revert never restored the rule on the node")
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func admits(t *testing.T, operator *firewalloperator.HostOperator, port string) bool {
	t.Helper()
	status, err := operator.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, rule := range status.Rules {
		if rule.Port == port && rule.Action == firewalloperator.ActionAllow && !strings.EqualFold(rule.From, "anywhere") {
			return true
		}
	}
	return false
}
