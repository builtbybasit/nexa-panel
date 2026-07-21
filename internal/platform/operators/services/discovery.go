package services

import (
	"context"
	"sort"
	"strings"
)

// Discover lists the managed service units present on the node. It enumerates
// installed unit files once, keeps only the allow-listed, non-protected ones,
// then fills each unit's active and boot-enablement state.
func (o *HostOperator) Discover(ctx context.Context) ([]Service, error) {
	output, err := o.runner.Run(ctx, Command{
		Name: "systemctl",
		Args: []string{"list-unit-files", "--type=service", "--no-legend", "--plain", "--no-pager"},
	})
	if err != nil {
		return nil, commandError("list systemd service units", output, err)
	}
	units := []string{}
	seen := map[string]struct{}{}
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) == 0 {
			continue
		}
		unit := fields[0]
		if !managed(unit) {
			continue
		}
		if _, dup := seen[unit]; dup {
			continue
		}
		seen[unit] = struct{}{}
		units = append(units, unit)
	}
	sort.Strings(units)

	services := make([]Service, 0, len(units))
	for _, unit := range units {
		services = append(services, o.observe(ctx, unit))
	}
	return services, nil
}

// observe reads one unit's current active and enabled state. systemctl exits
// non-zero for an inactive or disabled unit, which is expected — the trimmed
// stdout word ("active"/"inactive"/"failed", "enabled"/"disabled"/"static") is
// what matters, not the exit code.
func (o *HostOperator) observe(ctx context.Context, unit string) Service {
	activeOut, _ := o.runner.Run(ctx, Command{Name: "systemctl", Args: []string{"is-active", unit}})
	status := strings.TrimSpace(string(activeOut))
	if status == "" {
		status = "unknown"
	}
	enabledOut, _ := o.runner.Run(ctx, Command{Name: "systemctl", Args: []string{"is-enabled", unit}})
	enabled := strings.TrimSpace(string(enabledOut))
	return Service{
		Name:        displayName(unit),
		SystemdUnit: unit,
		Status:      status,
		Active:      status == "active",
		Enabled:     enabled == "enabled" || enabled == "enabled-runtime" || enabled == "alias",
	}
}

// find returns the discovered service for a unit, or false if it is not a
// managed, present unit. This is the boundary that keeps an arbitrary caller
// string out of a systemctl command line.
func (o *HostOperator) find(ctx context.Context, unit string) (Service, bool, error) {
	unit = strings.TrimSpace(unit)
	if !managed(unit) {
		return Service{}, false, nil
	}
	services, err := o.Discover(ctx)
	if err != nil {
		return Service{}, false, err
	}
	for _, service := range services {
		if service.SystemdUnit == unit {
			return service, true, nil
		}
	}
	return Service{}, false, nil
}
