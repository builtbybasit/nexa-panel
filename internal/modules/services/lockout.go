package services

import (
	"fmt"
	"strings"
)

// revertibleCriticalUnits are the units whose stop takes the operator's access
// with it but which the panel can still start again afterwards: Nginx serves the
// panel, and sshd is the way back in when the panel is down. Stopping one is
// allowed only behind an acknowledged risk and an armed automatic revert.
var revertibleCriticalUnits = map[string]string{
	"nginx.service": "the panel's HTTP/HTTPS ingress",
	"ssh.service":   "SSH logins to this server",
	"sshd.service":  "SSH logins to this server",
}

// irreversibleCriticalUnits are the units that carry the revert mechanism
// itself. Stopping the API kills the timer that would undo it; stopping the
// agent removes the only privileged path back to systemd. Arming a revert for
// either would be a promise the panel cannot keep, so the stop is refused
// outright and has to be done from a root console.
var irreversibleCriticalUnits = map[string]string{
	"nexa-api.service":   "the panel API, which runs the automatic rollback timer",
	"nexa-agent.service": "the privileged agent, the only path back to systemd",
}

// assessLockout decides whether one service change can cut the operator off. The
// judgement is the control plane's, not the browser's: a direct API call must
// meet the same guard as the Services page.
//
// The second return value marks a refusal no acknowledgement can lift.
func assessLockout(unit, action string) (reasons []string, irreversible bool) {
	if action != "stop" {
		return nil, false
	}
	unit = normaliseUnit(unit)
	if what, listed := irreversibleCriticalUnits[unit]; listed {
		return []string{fmt.Sprintf("Stopping %s stops %s, so the panel could not start it again.", unit, what)}, true
	}
	if what, listed := revertibleCriticalUnits[unit]; listed {
		return []string{fmt.Sprintf("Stopping %s stops %s.", unit, what)}, false
	}
	return nil, false
}

// normaliseUnit gives a bare name its implicit ".service" suffix so "nginx" and
// "nginx.service" cannot take different paths through the guard.
func normaliseUnit(unit string) string {
	unit = strings.TrimSpace(strings.ToLower(unit))
	if unit != "" && !strings.Contains(unit, ".") {
		unit += ".service"
	}
	return unit
}
