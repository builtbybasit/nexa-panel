// Package firewall is the control-panel feature module behind the Firewall
// page. It surfaces the node's UFW status and rule table and orchestrates
// enable/disable and per-rule allow/deny/delete changes as durable, audited
// jobs through the privileged firewall operator. UFW on the node is the single
// source of truth for the rule table, so the module keeps no copy of it.
//
// The one piece of control-panel state it does keep is the armed automatic
// revert. A rule change can strand the operator outside their own server, and
// only the control panel can judge that: it knows the caller's peer address and
// the whole rule table. A change judged lockout-capable is refused unless the
// caller acknowledges the risk, and then it is applied with a timed revert armed
// behind it — undone automatically unless a still-working session confirms
// inside the window. See internal/modules/safeguard.
package firewall

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/nexa-panel/nexa-panel/internal/modules/safeguard"
	"github.com/nexa-panel/nexa-panel/internal/platform/audit"
	"github.com/nexa-panel/nexa-panel/internal/platform/jobs"
	"github.com/nexa-panel/nexa-panel/internal/platform/module"
	firewalloperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/firewall"
)

// lockoutDomain names this module's reverts inside the shared guard.
const lockoutDomain = "firewall"

type Module struct {
	jobs     *jobs.Module
	operator firewalloperator.Operator
	guard    *safeguard.Guard
}

// Option configures optional module behaviour.
type Option func(*Module)

// WithLockoutGuard arms timed automatic reverts for lockout-capable rule
// changes. Without it the module has nowhere durable to record a pending
// revert, so it refuses those changes outright rather than applying one it
// cannot undo.
func WithLockoutGuard(guard *safeguard.Guard) Option {
	return func(m *Module) { m.guard = guard }
}

