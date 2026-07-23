package deploy

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/nexa-panel/nexa-panel/internal/modules/sites"
	"github.com/nexa-panel/nexa-panel/internal/platform/jobs"
	deployoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/deploy"
	firewalloperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/firewall"
)

// sshRemediation is the exact command an operator can paste. The prepare job
// never runs it: opening a port is a firewall change, and firewall changes go
// through the MFA-gated firewall.write path or nowhere.
const sshRemediation = "ufw allow 22/tcp"

// FirewallInspector reads the node's firewall state. It is the read-only half
// of the firewall operator, narrowed to the one call this module makes, so a
// prepare run can report that SSH is unreachable without ever gaining the
// ability to change a rule.
type FirewallInspector interface {
	Discover(ctx context.Context) (firewalloperator.Status, error)
}

// prepareJobPayload is the job request. It is persisted verbatim
// (jobs/repository.go:107) and carries only the site it is for and the branch
// whose CLI the node needs — no credential, and no package name: which packages
// a prepare may install is fixed in the operator.
// The identity fields are the site's own, re-derived by the operator from the
// slug before either the wrapper or the sudoers rule is written, so a tampered
// payload cannot widen the grant — it can only fail validation.
type prepareJobPayload struct {
	SiteID     string `json:"siteId"`
	PHPVersion string `json:"phpVersion"`
	Slug       string `json:"slug"`
	UnixUser   string `json:"unixUser"`
	RootPath   string `json:"rootPath"`
	// DeployerMode is what decides whether this run grants the site its narrow
	// PHP-FPM reload permission. A standard-mode site never gets one.
	DeployerMode bool `json:"deployerMode"`
}

// FirewallSSH is the verdict of the port-22 check. Checked is false when the
// panel has no firewall operator wired at all, which is a fact about this
// build rather than about the node and must not read as "port 22 is closed".
type FirewallSSH struct {
	Checked     bool   `json:"checked"`
	Installed   bool   `json:"installed"`
	Active      bool   `json:"active"`
	Open        bool   `json:"open"`
	Remediation string `json:"remediation,omitempty"`
}

// NodePreparation is the job result: what the node has, what it now has, and
// whether an SSH client can still reach it. Ready is the single answer the page
// leads with — every prerequisite present — and it deliberately excludes the
// firewall, which is a warning surface, not a gate this job can clear.
type NodePreparation struct {
	SiteID      string                      `json:"siteId"`
	Ready       bool                        `json:"ready"`
	Tools       []deployoperator.ToolStatus `json:"tools"`
	FirewallSSH FirewallSSH                 `json:"firewallSsh"`
	Warnings    []string                    `json:"warnings"`
}

// prepare queues the node preparation. It is a job rather than a synchronous
// call because it may run apt for tens of minutes and the page streams its
// progress; the payload carries no secret, which is what makes a persisted
// request acceptable here.
func (m *Module) prepare(ctx context.Context, site sites.Site, actor *string, remoteAddress string) (jobs.Job, error) {
	// The audit entry is written before the job exists and fail-closed: a
	// prepare installs packages on the node, and an install nobody can attribute
	// afterwards is not one this panel makes.
	metadata := map[string]any{"phpVersion": site.PHPVersion, "slug": site.Slug}
	if err := m.record(ctx, "deploy.node_prepared", site, actor, remoteAddress, metadata); err != nil {
		return jobs.Job{}, err
	}
	payload := prepareJobPayload{
		SiteID: site.ID, PHPVersion: site.PHPVersion,
		Slug: site.Slug, UnixUser: site.UnixUser, RootPath: site.RootPath,
		DeployerMode: site.DeploymentMode == sites.DeploymentModeDeployer,
	}
	// The site scope is what lets a developer-role caller watch the job they
	// just started (jobs/access.go:64-87).
	return m.jobs.SubmitTitledWithOptions(ctx, "deploy.prepare", "Prepare this node for deployments", payload,
		jobs.SubmitOptions{ActorUserID: actor, SiteIDs: []string{site.ID}})
}

// prepareJob drives the node and then reads the firewall. A tool that could not
// be installed is a result, not a failure — the whole point of the run is to
// report what is missing — so only a node that could not be asked at all fails
// the job.
func (m *Module) prepareJob(ctx context.Context, raw json.RawMessage, report func(int, string) error) (any, error) {
	var payload prepareJobPayload
	if err := json.Unmarshal(raw, &payload); err != nil || payload.SiteID == "" || payload.PHPVersion == "" {
		return nil, errors.New("invalid node preparation request")
	}
	_ = report(10, "Checking the node's deployment tooling.")
	observation, err := m.operator.Prepare(ctx, deployoperator.PrepareRequest{PHPVersion: payload.PHPVersion})
	if err != nil {
		return nil, err
	}
	_ = report(80, "Checking that SSH can still reach this node.")
	result := NodePreparation{
		SiteID: payload.SiteID, Ready: true,
		Tools: observation.Tools, Warnings: observation.Warnings,
	}
	if result.Warnings == nil {
		result.Warnings = []string{}
	}
	for _, status := range result.Tools {
		if !status.Installed {
			result.Ready = false
		}
	}
	// A deployer publishes a new release and then has to make PHP-FPM pick it
	// up. Granting that here — and only for a deployer-mode site — is what keeps
	// the permission narrow: one fixed wrapper reloading one branch's unit,
	// never a general sudo grant. A grant that fails is a warning, like a tool
	// that could not be installed, because the rest of the run is still useful.
	if grant, ok, err := m.reloadGrantFor(ctx, payload); err != nil {
		result.Ready = false
		result.Warnings = append(result.Warnings,
			"This site could not be granted permission to reload its PHP-FPM pool: "+err.Error())
	} else if ok {
		_ = report(88, "Granting this site permission to reload its PHP-FPM pool.")
		if err := m.applyReloadGrant(ctx, grant, true); err != nil {
			result.Ready = false
			result.Warnings = append(result.Warnings,
				"This site could not be granted permission to reload its PHP-FPM pool: "+err.Error())
		}
	}
	firewall, warnings := m.checkSSHPort(ctx)
	result.FirewallSSH = firewall
	result.Warnings = append(result.Warnings, warnings...)
	if result.Ready {
		_ = report(95, "The node has everything a deployment needs.")
		return result, nil
	}
	_ = report(95, "The node is missing some deployment tooling.")
	return result, nil
}

