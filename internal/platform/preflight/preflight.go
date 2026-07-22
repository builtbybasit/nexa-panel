// Package preflight answers the one question the installer has to settle before
// it touches anything: would installing here break something that already runs
// on this machine? Every check reports rather than acts, so the same set backs
// both `nexa doctor` and the installer's abort-before-you-mutate gate.
package preflight

import (
	"context"
	"sort"
	"sync"

	"github.com/nexa-panel/nexa-panel/internal/platform/secrets"
)

type Status string

const (
	StatusOK      Status = "ok"
	StatusWarning Status = "warning"
	StatusBlocker Status = "blocker"
)

// Result is one check's verdict. Remediation is the operator-facing sentence
// that says what to do about a warning or a blocker; it is empty when there is
// nothing to do.
type Result struct {
	Name        string `json:"name"`
	Title       string `json:"title"`
	Status      Status `json:"status"`
	Detail      string `json:"detail"`
	Remediation string `json:"remediation,omitempty"`
}

type Check interface {
	Name() string
	Run(ctx context.Context) Result
}

type Report struct {
	Version string   `json:"version"`
	Checks  []Result `json:"checks"`
}

func (r Report) HasBlocker() bool { return r.has(StatusBlocker) }

func (r Report) HasWarning() bool { return r.has(StatusWarning) }

func (r Report) has(status Status) bool {
	for _, result := range r.Checks {
		if result.Status == status {
			return true
		}
	}
	return false
}

// Blockers returns the results that must be resolved before installing.
func (r Report) Blockers() []Result {
	var blockers []Result
	for _, result := range r.Checks {
		if result.Status == StatusBlocker {
			blockers = append(blockers, result)
		}
	}
	return blockers
}

// Config is the machine layout the checks are run against. Zero fields fall
// back to the packaged node layout, so callers only state what differs.
type Config struct {
	Ports                []int
	StatePath            string
	MasterKeyPaths       []string
	ConfigDir            string
	SiteRoot             string
	AllowExistingInstall bool
	Endpoints            []Endpoint
}

// DefaultConfig mirrors the layout packaging/tmpfiles/nexa-panel.conf creates
// and the ports scripts/install.sh publishes through Nginx.
func DefaultConfig() Config {
	return Config{
		Ports:          []int{80, 443, 8888},
		StatePath:      "/var/lib/nexa-panel/control.db",
		MasterKeyPaths: []string{secrets.DefaultKeyPath, secrets.LegacyKeyPath},
		ConfigDir:      "/etc/nexa-panel",
		SiteRoot:       "/srv/nexa",
		Endpoints:      DefaultEndpoints(),
	}
}

// Checks wires the production probes — /proc, ss, systemctl, statfs, DNS — into
// the full check set, in the order `nexa doctor` renders them.
func Checks(cfg Config) []Check {
	defaults := DefaultConfig()
	if len(cfg.Ports) == 0 {
		cfg.Ports = defaults.Ports
	}
	if cfg.StatePath == "" {
		cfg.StatePath = defaults.StatePath
	}
	if len(cfg.MasterKeyPaths) == 0 {
		cfg.MasterKeyPaths = defaults.MasterKeyPaths
	}
	if cfg.ConfigDir == "" {
		cfg.ConfigDir = defaults.ConfigDir
	}
	if cfg.SiteRoot == "" {
		cfg.SiteRoot = defaults.SiteRoot
	}
	if len(cfg.Endpoints) == 0 {
		cfg.Endpoints = defaults.Endpoints
	}
	return []Check{
		NewPlatformCheck(),
		NewSystemdCheck(),
		NewMemoryCheck(),
		NewPodmanCheck(),
		NewPortsCheck(cfg.Ports),
		NewExistingInstallCheck(cfg.StatePath, cfg.MasterKeyPaths, cfg.AllowExistingInstall),
		NewCompetingPanelCheck(),
		NewDiskCheck(cfg.StatePath, cfg.ConfigDir, cfg.SiteRoot),
		NewNetworkCheck(cfg.Endpoints),
	}
}

// Run executes every check and returns their results in check order. The checks
// are independent read-only probes, so they run at the same time rather than
// carving one deadline up in sequence: run in order, a host with three slow
// filesystems spends the whole budget before the network check starts and the
// network is then reported as unreachable on a host whose network is fine. Each
// check now sees the full budget, and the report still ends within it.
func Run(ctx context.Context, checks []Check) []Result {
	results := make([]Result, len(checks))
	var running sync.WaitGroup
	for index, check := range checks {
		running.Add(1)
		go func() {
			defer running.Done()
			results[index] = check.Run(ctx)
		}()
	}
	running.Wait()
	return results
}

func ok(name, title, detail string) Result {
	return Result{Name: name, Title: title, Status: StatusOK, Detail: detail}
}

func warning(name, title, detail, remediation string) Result {
	return Result{Name: name, Title: title, Status: StatusWarning, Detail: detail, Remediation: remediation}
}

func blocker(name, title, detail, remediation string) Result {
	return Result{Name: name, Title: title, Status: StatusBlocker, Detail: detail, Remediation: remediation}
}

func sortedUnique(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	unique := make([]string, 0, len(values))
	for _, value := range values {
		if _, duplicate := seen[value]; duplicate {
			continue
		}
		seen[value] = struct{}{}
		unique = append(unique, value)
	}
	sort.Strings(unique)
	return unique
}
