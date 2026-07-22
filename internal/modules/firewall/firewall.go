// Package firewall is the control-plane feature module behind the Firewall
// page. It surfaces the node's UFW status and rule table and orchestrates
// enable/disable and per-rule allow/deny/delete changes as durable, audited
// jobs through the privileged firewall operator. There is no control-plane
// state to keep: UFW on the node is the single source of truth, so the module
// is a thin, job-wrapped pass-through to the operator.
package firewall

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/nexa-panel/nexa-panel/internal/platform/audit"
	"github.com/nexa-panel/nexa-panel/internal/platform/jobs"
	"github.com/nexa-panel/nexa-panel/internal/platform/module"
	firewalloperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/firewall"
)

type Module struct {
	jobs     *jobs.Module
	operator firewalloperator.Operator
}

func New(queue *jobs.Module, operator firewalloperator.Operator) (*Module, error) {
	if queue == nil || operator == nil {
		return nil, errors.New("firewall jobs and operator are required")
	}
	m := &Module{jobs: queue, operator: operator}
	if err := queue.RegisterHandler("firewall.apply", m.applyJob); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *Module) Descriptor() module.Descriptor {
	return module.Descriptor{
		ID: "firewall", Name: "Firewall", Version: "0.1.0",
		Description:        "Manage the node's UFW firewall: open and close ports and enable or disable the firewall.",
		Dependencies:       []string{"identity", "jobs"},
		EstimatedIdleBytes: 64 * 1024,
	}
}

func (m *Module) Register(registry module.Registry) error { return m.registerHTTP(registry) }

// Status reports the node's current firewall state. It is a direct pass-through
// to the operator — there is no control-plane state to protect.
func (m *Module) Status(ctx context.Context) (firewalloperator.Status, error) {
	return m.operator.Discover(ctx)
}

// Submit validates a change and queues it as one durable job. The operator
// re-validates and re-checks the node's real state on apply, so a stale or
// forged change never takes effect.
//
// The audit entry is written between validation and submission, and fail-closed:
// a firewall change that cannot be attributed in the audit log is refused rather
// than queued. Submit is the point a human's intent is accepted — the job runs
// later on a worker goroutine with no actor of its own — so it is the only place
// the change is attributable.
func (m *Module) Submit(ctx context.Context, change firewalloperator.Change, actor *string, remoteAddress string) (jobs.Job, error) {
	change.Action = strings.TrimSpace(strings.ToLower(change.Action))
	change.Rule.Protocol = strings.TrimSpace(strings.ToLower(change.Rule.Protocol))
	change.Rule.Action = strings.TrimSpace(strings.ToLower(change.Rule.Action))
	title, err := changeTitle(change)
	if err != nil {
		return jobs.Job{}, err
	}
	entry := audit.Entry{
		ActorUserID: actor, Action: changeAuditAction(change.Action), Subject: changeSubject(change),
		RemoteAddress: remoteAddress, Metadata: m.changeMetadata(ctx, change),
	}
	if err := m.jobs.Audit().RecordSensitive(ctx, entry); err != nil {
		return jobs.Job{}, err
	}
	return m.jobs.SubmitTitled(ctx, "firewall.apply", title, change, actor)
}

// changeAuditAction gives each verb its own audit action, so the log can be
// filtered by what was actually done rather than by a single "firewall.changed".
func changeAuditAction(action string) string {
	switch action {
	case firewalloperator.ActionEnable:
		return "firewall.enabled"
	case firewalloperator.ActionDisable:
		return "firewall.disabled"
	case firewalloperator.ActionAllow:
		return "firewall.rule_allowed"
	case firewalloperator.ActionDeny:
		return "firewall.rule_denied"
	default:
		return "firewall.rule_deleted"
	}
}

// changeSubject names the concrete thing that changed, so "who changed firewall
// rule X" is answerable by filtering on the subject alone.
func changeSubject(change firewalloperator.Change) string {
	if change.Action == firewalloperator.ActionEnable || change.Action == firewalloperator.ActionDisable {
		return "firewall:node"
	}
	return "firewall-rule:" + ruleLabel(change.Rule)
}

// changeMetadata records the target in fielded form plus the before/after state
// the change moves the node between. The before-state is read from the node and
// is deliberately best-effort: an unreachable agent must not stop the change
// from being audited, so the snapshot is simply omitted.
func (m *Module) changeMetadata(ctx context.Context, change firewalloperator.Change) map[string]any {
	metadata := map[string]any{"action": change.Action}
	if change.Action != firewalloperator.ActionEnable && change.Action != firewalloperator.ActionDisable {
		metadata["port"] = change.Rule.Port
		metadata["protocol"] = change.Rule.Protocol
		metadata["from"] = change.Rule.From
		metadata["direction"] = "incoming"
	}
	status, err := m.operator.Discover(ctx)
	if err != nil {
		return metadata
	}
	switch change.Action {
	case firewalloperator.ActionEnable, firewalloperator.ActionDisable:
		metadata["before"] = map[string]any{"active": status.Active}
		metadata["after"] = map[string]any{"active": change.Action == firewalloperator.ActionEnable}
	default:
		metadata["before"] = map[string]any{"rule": matchingRuleAction(status, change.Rule)}
		metadata["after"] = map[string]any{"rule": intendedRuleAction(change)}
	}
	return metadata
}

// matchingRuleAction reports what the node already does with the targeted
// port/protocol/source — "none" when no rule covers it — so the audit row says
// what the change replaced, not just what it asked for.
func matchingRuleAction(status firewalloperator.Status, rule firewalloperator.Rule) string {
	for _, existing := range status.Rules {
		if existing.Port == rule.Port && existing.Protocol == rule.Protocol && existing.From == rule.From {
			return existing.Action
		}
	}
	return "none"
}

func intendedRuleAction(change firewalloperator.Change) string {
	if change.Action == firewalloperator.ActionDelete {
		return "none"
	}
	return change.Action
}

// changeTitle produces a human-readable job title and doubles as the up-front
// guard that the action word is one this module queues.
func changeTitle(change firewalloperator.Change) (string, error) {
	switch change.Action {
	case firewalloperator.ActionEnable:
		return "Enable firewall", nil
	case firewalloperator.ActionDisable:
		return "Disable firewall", nil
	case firewalloperator.ActionAllow:
		return "Allow " + ruleLabel(change.Rule), nil
	case firewalloperator.ActionDeny:
		return "Deny " + ruleLabel(change.Rule), nil
	case firewalloperator.ActionDelete:
		return "Delete rule for " + ruleLabel(change.Rule), nil
	default:
		return "", fmt.Errorf("firewall action must be enable, disable, allow, deny, or delete")
	}
}

func ruleLabel(rule firewalloperator.Rule) string {
	label := rule.Port
	if rule.Protocol != "" {
		label += "/" + rule.Protocol
	}
	if rule.From != "" {
		label += " from " + rule.From
	}
	return label
}

// applyJob applies one firewall change. The operator validates the change and
// re-reads the node's real state, then returns the fresh status as the job
// result.
func (m *Module) applyJob(ctx context.Context, raw json.RawMessage, report func(int, string) error) (any, error) {
	var change firewalloperator.Change
	if err := json.Unmarshal(raw, &change); err != nil || change.Action == "" {
		return nil, errors.New("invalid firewall change request")
	}
	_ = report(30, "Applying the firewall change.")
	status, err := m.operator.Apply(ctx, change)
	if err != nil {
		return nil, err
	}
	_ = report(95, "Firewall change is verified.")
	return status, nil
}
