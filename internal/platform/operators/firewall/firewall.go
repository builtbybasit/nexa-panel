// Package firewall is the privileged operator behind the Firewall page. It
// drives UFW (the Uncomplicated Firewall) on the node: reading the current
// status and rule table, opening and closing ports, and enabling or disabling
// the firewall itself.
//
// Enabling the firewall is the one dangerous operation — a default-deny policy
// with no allow rules would sever both SSH and the panel the moment it takes
// effect. So enable is never a bare `ufw enable`: the operator first re-asserts
// allow rules for SSH (every port sshd actually listens on, plus 22 as a
// floor) and for the panel's HTTP/HTTPS ports, then enables. That safety net
// lives in the operator, not the caller, so it cannot be bypassed.
package firewall

import (
	"context"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
)

// Panel HTTP/HTTPS ports. The panel is served by Nginx on 80 (and 443 once a
// hostname and TLS certificate are configured), so both are auto-allowed when
// the firewall is enabled.
var panelPorts = []string{"80", "443"}

// portPattern accepts a single port (1-65535) or an inclusive range "N:M".
var portPattern = regexp.MustCompile(`^[0-9]{1,5}(:[0-9]{1,5})?$`)

// commentPattern keeps a rule comment to plain, single-line printable text so it
// never smuggles control characters into a UFW rule.
var commentPattern = regexp.MustCompile(`^[\w .,:/@()+-]{0,120}$`)

// Rule is one firewall rule, either as parsed from `ufw status` for display or
// as a change requested by the caller.
type Rule struct {
	// Port is a single port ("80") or an inclusive range ("8000:8100").
	Port string `json:"port"`
	// Protocol is "tcp", "udp", or "" for both.
	Protocol string `json:"protocol"`
	// Action is "allow" or "deny" for a change; parsed status may also report
	// "reject" or "limit".
	Action string `json:"action"`
	// From is an optional source IP or CIDR; empty means anywhere.
	From string `json:"from"`
	// Comment is an optional label attached when the rule is created.
	Comment string `json:"comment,omitempty"`
	// V6 flags the IPv6 half of a rule as reported by `ufw status`; UFW lists a
	// port-only rule once per address family.
	V6 bool `json:"v6"`
}

// Status is the node's current firewall state.
type Status struct {
	// Installed reports whether the ufw binary is present at all.
	Installed bool `json:"installed"`
	// Active reports whether the firewall is enabled and enforcing.
	Active bool `json:"active"`
	// Rules is the incoming allow/deny table as UFW reports it.
	Rules []Rule `json:"rules"`
}

// Change is a single requested mutation. Action selects the verb; Rule carries
// the target for the port verbs and is ignored for enable/disable.
type Change struct {
	// Action is one of: enable, disable, allow, deny, delete.
	Action string `json:"action"`
	Rule   Rule   `json:"rule"`
}

const (
	ActionEnable  = "enable"
	ActionDisable = "disable"
	ActionAllow   = "allow"
	ActionDeny    = "deny"
	ActionDelete  = "delete"
)

// Operator is the node-side surface the control panel drives over the agent
// socket. HostOperator is the real implementation; UnixClient is the proxy.
type Operator interface {
	// Discover reports the current firewall status and rule table.
	Discover(context.Context) (Status, error)
	// Apply performs one change and returns the resulting status.
	Apply(context.Context, Change) (Status, error)
}

// validate checks a change is well-formed before any ufw command is built. The
// port verbs additionally require a valid rule target.
func (c Change) validate() error {
	switch c.Action {
	case ActionEnable, ActionDisable:
		return nil
	case ActionAllow, ActionDeny:
		return c.Rule.validate()
	case ActionDelete:
		if c.Rule.Action != ActionAllow && c.Rule.Action != ActionDeny {
			return errors.New("a rule to delete must name whether it is an allow or deny rule")
		}
		return c.Rule.validate()
	default:
		return fmt.Errorf("unknown firewall action %q", c.Action)
	}
}

func (r Rule) validate() error {
	if !portPattern.MatchString(r.Port) {
		return errors.New("port must be a number 1-65535 or a range like 8000:8100")
	}
	for _, part := range strings.Split(r.Port, ":") {
		n, err := strconv.Atoi(part)
		if err != nil || n < 1 || n > 65535 {
			return errors.New("port must be between 1 and 65535")
		}
	}
	switch r.Protocol {
	case "", "tcp", "udp":
	default:
		return errors.New("protocol must be tcp, udp, or empty for both")
	}
	if r.From != "" {
		if net.ParseIP(r.From) == nil {
			if _, _, err := net.ParseCIDR(r.From); err != nil {
				return errors.New("source must be a valid IP address or CIDR range")
			}
		}
	}
	if !commentPattern.MatchString(r.Comment) {
		return errors.New("comment contains unsupported characters")
	}
	// A source-scoped rule must name a protocol: UFW's extended syntax
	// (`from … to any port … proto …`) has no "both protocols" spelling.
	if r.From != "" && r.Protocol == "" {
		return errors.New("a rule limited to a source address must specify tcp or udp")
	}
	return nil
}

// portSpec renders "port" or "port/proto" for the simple UFW rule form.
func (r Rule) portSpec() string {
	if r.Protocol == "" {
		return r.Port
	}
	return r.Port + "/" + r.Protocol
}
