package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/nexa-panel/nexa-panel/internal/platform/audit"
	deployoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/deploy"
	firewalloperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/firewall"
)

// fakeFirewall answers the port-22 check. It also carries an Apply the module
// is never given a way to reach, so a test can assert that a prepare run leaves
// the firewall exactly as it found it.
type fakeFirewall struct {
	status    firewalloperator.Status
	err       error
	discovers int
	applied   []firewalloperator.Change
}

func (f *fakeFirewall) Discover(context.Context) (firewalloperator.Status, error) {
	f.discovers++
	return f.status, f.err
}

func (f *fakeFirewall) Apply(_ context.Context, change firewalloperator.Change) (firewalloperator.Status, error) {
	f.applied = append(f.applied, change)
	return f.status, nil
}

// sshAllowed is what `ufw status verbose` parses to for a node with SSH open:
// UFW lists a port-only rule once per address family, so the IPv6 duplicate is
// part of the normal shape and not a second rule.
var sshAllowed = firewalloperator.Status{
	Installed: true, Active: true,
	Rules: []firewalloperator.Rule{
		{Port: "22", Protocol: "tcp", Action: "allow"},
		{Port: "80", Protocol: "tcp", Action: "allow"},
		{Port: "22", Protocol: "tcp", Action: "allow", V6: true},
		{Port: "80", Protocol: "tcp", Action: "allow", V6: true},
	},
}

// preparedNode is what a node with all six prerequisites reports back.
var preparedNode = deployoperator.PrepareObservation{
	Tools: []deployoperator.ToolStatus{
		{Name: "git", Path: "/usr/bin/git", Version: "git version 2.43.0", Installed: true, Action: deployoperator.ToolPresent},
		{Name: "rsync", Path: "/usr/bin/rsync", Installed: true, Action: deployoperator.ToolInstalled},
	},
}

