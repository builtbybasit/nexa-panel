package preflight

import (
	"context"
	"fmt"
	"strings"
)

// ExistingInstallCheck refuses to let an install run over a live one. Reusing
// this node's state directory under a second install would have two API
// processes writing one control.db, and a fresh master key orphans every secret
// already stored — so a running panel is a blocker, while a stopped one is the
// ordinary in-place upgrade the installer is built for.
type ExistingInstallCheck struct {
	StatePath      string
	MasterKeyPaths []string
	Units          []string
	AllowExisting  bool
	Exists         func(path string) bool
	Unit           func(ctx context.Context, unit string) UnitState
}

func NewExistingInstallCheck(statePath string, masterKeyPaths []string, allowExisting bool) ExistingInstallCheck {
	return ExistingInstallCheck{
		StatePath:      statePath,
		MasterKeyPaths: masterKeyPaths,
		Units:          []string{"nexa-api.service", "nexa-agent.service"},
		AllowExisting:  allowExisting,
		Exists:         pathExists,
		Unit:           unitState,
	}
}

func (c ExistingInstallCheck) Name() string { return "existing-install" }

func (c ExistingInstallCheck) Run(ctx context.Context) Result {
	const title = "Existing install"
	var found, running []string
	if c.Exists(c.StatePath) {
		found = append(found, "control database "+c.StatePath)
	}
	for _, path := range c.MasterKeyPaths {
		if c.Exists(path) {
			found = append(found, "master key "+path)
		}
	}
	for _, unit := range c.Units {
		state := c.Unit(ctx, unit)
		if state.Active {
			running = append(running, unit)
			continue
		}
		if state.Present {
			found = append(found, unit)
		}
	}

	if len(running) > 0 {
		detail := fmt.Sprintf("Nexa Panel is already running here (%s)", strings.Join(running, ", "))
		if c.AllowExisting {
			return warning(c.Name(), title, detail+"; upgrading in place as requested", "The services restart at the end of the install; back up control.db and the master key first.")
		}
		return blocker(
			c.Name(), title, detail,
			"Installing over a live panel replaces its units and configuration. Back up control.db and the master key, then re-run with --allow-existing to upgrade this node in place.",
		)
	}
	if len(found) > 0 {
		return warning(
			c.Name(), title,
			fmt.Sprintf("a stopped install is already present: %s", strings.Join(found, ", ")),
			"This run upgrades it in place and keeps its state; back up control.db and the master key first.",
		)
	}
	return ok(c.Name(), title, "none found")
}

// CompetingPanelCheck looks for the other control panels. They own the same
// pieces of the machine Nexa Panel does — the web server, the PHP-FPM pools,
// the system accounts — so two of them on one host fight rather than coexist.
type CompetingPanelCheck struct {
	Panels []CompetingPanel
	Exists func(path string) bool
	Unit   func(ctx context.Context, unit string) UnitState
}

type CompetingPanel struct {
	Name  string
	Paths []string
	Unit  string
	// Coexists marks software that only conflicts while it is running, such as
	// an Apache that would hold the ports the panel's Nginx needs.
	Coexists bool
}

func NewCompetingPanelCheck() CompetingPanelCheck {
	return CompetingPanelCheck{
		Panels: []CompetingPanel{
			{Name: "cPanel", Paths: []string{"/usr/local/cpanel"}, Unit: "cpanel.service"},
			{Name: "Plesk", Paths: []string{"/usr/local/psa", "/opt/psa"}, Unit: "psa.service"},
			{Name: "CyberPanel", Paths: []string{"/usr/local/CyberCP"}, Unit: "lscpd.service"},
			{Name: "Webmin", Paths: []string{"/etc/webmin"}, Unit: "webmin.service"},
			{Name: "Apache", Paths: []string{"/etc/apache2"}, Unit: "apache2.service", Coexists: true},
		},
		Exists: pathExists,
		Unit:   unitState,
	}
}

func (c CompetingPanelCheck) Name() string { return "competing-panel" }

func (c CompetingPanelCheck) Run(ctx context.Context) Result {
	const title = "Competing panels"
	var conflicts, present []string
	for _, panel := range c.Panels {
		installed := false
		for _, path := range panel.Paths {
			if c.Exists(path) {
				installed = true
				break
			}
		}
		state := c.Unit(ctx, panel.Unit)
		switch {
		case state.Active:
			conflicts = append(conflicts, fmt.Sprintf("%s (%s is running)", panel.Name, panel.Unit))
		case installed && panel.Coexists:
			present = append(present, fmt.Sprintf("%s (installed, not running)", panel.Name))
		case installed:
			conflicts = append(conflicts, fmt.Sprintf("%s (installed)", panel.Name))
		}
	}

	if len(conflicts) > 0 {
		return blocker(
			c.Name(), title,
			strings.Join(sortedUnique(conflicts), ", "),
			"Remove the other control panel, or install Nexa Panel on a clean host. Two panels managing one machine's web server and system accounts corrupt each other's configuration.",
		)
	}
	if len(present) > 0 {
		return warning(
			c.Name(), title,
			strings.Join(sortedUnique(present), ", "),
			"Keep it stopped and disabled: it binds the ports the panel's Nginx serves from.",
		)
	}
	return ok(c.Name(), title, "none found")
}
