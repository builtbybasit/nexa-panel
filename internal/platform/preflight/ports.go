package preflight

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// Listener is a socket already accepting connections on this host.
type Listener struct {
	Port    int    `json:"port"`
	Process string `json:"process,omitempty"`
	PID     int    `json:"pid,omitempty"`
}

func (l Listener) describe() string {
	switch {
	case l.Process != "" && l.PID != 0:
		return fmt.Sprintf("%d (%s, pid %d)", l.Port, l.Process, l.PID)
	case l.Process != "":
		return fmt.Sprintf("%d (%s)", l.Port, l.Process)
	default:
		return fmt.Sprintf("%d (unknown process)", l.Port)
	}
}

// PortsCheck looks for anything already bound to the ports the installer
// publishes the panel on. Managed names are the processes the installer owns
// anyway — a re-run finds its own Nginx on :80 and must not treat that as a
// conflict, or the installer stops being re-runnable.
type PortsCheck struct {
	Ports     []int
	Managed   []string
	Listeners func(ctx context.Context) ([]Listener, error)
}

func NewPortsCheck(ports []int) PortsCheck {
	return PortsCheck{Ports: ports, Managed: []string{"nginx"}, Listeners: listeningSockets}
}

func (c PortsCheck) Name() string { return "ports" }

func (c PortsCheck) Run(ctx context.Context) Result {
	const title = "Ports"
	wanted := make(map[int]struct{}, len(c.Ports))
	labels := make([]string, 0, len(c.Ports))
	for _, port := range c.Ports {
		wanted[port] = struct{}{}
		labels = append(labels, strconv.Itoa(port))
	}

	listeners, err := c.Listeners(ctx)
	if err != nil {
		return warning(c.Name(), title, fmt.Sprintf("cannot enumerate listening sockets (%v)", err), fmt.Sprintf("Confirm by hand that %s are free.", strings.Join(labels, ", ")))
	}

	managed := make(map[string]struct{}, len(c.Managed))
	for _, name := range c.Managed {
		managed[name] = struct{}{}
	}
	var conflicts, ours []string
	for _, listener := range listeners {
		if _, listed := wanted[listener.Port]; !listed {
			continue
		}
		if _, mine := managed[listener.Process]; mine {
			ours = append(ours, listener.describe())
			continue
		}
		conflicts = append(conflicts, listener.describe())
	}

	if len(conflicts) > 0 {
		return blocker(
			c.Name(), title,
			fmt.Sprintf("already listened on: %s", strings.Join(sortedUnique(conflicts), ", ")),
			"Stop or move the process holding each port; the panel's Nginx cannot bind a port another server owns.",
		)
	}
	if len(ours) > 0 {
		return warning(
			c.Name(), title,
			fmt.Sprintf("held by software this installer manages: %s", strings.Join(sortedUnique(ours), ", ")),
			"Installing rewrites the panel's Nginx site; any other site served from this Nginx keeps working.",
		)
	}
	return ok(c.Name(), title, fmt.Sprintf("%s are free", strings.Join(labels, ", ")))
}

// listeningSockets shells out to ss rather than reading /proc/net/tcp: ss
// already joins the socket table to the owning process, which is the part an
// operator needs to act on the conflict.
func listeningSockets(ctx context.Context) ([]Listener, error) {
	path, err := exec.LookPath("ss")
	if err != nil {
		return nil, fmt.Errorf("locate ss: %w", err)
	}
	output, err := exec.CommandContext(ctx, path, "-H", "-l", "-n", "-t", "-p").Output()
	if err != nil {
		return nil, fmt.Errorf("list listening sockets: %w", err)
	}
	return parseListeners(string(output)), nil
}

// parseListeners reads `ss -Hlntp` rows, whose fourth column is the local
// address and whose optional trailing column names the owner, e.g.
// `users:(("nginx",pid=812,fd=6))`.
func parseListeners(output string) []Listener {
	var listeners []Listener
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		local := fields[3]
		index := strings.LastIndex(local, ":")
		if index < 0 {
			continue
		}
		port, err := strconv.Atoi(local[index+1:])
		if err != nil {
			continue
		}
		listener := Listener{Port: port}
		if users := strings.Index(line, `users:(("`); users >= 0 {
			rest := line[users+len(`users:(("`):]
			if name, tail, found := strings.Cut(rest, `"`); found {
				listener.Process = name
				if _, after, found := strings.Cut(tail, "pid="); found {
					pid, _, _ := strings.Cut(after, ",")
					listener.PID, _ = strconv.Atoi(pid)
				}
			}
		}
		listeners = append(listeners, listener)
	}
	return listeners
}