func New(queue *jobs.Module, operator firewalloperator.Operator, options ...Option) (*Module, error) {
	if queue == nil || operator == nil {
		return nil, errors.New("firewall jobs and operator are required")
	}
	m := &Module{jobs: queue, operator: operator}
	for _, option := range options {
		option(m)
	}
	if err := queue.RegisterHandler("firewall.apply", m.applyJob); err != nil {
		return nil, err
	}
	if m.guard != nil {
		if err := m.guard.Register(lockoutDomain, m.executeRevert); err != nil {
			return nil, err
		}
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

// Start reconciles armed reverts left behind by a previous API process. The
// guard is shared with the services module and both start it; the second call is
// a no-op, which is what keeps the reconcile from running twice.
func (m *Module) Start(ctx context.Context) {
	if m.guard != nil {
		m.guard.Start(ctx)
	}
}

// Close stops the countdowns. Anything still armed stays armed in the table and
// is rescheduled by the next Start, so a shutdown cannot silently disarm a
// pending rollback.
func (m *Module) Close() {
	if m.guard != nil {
		m.guard.Close()
	}
}

// Status reports the node's current firewall state. It is a direct pass-through
// to the operator: the rule table itself is never mirrored here.
func (m *Module) Status(ctx context.Context) (firewalloperator.Status, error) {
	return m.operator.Discover(ctx)
}

// Submission is the outcome of an accepted change: the job applying it and,
// when the change could have locked the operator out, the armed revert that
// undoes it unless confirmed in time.
type Submission struct {
	Job    jobs.Job          `json:"job"`
	Revert *safeguard.Revert `json:"revert,omitempty"`
	Window int               `json:"revertWindowSeconds,omitempty"`
}

// Submit validates a change and queues it as one durable job. The operator
// re-validates and re-checks the node's real state on apply, so a stale or
// forged change never takes effect.
//
// Before anything is queued the change is judged against the node's live rule
// table and the caller's own peer address. A change that can strand the operator
// is refused with a *safeguard.RiskError unless acknowledged is set, and an
// acknowledged one is applied only with a timed revert armed behind it.
//
// The audit entry is written between validation and submission, and fail-closed:
// a firewall change that cannot be attributed in the audit log is refused rather
// than queued. Submit is the point a human's intent is accepted — the job runs
// later on a worker goroutine with no actor of its own — so it is the only place
// the change is attributable.
func (m *Module) Submit(ctx context.Context, change firewalloperator.Change, actor *string, remoteAddress string, acknowledged bool) (Submission, error) {
	change.Action = strings.TrimSpace(strings.ToLower(change.Action))
	change.Rule.Protocol = strings.TrimSpace(strings.ToLower(change.Rule.Protocol))
	change.Rule.Action = strings.TrimSpace(strings.ToLower(change.Rule.Action))
	title, err := changeTitle(change)
	if err != nil {
		return Submission{}, err
	}
	// The status read is best-effort for the audit snapshot but authoritative
	// for the lockout judgement, so both share the one round-trip.
	status, statusErr := m.operator.Discover(ctx)
	var reasons []string
	if statusErr == nil {
		reasons = assessLockout(status, change, remoteAddress)
	}
	revertChange, err := m.prepareRevert(reasons, change, acknowledged)
	if err != nil {
		return Submission{}, err
	}
	entry := audit.Entry{
		ActorUserID: actor, Action: changeAuditAction(change.Action), Subject: changeSubject(change),
		RemoteAddress: remoteAddress, Metadata: changeMetadata(status, statusErr, change, reasons),
	}
	if err := m.jobs.Audit().RecordSensitive(ctx, entry); err != nil {
		return Submission{}, err
	}
	job, err := m.jobs.SubmitTitled(ctx, "firewall.apply", title, change, actor)
	if err != nil {
		return Submission{}, err
	}
	submission := Submission{Job: job}
	if len(reasons) == 0 {
		return submission, nil
	}
	revert, err := m.guard.Arm(ctx, safeguard.ArmRequest{
		Domain: lockoutDomain, Subject: ruleLabel(change.Rule), Reasons: reasons,
		Summary: revertSummary(revertChange),
		Payload: revertChange, JobID: job.ID, ActorUserID: actor,
	})
	if err != nil {
		return Submission{}, err
	}
	submission.Revert = &revert
	submission.Window = int(m.guard.Window().Seconds())
	return submission, nil
}

// prepareRevert turns a lockout judgement into the change that would undo it,
// refusing the whole request when the risk is unacknowledged or when no armed
// revert can be recorded. Applying a lockout-capable change with no way back is
// exactly the outage this module exists to prevent, so an unconfigured guard
// fails closed rather than quietly proceeding.
func (m *Module) prepareRevert(reasons []string, change firewalloperator.Change, acknowledged bool) (firewalloperator.Change, error) {
	if len(reasons) == 0 {
		return firewalloperator.Change{}, nil
	}
	if !acknowledged {
		return firewalloperator.Change{}, &safeguard.RiskError{Reasons: reasons}
	}
	if m.guard == nil {
		return firewalloperator.Change{}, errors.New("this change can lock you out and automatic rollback is unavailable on this node, so it was refused")
	}
	return revertFor(change)
}

// executeRevert undoes one armed change by queueing it as an ordinary firewall
// job, so the revert is visible, audited by the job trail, and applied through
// the same validated operator path as the change it undoes.
func (m *Module) executeRevert(ctx context.Context, revert safeguard.Revert) (int64, error) {
	var change firewalloperator.Change
	if err := json.Unmarshal(revert.Payload, &change); err != nil || change.Action == "" {
		return 0, errors.New("the stored revert instruction could not be read")
	}
	title, err := changeTitle(change)
	if err != nil {
		return 0, err
	}
	job, err := m.jobs.SubmitTitled(ctx, "firewall.apply", "Automatic rollback: "+title, change, revert.ActorUserID)
	if err != nil {
		return 0, err
	}
	return job.ID, nil
}

// Reverts lists this module's recent armed and settled reverts for the UI.
func (m *Module) Reverts(ctx context.Context) ([]safeguard.Revert, error) {
	if m.guard == nil {
		return nil, nil
	}
	return m.guard.Recent(ctx, lockoutDomain, 20)
}

// ConfirmRevert disarms one revert. The request having arrived over an
// authenticated session is the verification that access still works.
func (m *Module) ConfirmRevert(ctx context.Context, id string) (safeguard.Revert, error) {
	if m.guard == nil {
		return safeguard.Revert{}, safeguard.ErrNotFound
	}
	return m.guard.Confirm(ctx, lockoutDomain, id)
}

// RevertWindowSeconds is how long an armed revert waits, for the UI copy.
func (m *Module) RevertWindowSeconds() int {
	if m.guard == nil {
		return 0
	}
	return int(m.guard.Window().Seconds())
}

// revertSummary is the sentence the page shows about how access comes back. The
// change was built by revertFor, so its verb is always one changeTitle knows.
func revertSummary(change firewalloperator.Change) string {
	title, err := changeTitle(change)
	if err != nil {
		return "Access is restored automatically unless you confirm you are still connected."
	}
	return "Access is restored automatically (" + strings.ToLower(title) + ") unless you confirm you are still connected."
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
// the change moves the node between, and the lockout reasons when the change was
// accepted despite them. The before-state is read from the node and is
// deliberately best-effort: an unreachable agent must not stop the change from
// being audited, so the snapshot is simply omitted.
func changeMetadata(status firewalloperator.Status, statusErr error, change firewalloperator.Change, reasons []string) map[string]any {
	metadata := map[string]any{"action": change.Action}
	if change.Action != firewalloperator.ActionEnable && change.Action != firewalloperator.ActionDisable {
		metadata["port"] = change.Rule.Port
		metadata["protocol"] = change.Rule.Protocol
		metadata["from"] = change.Rule.From
		metadata["direction"] = "incoming"
	}
	if len(reasons) > 0 {
		metadata["lockoutReasons"] = reasons
		metadata["automaticRevertArmed"] = true
	}
	if statusErr != nil {
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
