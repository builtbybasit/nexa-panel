package firewall

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/platform/audit"
	"github.com/nexa-panel/nexa-panel/internal/platform/jobs"
	firewalloperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/firewall"
	"github.com/nexa-panel/nexa-panel/internal/platform/persistence"
	"github.com/uptrace/bun"
)

type fakeFirewallOperator struct {
	status firewalloperator.Status
}

func (f *fakeFirewallOperator) Discover(context.Context) (firewalloperator.Status, error) {
	return f.status, nil
}

func (f *fakeFirewallOperator) Apply(context.Context, firewalloperator.Change) (firewalloperator.Status, error) {
	return f.status, nil
}

func newFirewallModule(t *testing.T) (*Module, *audit.Module, *bun.DB) {
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
	auditLog, err := audit.New(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	queue, err := jobs.NewWithConfig(ctx, database, auditLog, slog.New(slog.NewTextHandler(io.Discard, nil)), jobs.Config{PollInterval: 5 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	operator := &fakeFirewallOperator{status: firewalloperator.Status{
		Installed: true, Active: true,
		Rules: []firewalloperator.Rule{{Port: "8080", Protocol: "tcp", Action: "deny"}},
	}}
	module, err := New(queue, operator)
	if err != nil {
		t.Fatal(err)
	}
	return module, auditLog, database
}

// A firewall change must be answerable from the audit table alone: who, which
// verb, which port, and what the node did with that port beforehand.
func TestSubmitRecordsATargetedFirewallEntry(t *testing.T) {
	module, auditLog, _ := newFirewallModule(t)
	actor := "user_admin"
	change := firewalloperator.Change{
		Action: firewalloperator.ActionAllow,
		Rule:   firewalloperator.Rule{Port: "8080", Protocol: "TCP"},
	}
	if _, err := module.Submit(context.Background(), change, &actor, "203.0.113.9"); err != nil {
		t.Fatalf("Submit returned an error: %v", err)
	}

	events, err := auditLog.List(context.Background(), 50)
	if err != nil {
		t.Fatal(err)
	}
	var recorded *audit.Event
	for index := range events {
		if events[index].Action == "firewall.rule_allowed" {
			recorded = &events[index]
			break
		}
	}
	if recorded == nil {
		t.Fatalf("no firewall.rule_allowed entry in %+v", events)
	}
	if recorded.ActorUserID == nil || *recorded.ActorUserID != actor {
		t.Fatalf("actor = %v, want %s", recorded.ActorUserID, actor)
	}
	if recorded.Subject != "firewall-rule:8080/tcp" || recorded.RemoteAddress != "203.0.113.9" {
		t.Fatalf("subject = %q, remote = %q", recorded.Subject, recorded.RemoteAddress)
	}
	if recorded.Metadata["port"] != "8080" || recorded.Metadata["protocol"] != "tcp" {
		t.Fatalf("metadata = %+v", recorded.Metadata)
	}
	before, _ := recorded.Metadata["before"].(map[string]any)
	after, _ := recorded.Metadata["after"].(map[string]any)
	if before["rule"] != "deny" || after["rule"] != "allow" {
		t.Fatalf("before/after = %+v / %+v", before, after)
	}
}

// Dropping the table stands in for an audit log that cannot be written: the
// firewall change must be refused, not applied unaudited.
func TestSubmitRefusesAnUnauditableChange(t *testing.T) {
	module, _, database := newFirewallModule(t)
	if _, err := database.ExecContext(context.Background(), "DROP TABLE audit_events"); err != nil {
		t.Fatal(err)
	}
	actor := "user_admin"
	change := firewalloperator.Change{Action: firewalloperator.ActionDisable}
	if _, err := module.Submit(context.Background(), change, &actor, ""); !errors.Is(err, audit.ErrUnauditable) {
		t.Fatalf("Submit err = %v, want audit.ErrUnauditable", err)
	}
	queued, err := module.jobs.List(context.Background(), 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(queued) != 0 {
		t.Fatalf("an unauditable change was queued anyway: %+v", queued)
	}
}
