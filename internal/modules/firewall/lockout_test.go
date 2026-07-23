package firewall

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
	firewalloperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/firewall"
	"github.com/nexa-panel/nexa-panel/internal/platform/persistence"
)

// newGuardedModule builds a firewall module whose operator reports the given
// rule table and whose reverts are armed against a real control database.
func newGuardedModule(t *testing.T, window time.Duration, rules ...firewalloperator.Rule) (*Module, *safeguard.Guard, *audit.Module) {
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
	operator := &fakeFirewallOperator{status: firewalloperator.Status{Installed: true, Active: true, Rules: rules}}
	module, err := New(queue, operator, WithLockoutGuard(guard))
	if err != nil {
		t.Fatal(err)
	}
	return module, guard, auditLog
}

var sshRule = firewalloperator.Rule{Port: "22", Protocol: "tcp", Action: "allow"}
var httpsRule = firewalloperator.Rule{Port: "443", Protocol: "tcp", Action: "allow"}

// Deleting the last rule admitting SSH must not be possible on the strength of a
// typed browser confirmation alone: the server refuses it outright.
func TestDeletingTheOnlyRuleAdmittingSSHIsRefused(t *testing.T) {
	module, _, _ := newGuardedModule(t, time.Hour, sshRule, httpsRule)
	change := firewalloperator.Change{Action: firewalloperator.ActionDelete, Rule: sshRule}
	actor := "user_admin"

	_, err := module.Submit(context.Background(), change, &actor, "198.51.100.4", false)
	var risk *safeguard.RiskError
	if !errors.As(err, &risk) {
		t.Fatalf("Submit err = %v, want a safeguard.RiskError", err)
	}
	if len(risk.Reasons) != 1 || risk.Reasons[0] != "It is the only remaining rule admitting SSH (port 22)." {
		t.Fatalf("reasons = %q", risk.Reasons)
	}
	queued, err := module.jobs.List(context.Background(), 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(queued) != 0 {
		t.Fatalf("a refused change was queued anyway: %+v", queued)
	}
}

// A second rule admitting SSH is the verified alternative the guard is looking
// for, so removing one of the pair is ordinary work.
func TestDeletingOneOfTwoRulesAdmittingSSHIsAllowed(t *testing.T) {
	spare := firewalloperator.Rule{Port: "22", Protocol: "tcp", Action: "allow", From: "203.0.113.0/24"}
	module, _, _ := newGuardedModule(t, time.Hour, sshRule, spare, httpsRule)
	change := firewalloperator.Change{Action: firewalloperator.ActionDelete, Rule: spare}
	actor := "user_admin"

	// The caller is outside the source range, so the rule is not carrying this
	// session either.
	submission, err := module.Submit(context.Background(), change, &actor, "198.51.100.4", false)
	if err != nil {
		t.Fatalf("Submit err = %v", err)
	}
	if submission.Revert != nil {
		t.Fatalf("an unrisky delete armed a revert: %+v", submission.Revert)
	}
}

// The rule admitting the caller's own address on the panel port is the one
// carrying this request; the server knows the peer address and must say so.
func TestDeletingTheRuleCarryingTheCurrentSessionIsRefused(t *testing.T) {
	scoped := firewalloperator.Rule{Port: "443", Protocol: "tcp", Action: "allow", From: "203.0.113.0/24"}
	module, _, _ := newGuardedModule(t, time.Hour, sshRule, httpsRule, scoped)
	change := firewalloperator.Change{Action: firewalloperator.ActionDelete, Rule: scoped}
	actor := "user_admin"

	_, err := module.Submit(context.Background(), change, &actor, "203.0.113.9", false)
	var risk *safeguard.RiskError
	if !errors.As(err, &risk) {
		t.Fatalf("Submit err = %v, want a safeguard.RiskError", err)
	}
	if len(risk.Reasons) != 1 || risk.Reasons[0] != "It is the rule admitting the session you are using right now (203.0.113.9 reaching 443/tcp from 203.0.113.0/24)." {
		t.Fatalf("reasons = %q", risk.Reasons)
	}
}

// Acknowledging the risk applies the change, but only behind an armed revert
// that restores the rule — and the armed revert is a durable, pollable record.
func TestAnAcknowledgedRiskyDeleteArmsARevertThatRestoresTheRule(t *testing.T) {
	module, guard, auditLog := newGuardedModule(t, time.Hour, sshRule, httpsRule)
	change := firewalloperator.Change{Action: firewalloperator.ActionDelete, Rule: sshRule}
	actor := "user_admin"
	ctx := context.Background()

	submission, err := module.Submit(ctx, change, &actor, "198.51.100.4", true)
	if err != nil {
		t.Fatalf("Submit err = %v", err)
	}
	if submission.Revert == nil || !submission.Revert.Armed() {
		t.Fatalf("submission = %+v, want an armed revert", submission)
	}
	if submission.Revert.JobID != submission.Job.ID {
		t.Fatalf("revert job id = %d, want the applying job %d", submission.Revert.JobID, submission.Job.ID)
	}

	stored, err := guard.Get(ctx, "firewall", submission.Revert.ID)
	if err != nil {
		t.Fatal(err)
	}
	var undo firewalloperator.Change
	if err := json.Unmarshal(stored.Payload, &undo); err != nil {
		t.Fatal(err)
	}
	if undo.Action != firewalloperator.ActionAllow || undo.Rule.Port != "22" {
		t.Fatalf("stored undo = %+v, want an allow of 22/tcp", undo)
	}

	// The reasons are also on the audit row, so the log says the change was
	// accepted knowing it could cut access off.
	events, err := auditLog.List(ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, event := range events {
		if event.Action == "firewall.rule_deleted" {
			found = event.Metadata["automaticRevertArmed"] == true
		}
	}
	if !found {
		t.Fatalf("no audited delete recorded the armed revert: %+v", events)
	}
}

// The executor is what the timer calls. It must queue the undo as a real
// firewall job so the rollback is applied and visible like any other change.
func TestExecuteRevertQueuesTheUndoAsAJob(t *testing.T) {
	module, _, _ := newGuardedModule(t, time.Hour, sshRule)
	ctx := context.Background()
	payload, err := json.Marshal(firewalloperator.Change{Action: firewalloperator.ActionAllow, Rule: sshRule})
	if err != nil {
		t.Fatal(err)
	}
	jobID, err := module.executeRevert(ctx, safeguard.Revert{Domain: "firewall", Payload: payload})
	if err != nil || jobID == 0 {
		t.Fatalf("executeRevert = %d, %v", jobID, err)
	}
	queued, err := module.jobs.Get(ctx, jobID)
	if err != nil {
		t.Fatal(err)
	}
	if queued.Title != "Automatic rollback: Allow 22/tcp" {
		t.Fatalf("revert job title = %q", queued.Title)
	}
}

// Without a durable place to record the pending revert, a lockout-capable change
// must be refused rather than applied with no way back.
func TestARiskyChangeIsRefusedWithoutAGuard(t *testing.T) {
	module, _, _ := newFirewallModule(t)
	module.operator = &fakeFirewallOperator{status: firewalloperator.Status{
		Installed: true, Active: true, Rules: []firewalloperator.Rule{sshRule},
	}}
	actor := "user_admin"
	change := firewalloperator.Change{Action: firewalloperator.ActionDelete, Rule: sshRule}

	if _, err := module.Submit(context.Background(), change, &actor, "198.51.100.4", true); err == nil {
		t.Fatal("a risky change was accepted with no guard to arm a revert")
	}
}

// A firewall that is not enforcing cannot strand anyone, so its rule edits stay
// friction-free.
func TestAnInactiveFirewallHasNoLockoutRisk(t *testing.T) {
	if reasons := assessLockout(firewalloperator.Status{Installed: true}, firewalloperator.Change{
		Action: firewalloperator.ActionDelete, Rule: sshRule,
	}, "203.0.113.9"); len(reasons) != 0 {
		t.Fatalf("reasons = %q, want none while the firewall is inactive", reasons)
	}
}

// A port range covering SSH is just as load-bearing as a rule naming port 22.
func TestARangeRuleCountsAsAdmittingTheAccessPort(t *testing.T) {
	rangeRule := firewalloperator.Rule{Port: "20:30", Protocol: "tcp", Action: "allow"}
	status := firewalloperator.Status{Installed: true, Active: true, Rules: []firewalloperator.Rule{rangeRule, httpsRule}}
	reasons := assessLockout(status, firewalloperator.Change{Action: firewalloperator.ActionDelete, Rule: rangeRule}, "")
	if len(reasons) != 1 {
		t.Fatalf("reasons = %q, want the only-rule-admitting-SSH reason", reasons)
	}
}

// Denying an access port for the caller's own address closes the same door as
// deleting the allow rule.
func TestDenyingAnAccessPortForTheCallerIsRisky(t *testing.T) {
	status := firewalloperator.Status{Installed: true, Active: true, Rules: []firewalloperator.Rule{sshRule, httpsRule}}
	change := firewalloperator.Change{
		Action: firewalloperator.ActionDeny,
		Rule:   firewalloperator.Rule{Port: "22", Protocol: "tcp", Action: "deny"},
	}
	if reasons := assessLockout(status, change, "203.0.113.9"); len(reasons) == 0 {
		t.Fatal("denying SSH for everyone was judged safe")
	}
}