// reloadGrantFor decides whether this run may install the site's PHP-FPM reload
// grant, and names the site it would be installed for. The decision is made
// from the site as it reads *now*, never from the mode captured in the durable
// payload at submit time: a prepare run spends minutes in apt, and nothing
// serialises it against a mode switch. switchMode revokes the grant before it
// moves the column precisely so a standard-mode site can never hold one, and a
// stale payload re-granting afterwards would defeat that ordering — leaving a
// standard-mode site's unix user with a sudoers rule that teardown, which
// early-returns for a non-deployer site, would never remove.
//
// It fails closed: a site that could not be re-read mid-run is not granted, and
// the caller reports that as a warning like any other grant failure.
func (m *Module) reloadGrantFor(ctx context.Context, payload prepareJobPayload) (prepareJobPayload, bool, error) {
	if !payload.DeployerMode || payload.Slug == "" {
		return prepareJobPayload{}, false, nil
	}
	site, err := m.sites.Get(ctx, payload.SiteID)
	if err != nil {
		return prepareJobPayload{}, false, err
	}
	if site.DeploymentMode != sites.DeploymentModeDeployer {
		return prepareJobPayload{}, false, nil
	}
	return reloadPayload(site), true, nil
}

// applyReloadGrant settles the site's PHP-FPM reload permission on the node.
// It is idempotent in both directions, which is what lets the prepare job call
// it on every run and the mode switch call it on every departure from deployer
// mode without either having to know the current state.
func (m *Module) applyReloadGrant(ctx context.Context, payload prepareJobPayload, enabled bool) error {
	_, err := m.operator.ApplyFPMReload(ctx, deployoperator.FPMReloadRequest{
		Slug: payload.Slug, UnixUser: payload.UnixUser, RootPath: payload.RootPath,
		PHPVersion: payload.PHPVersion, Enabled: enabled,
	})
	return err
}

// checkSSHPort reports whether a deployer can still open an SSH session to this
// node. It is strictly read-only: it never opens a port, because opening one is
// a firewall change and those belong behind firewall.write, which is
// MFA-sensitive. The worst this can do is tell an operator to run one command.
func (m *Module) checkSSHPort(ctx context.Context) (FirewallSSH, []string) {
	if m.firewall == nil {
		return FirewallSSH{}, []string{"The panel could not check whether port 22 is open on this node."}
	}
	status, err := m.firewall.Discover(ctx)
	if err != nil {
		return FirewallSSH{}, []string{"The node's firewall state could not be read: " + err.Error()}
	}
	verdict := FirewallSSH{Checked: true, Installed: status.Installed, Active: status.Active}
	switch {
	case !status.Installed:
		// No firewall at all: nothing is blocking SSH, but nothing is blocking
		// anything else either, and that is worth saying out loud.
		verdict.Open = true
		return verdict, []string{"This node has no firewall installed, so port 22 is reachable and so is every other port."}
	case !status.Active:
		// An inactive firewall is reported as a warning rather than as a pass,
		// for the same reason.
		verdict.Open = true
		return verdict, []string{"The node's firewall is inactive, so port 22 is reachable and so is every other port."}
	case sshRuleAllows(status.Rules):
		verdict.Open = true
		return verdict, nil
	default:
		verdict.Remediation = sshRemediation
		return verdict, []string{"The firewall does not allow port 22, so a deployment over SSH cannot reach this node. Allow it from the Firewall page, or run `" + sshRemediation + "` on the node."}
	}
}

// sshRuleAllows looks for an allow rule covering port 22. UFW lists a port-only
// rule once per address family and reports the protocol as "tcp" or, for a rule
// added without one, as empty; both are the same rule as far as an SSH client
// is concerned, so either row is enough and the IPv6 duplicate is simply a
// second match.
//
// A source-scoped rule is not treated as open: a rule that only admits one
// network is not the same as a reachable node, and reporting it as open would
// send an operator looking for the fault everywhere but the firewall.
func sshRuleAllows(rules []firewalloperator.Rule) bool {
	for _, rule := range rules {
		if rule.Action != firewalloperator.ActionAllow || rule.Port != "22" || rule.From != "" {
			continue
		}
		if rule.Protocol == "" || rule.Protocol == "tcp" {
			return true
		}
	}
	return false
}
