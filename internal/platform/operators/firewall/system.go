package firewall

import "strings"

// actionWords maps UFW's uppercase status verbs to our lowercase rule actions.
var actionWords = map[string]string{
	"ALLOW":  ActionAllow,
	"DENY":   ActionDeny,
	"REJECT": "reject",
	"LIMIT":  "limit",
}

// parseStatus turns `ufw status verbose` output into a Status. Only the active
// flag and the incoming (IN) rule rows are extracted; the "To"/"Action"/"From"
// table is column-aligned but the action verb is a reliable anchor, so each row
// is split around it rather than by fixed columns.
func parseStatus(output string) Status {
	status := Status{}
	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "Status:") {
			status.Active = strings.TrimSpace(strings.TrimPrefix(trimmed, "Status:")) == "active"
			continue
		}
		if rule, ok := parseRuleLine(trimmed); ok {
			status.Rules = append(status.Rules, rule)
		}
	}
	return status
}

func parseRuleLine(line string) (Rule, bool) {
	fields := strings.Fields(line)
	actionIdx := -1
	var action string
	for i, field := range fields {
		if mapped, ok := actionWords[field]; ok {
			actionIdx, action = i, mapped
			break
		}
	}
	// A real rule row has a "To" spec before the verb and a direction word after
	// it. Header and default-policy lines lack that shape.
	if actionIdx <= 0 || actionIdx+1 >= len(fields) {
		return Rule{}, false
	}
	// Only surface incoming rules; the panel manages inbound exposure.
	if strings.ToUpper(fields[actionIdx+1]) != "IN" {
		return Rule{}, false
	}

	to := strings.Join(fields[:actionIdx], " ")
	from := strings.Join(fields[actionIdx+2:], " ")

	rule := Rule{Action: action}
	rule.V6 = strings.Contains(to, "(v6)") || strings.Contains(from, "(v6)")
	to = strings.TrimSpace(strings.ReplaceAll(to, "(v6)", ""))

	// "To" is either a port, a port/proto, a range, or a bare "Anywhere".
	if portproto := to; !strings.EqualFold(portproto, "Anywhere") {
		if slash := strings.LastIndex(portproto, "/"); slash != -1 {
			rule.Port = portproto[:slash]
			rule.Protocol = strings.ToLower(portproto[slash+1:])
		} else {
			rule.Port = portproto
		}
	}

	from = strings.TrimSpace(strings.ReplaceAll(from, "(v6)", ""))
	if !strings.EqualFold(from, "Anywhere") {
		rule.From = from
	}
	return rule, true
}
