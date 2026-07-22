package firewall

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"sync"
)

// Command is a single privileged process invocation.
type Command struct {
	Name string
	Args []string
}

// Runner executes commands. Production uses execRunner; tests inject a fake.
type Runner interface {
	Run(context.Context, Command) ([]byte, error)
}

type execRunner struct{}

func (execRunner) Run(ctx context.Context, command Command) ([]byte, error) {
	process := exec.CommandContext(ctx, command.Name, command.Args...)
	output, err := process.CombinedOutput()
	if len(output) > 8*1024 {
		output = output[:8*1024]
	}
	return output, err
}

// HostOperator manages UFW on the local node.
type HostOperator struct {
	runner Runner
	// applyMu serialises applies so two concurrent rule changes can't interleave
	// their read-modify-enable windows.
	applyMu sync.Mutex
}

// NewHostOperator builds the operator; a nil runner uses the real exec runner.
func NewHostOperator(runner Runner) (*HostOperator, error) {
	if runner == nil {
		runner = execRunner{}
	}
	return &HostOperator{runner: runner}, nil
}

// Discover reports the current firewall status. A missing ufw binary is a
// normal state (Installed:false), not an error, so the UI can offer to install
// it rather than showing a failure.
func (o *HostOperator) Discover(ctx context.Context) (Status, error) {
	if !o.installed(ctx) {
		return Status{Installed: false}, nil
	}
	output, err := o.runner.Run(ctx, Command{Name: "ufw", Args: []string{"status", "verbose"}})
	if err != nil {
		return Status{}, commandError("read firewall status", output, err)
	}
	status := parseStatus(string(output))
	status.Installed = true
	return status, nil
}

// Apply performs one change under the serialising lock and returns the fresh
// status so the caller never has to issue a second Discover round-trip.
func (o *HostOperator) Apply(ctx context.Context, change Change) (Status, error) {
	if err := change.validate(); err != nil {
		return Status{}, err
	}
	o.applyMu.Lock()
	defer o.applyMu.Unlock()

	if !o.installed(ctx) {
		return Status{}, errors.New("ufw is not installed on this node")
	}

	var err error
	switch change.Action {
	case ActionEnable:
		err = o.enable(ctx)
	case ActionDisable:
		err = o.run(ctx, "disable")
	case ActionAllow, ActionDeny:
		err = o.run(ctx, ruleArgs(change.Action, change.Rule)...)
	case ActionDelete:
		err = o.run(ctx, deleteArgs(change.Rule)...)
	}
	if err != nil {
		return Status{}, err
	}
	return o.Discover(ctx)
}

// enable re-asserts the survival rules before turning the firewall on, so a
// default-deny policy can never orphan the SSH session or the panel. `ufw
// enable` normally prompts about disrupting SSH; --force answers yes.
func (o *HostOperator) enable(ctx context.Context) error {
	for _, port := range o.survivalPorts(ctx) {
		if err := o.run(ctx, "allow", port); err != nil {
			return fmt.Errorf("pre-authorise %s before enabling the firewall: %w", port, err)
		}
	}
	return o.run(ctx, "--force", "enable")
}

// survivalPorts is the set of tcp ports that must stay reachable across an
// enable: every port sshd listens on (22 as a floor) plus the panel's HTTP and
// HTTPS ports. Returned as "port/tcp" specs, de-duplicated and ordered.
func (o *HostOperator) survivalPorts(ctx context.Context) []string {
	ports := map[string]struct{}{"22": {}}
	for _, p := range o.sshdPorts(ctx) {
		ports[p] = struct{}{}
	}
	for _, p := range panelPorts {
		ports[p] = struct{}{}
	}
	specs := make([]string, 0, len(ports))
	for p := range ports {
		specs = append(specs, p+"/tcp")
	}
	sort.Strings(specs)
	return specs
}

// sshdPorts reads the ports sshd is actually configured to listen on via
// `sshd -T`. It is best-effort: any failure falls back to the 22 floor that
// survivalPorts always seeds.
func (o *HostOperator) sshdPorts(ctx context.Context) []string {
	output, err := o.runner.Run(ctx, Command{Name: "sshd", Args: []string{"-T"}})
	if err != nil {
		return nil
	}
	var ports []string
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(strings.ToLower(line))
		if len(fields) == 2 && fields[0] == "port" && portPattern.MatchString(fields[1]) {
			ports = append(ports, fields[1])
		}
	}
	return ports
}

func (o *HostOperator) installed(ctx context.Context) bool {
	_, err := o.runner.Run(ctx, Command{Name: "ufw", Args: []string{"version"}})
	return err == nil
}

func (o *HostOperator) run(ctx context.Context, args ...string) error {
	output, err := o.runner.Run(ctx, Command{Name: "ufw", Args: args})
	if err != nil {
		return commandError("ufw "+strings.Join(args, " "), output, err)
	}
	return nil
}

// ruleArgs builds the ufw argument vector for an allow/deny rule. It uses the
// simple `ufw allow 80/tcp` form when the rule applies to any source, and the
// extended `ufw allow from <src> to any port <p> proto <proto>` form when it is
// scoped to a source address. A comment, when present, is appended — UFW stores
// it but ignores it when matching a delete, which is why delete rebuilds the
// spec without the comment.
func ruleArgs(action string, rule Rule) []string {
	var args []string
	if rule.From == "" {
		args = []string{action, rule.portSpec()}
	} else {
		args = []string{action, "from", rule.From, "to", "any", "port", rule.Port, "proto", rule.Protocol}
	}
	if rule.Comment != "" {
		args = append(args, "comment", rule.Comment)
	}
	return args
}

// deleteArgs rebuilds a rule's spec under `ufw delete` without its comment: UFW
// matches a delete on the rule body alone, so a stored comment must be dropped
// or the delete finds nothing.
func deleteArgs(rule Rule) []string {
	rule.Comment = ""
	return append([]string{"delete"}, ruleArgs(rule.Action, rule)...)
}

func commandError(context string, output []byte, err error) error {
	message := strings.TrimSpace(string(output))
	if message == "" {
		return fmt.Errorf("%s: %w", context, err)
	}
	if len(message) > 500 {
		message = message[:500]
	}
	return fmt.Errorf("%s: %s", context, message)
}