func runPrepareJob(t *testing.T, module *Module) NodePreparation {
	t.Helper()
	payload, err := json.Marshal(prepareJobPayload{SiteID: testSite.ID, PHPVersion: "8.3"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := module.prepareJob(context.Background(), payload, func(int, string) error { return nil })
	if err != nil {
		t.Fatalf("prepareJob returned an error: %v", err)
	}
	preparation, ok := result.(NodePreparation)
	if !ok {
		t.Fatalf("result = %T, want NodePreparation", result)
	}
	return preparation
}

// The branch the job prepares is the site's own, never a caller's, and the run
// is recorded before it starts: it installs packages on the node.
func TestPrepareSubmitsAJobAndAuditsIt(t *testing.T) {
	module, _, _, auditLog, _ := newDeployModule(t)
	actor := "user_admin"
	job, err := module.prepare(context.Background(), testSite, &actor, "203.0.113.9")
	if err != nil {
		t.Fatalf("prepare returned an error: %v", err)
	}
	if job.Kind != "deploy.prepare" {
		t.Fatalf("job = %+v", job)
	}
	var payload prepareJobPayload
	if err := json.Unmarshal(job.Request, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.SiteID != testSite.ID || payload.PHPVersion != "8.3" {
		t.Fatalf("payload = %+v", payload)
	}
	recorded := findEvent(t, auditLog, "deploy.node_prepared")
	if recorded.ActorUserID == nil || *recorded.ActorUserID != actor || recorded.Subject != "site:site_blog" {
		t.Fatalf("entry = %+v", recorded)
	}
	if recorded.Metadata["phpVersion"] != "8.3" {
		t.Fatalf("metadata = %+v", recorded.Metadata)
	}
}

// Dropping the table stands in for an audit log that cannot be written: the
// packages must not be installed, audited or not.
func TestPrepareRefusesAnUnauditableRun(t *testing.T) {
	module, operator, _, _, database := newDeployModule(t)
	if _, err := database.ExecContext(context.Background(), "DROP TABLE audit_events"); err != nil {
		t.Fatal(err)
	}
	actor := "user_admin"
	if _, err := module.prepare(context.Background(), testSite, &actor, ""); !errors.Is(err, audit.ErrUnauditable) {
		t.Fatalf("prepare err = %v, want audit.ErrUnauditable", err)
	}
	if len(operator.prepared) != 0 {
		t.Fatalf("an unauditable prepare reached the node anyway: %+v", operator.prepared)
	}
}

// A node with every prerequisite and an open port 22 is ready, and says so
// without a single warning.
func TestPrepareJobReportsAReadyNode(t *testing.T) {
	module, operator, _, _, _ := newDeployModule(t)
	operator.preparation = preparedNode
	firewall := &fakeFirewall{status: sshAllowed}
	module.UseFirewallState(firewall)
	preparation := runPrepareJob(t, module)
	if !preparation.Ready || preparation.SiteID != testSite.ID {
		t.Fatalf("preparation = %+v", preparation)
	}
	if len(preparation.Tools) != 2 || len(preparation.Warnings) != 0 {
		t.Fatalf("preparation = %+v", preparation)
	}
	if !preparation.FirewallSSH.Checked || !preparation.FirewallSSH.Open || preparation.FirewallSSH.Remediation != "" {
		t.Fatalf("firewall = %+v", preparation.FirewallSSH)
	}
	if len(operator.prepared) != 1 || operator.prepared[0].PHPVersion != "8.3" {
		t.Fatalf("node calls = %+v", operator.prepared)
	}
}

// A tool the node could not install is a result with a warning, not a failed
// job: the run exists to say what is missing.
func TestPrepareJobReportsMissingToolingWithoutFailing(t *testing.T) {
	module, operator, _, _, _ := newDeployModule(t)
	operator.preparation = deployoperator.PrepareObservation{
		Tools: []deployoperator.ToolStatus{
			{Name: "git", Installed: true, Action: deployoperator.ToolPresent},
			{Name: "composer", Action: deployoperator.ToolFailed},
		},
		Warnings: []string{"composer could not be installed: exit status 100"},
	}
	module.UseFirewallState(&fakeFirewall{status: sshAllowed})
	preparation := runPrepareJob(t, module)
	if preparation.Ready {
		t.Fatalf("a node missing composer reported ready: %+v", preparation)
	}
	if len(preparation.Warnings) != 1 || !strings.Contains(preparation.Warnings[0], "composer") {
		t.Fatalf("warnings = %v", preparation.Warnings)
	}
}

// The check is read-only. A closed port 22 is reported with the exact command
// that would open it, and the firewall is not touched — opening a port stays
// behind the MFA-gated firewall.write path.
func TestPrepareJobWarnsAboutAClosedSSHPortWithoutOpeningIt(t *testing.T) {
	module, operator, _, _, _ := newDeployModule(t)
	operator.preparation = preparedNode
	firewall := &fakeFirewall{status: firewalloperator.Status{
		Installed: true, Active: true,
		Rules: []firewalloperator.Rule{
			{Port: "80", Protocol: "tcp", Action: "allow"},
			{Port: "443", Protocol: "tcp", Action: "allow", V6: true},
		},
	}}
	module.UseFirewallState(firewall)
	preparation := runPrepareJob(t, module)
	if preparation.FirewallSSH.Open || preparation.FirewallSSH.Remediation != "ufw allow 22/tcp" {
		t.Fatalf("firewall = %+v", preparation.FirewallSSH)
	}
	if len(preparation.Warnings) != 1 || !strings.Contains(preparation.Warnings[0], "ufw allow 22/tcp") {
		t.Fatalf("warnings = %v", preparation.Warnings)
	}
	if len(firewall.applied) != 0 {
		t.Fatalf("the prepare job changed the firewall: %+v", firewall.applied)
	}
	// A node missing nothing is still ready; the firewall is a warning surface.
	if !preparation.Ready {
		t.Fatalf("preparation = %+v", preparation)
	}
}

// An inactive firewall means nothing is blocking SSH, but it is reported as a
// warning rather than as a pass — nothing is blocking anything else either.
func TestPrepareJobWarnsWhenTheFirewallIsInactive(t *testing.T) {
	module, operator, _, _, _ := newDeployModule(t)
	operator.preparation = preparedNode
	module.UseFirewallState(&fakeFirewall{status: firewalloperator.Status{Installed: true, Rules: nil}})
	preparation := runPrepareJob(t, module)
	if !preparation.FirewallSSH.Open || preparation.FirewallSSH.Active {
		t.Fatalf("firewall = %+v", preparation.FirewallSSH)
	}
	if len(preparation.Warnings) != 1 || !strings.Contains(preparation.Warnings[0], "inactive") {
		t.Fatalf("warnings = %v", preparation.Warnings)
	}
}

// Without a firewall operator the panel does not know, and "not checked" must
// not read as "closed".
func TestPrepareJobReportsAnUncheckedFirewall(t *testing.T) {
	module, operator, _, _, _ := newDeployModule(t)
	operator.preparation = preparedNode
	preparation := runPrepareJob(t, module)
	if preparation.FirewallSSH.Checked || preparation.FirewallSSH.Open {
		t.Fatalf("firewall = %+v", preparation.FirewallSSH)
	}
	if len(preparation.Warnings) != 1 {
		t.Fatalf("warnings = %v", preparation.Warnings)
	}
}

// The rule table is read exactly as UFW reports it: a rule added without a
// protocol counts, a rule scoped to one source does not, and the IPv6 duplicate
// of an allow row is the same rule rather than a second one.
func TestSSHRuleAllows(t *testing.T) {
	cases := []struct {
		name  string
		rules []firewalloperator.Rule
		want  bool
	}{
		{"tcp", []firewalloperator.Rule{{Port: "22", Protocol: "tcp", Action: "allow"}}, true},
		{"both protocols", []firewalloperator.Rule{{Port: "22", Action: "allow"}}, true},
		{"v6 row only", []firewalloperator.Rule{{Port: "22", Protocol: "tcp", Action: "allow", V6: true}}, true},
		{"udp only", []firewalloperator.Rule{{Port: "22", Protocol: "udp", Action: "allow"}}, false},
		{"denied", []firewalloperator.Rule{{Port: "22", Protocol: "tcp", Action: "deny"}}, false},
		{"limited", []firewalloperator.Rule{{Port: "22", Protocol: "tcp", Action: "limit"}}, false},
		{"source scoped", []firewalloperator.Rule{{Port: "22", Protocol: "tcp", Action: "allow", From: "10.0.0.0/8"}}, false},
		{"app profile row", []firewalloperator.Rule{{Port: "", Action: "allow"}}, false},
		{"other ports", []firewalloperator.Rule{{Port: "80", Protocol: "tcp", Action: "allow"}}, false},
		{"none", nil, false},
	}
	for _, testCase := range cases {
		if got := sshRuleAllows(testCase.rules); got != testCase.want {
			t.Fatalf("%s: sshRuleAllows() = %v, want %v", testCase.name, got, testCase.want)
		}
	}
}

// A node that could not be asked at all is a failed job: there is no result to
// render and nothing was observed.
func TestPrepareJobFailsWhenTheNodeCannotBeAsked(t *testing.T) {
	module, operator, _, _, _ := newDeployModule(t)
	operator.prepareErr = errors.New("agent socket is gone")
	payload, err := json.Marshal(prepareJobPayload{SiteID: testSite.ID, PHPVersion: "8.3"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := module.prepareJob(context.Background(), payload, func(int, string) error { return nil }); err == nil {
		t.Fatal("prepareJob accepted a node that could not be reached")
	}
}
