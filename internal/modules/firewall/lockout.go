package firewall

import (
	"fmt"
	"net"
	"strconv"
	"strings"

	firewalloperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/firewall"
)

// accessPorts are the ports an operator gets back into the server through: SSH
// and the panel's HTTP/HTTPS ingress. They mirror the survival ports the
// operator re-asserts before it enables UFW, and are the set the control panel
// refuses to let the last rule for disappear.
//
// A node whose sshd listens somewhere unusual is covered by the second guard
// below, not this list: the rule carrying the caller's own session is protected
// whatever port it names.
var accessPorts = []accessPort{
	{number: 22, label: "SSH"},
	{number: 80, label: "the panel over HTTP", panel: true},
	{number: 443, label: "the panel over HTTPS", panel: true},
}

type accessPort struct {
	number int
	label  string
	// panel marks the ports this very request could have arrived on. A request
	// reaching the control panel came through the panel's ingress, never over
	// SSH, so only these ports can be carrying the caller's session.
	panel bool
}

// assessLockout decides whether a change can cut the operator off, and says why
// in operator-facing language. The control panel decides this, not the browser:
// only the server knows the caller's peer address and the node's whole rule
// table, and a UI-only guard is bypassed by any direct API call.
//
// Two situations qualify:
//
//  1. The change removes or overrides the rule that is admitting this very
//     request. Losing it disconnects the session issuing the change.
//  2. The change removes the last rule admitting SSH or the panel. Even if the
//     caller reaches the box another way today, no verified alternative remains.
func assessLockout(status firewalloperator.Status, change firewalloperator.Change, peer string) []string {
	// Only a firewall that is actually enforcing can strand anyone, and only the
	// rule verbs change who is admitted.
	if !status.Active {
		return nil
	}
	var reasons []string
	switch change.Action {
	case firewalloperator.ActionDelete:
		rule := change.Rule
		if rule.Action != firewalloperator.ActionAllow {
			return nil
		}
		if carriesSession(rule, peer) {
			reasons = append(reasons, sessionReason(rule, peer))
		}
		for _, port := range accessPorts {
			if !ruleAdmits(rule, port.number) {
				continue
			}
			if !anyOtherRuleAdmits(status, rule, port.number) {
				reasons = append(reasons, fmt.Sprintf("It is the only remaining rule admitting %s (port %d).", port.label, port.number))
			}
		}
	case firewalloperator.ActionDeny:
		// A deny rule over an access port shuts the door just as firmly as
		// deleting the allow rule that holds it open. The per-port check reports,
		// in deny-specific language, every access port this rule closes for the
		// caller's own address — including the port carrying the current session.
		// There is deliberately no separate "carries session" line: sessionReason
		// is worded for the allow/delete path ("the rule admitting the session"),
		// so reusing it here would wrongly claim a deny rule admits the session.
		rule := change.Rule
		for _, port := range accessPorts {
			if ruleAdmits(rule, port.number) && fromCovers(rule.From, peer) {
				reasons = append(reasons, fmt.Sprintf("It denies %s (port %d) for the address you are connected from.", port.label, port.number))
			}
		}
	}
	return dedupe(reasons)
}

// carriesSession reports whether the rule is what is letting this request in:
// it covers an access port and its source range covers the caller's address.
func carriesSession(rule firewalloperator.Rule, peer string) bool {
	if peer == "" || !fromCovers(rule.From, peer) {
		return false
	}
	for _, port := range accessPorts {
		if port.panel && ruleAdmits(rule, port.number) {
			return true
		}
	}
	return false
}

func sessionReason(rule firewalloperator.Rule, peer string) string {
	return fmt.Sprintf("It is the rule admitting the session you are using right now (%s reaching %s).", peer, ruleLabel(rule))
}

// anyOtherRuleAdmits looks for a second allow rule covering the same port. The
// candidate itself is skipped by identity, and UFW's IPv6 twin of the same rule
// is skipped too: it is one rule reported twice, so it is not an alternative.
func anyOtherRuleAdmits(status firewalloperator.Status, candidate firewalloperator.Rule, port int) bool {
	for _, existing := range status.Rules {
		if existing.Action != firewalloperator.ActionAllow {
			continue
		}
		if sameRuleBody(existing, candidate) {
			continue
		}
		if ruleAdmits(existing, port) {
			return true
		}
	}
	return false
}

func sameRuleBody(a, b firewalloperator.Rule) bool {
	return a.Port == b.Port && a.Protocol == b.Protocol && a.From == b.From
}

// ruleAdmits reports whether a rule's port spec covers the given port. An empty
// port is UFW's "Anywhere" row, which covers everything; a protocol other than
// tcp does not carry SSH or the panel.
func ruleAdmits(rule firewalloperator.Rule, port int) bool {
	if rule.Protocol != "" && rule.Protocol != "tcp" {
		return false
	}
	if rule.Port == "" {
		return true
	}
	low, high, ok := portRange(rule.Port)
	return ok && port >= low && port <= high
}

func portRange(spec string) (int, int, bool) {
	low, high, found := strings.Cut(spec, ":")
	start, err := strconv.Atoi(strings.TrimSpace(low))
	if err != nil {
		return 0, 0, false
	}
	if !found {
		return start, start, true
	}
	end, err := strconv.Atoi(strings.TrimSpace(high))
	if err != nil {
		return 0, 0, false
	}
	return start, end, true
}

// fromCovers reports whether a rule's source restriction admits an address. An
// empty source is UFW's "Anywhere", which admits every caller.
func fromCovers(from, peer string) bool {
	if strings.TrimSpace(from) == "" {
		return true
	}
	address := net.ParseIP(peer)
	if address == nil {
		return false
	}
	if _, network, err := net.ParseCIDR(from); err == nil {
		return network.Contains(address)
	}
	return net.ParseIP(from).Equal(address)
}

// revertFor builds the change that undoes a risky one, so the armed revert can
// put the node back exactly as it was.
func revertFor(change firewalloperator.Change) (firewalloperator.Change, error) {
	switch change.Action {
	case firewalloperator.ActionDelete:
		// The deleted rule already names whether it was allow or deny, so
		// re-adding it is the same rule body under its own verb.
		return firewalloperator.Change{Action: change.Rule.Action, Rule: change.Rule}, nil
	case firewalloperator.ActionDeny, firewalloperator.ActionAllow:
		rule := change.Rule
		rule.Action = change.Action
		return firewalloperator.Change{Action: firewalloperator.ActionDelete, Rule: rule}, nil
	default:
		return firewalloperator.Change{}, fmt.Errorf("no automatic revert exists for the %s action", change.Action)
	}
}

func dedupe(values []string) []string {
	if len(values) < 2 {
		return values
	}
	seen := make(map[string]struct{}, len(values))
	unique := values[:0]
	for _, value := range values {
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		unique = append(unique, value)
	}
	return unique
}
