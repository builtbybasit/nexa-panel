package preflight

import (
	"context"
	"os/exec"
	"strings"
)

// UnitState is as much as the checks need to know about a systemd unit: whether
// the host has it at all, and whether it is running right now.
type UnitState struct {
	Present bool `json:"present"`
	Active  bool `json:"active"`
}

// unitState asks systemctl, ignoring its exit status: `is-active` exits
// non-zero for every state that is not "active", which is an answer rather than
// a failure.
func unitState(ctx context.Context, unit string) UnitState {
	path, err := exec.LookPath("systemctl")
	if err != nil {
		return UnitState{}
	}
	loaded, _ := exec.CommandContext(ctx, path, "show", "--property=LoadState", "--value", unit).Output()
	state := UnitState{Present: strings.TrimSpace(string(loaded)) == "loaded"}
	active, _ := exec.CommandContext(ctx, path, "is-active", unit).Output()
	state.Active = strings.TrimSpace(string(active)) == "active"
	return state
}
