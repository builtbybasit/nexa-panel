package firewall

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// fakeRunner records ufw invocations and serves canned output per command.
type fakeRunner struct {
	calls   [][]string
	byArgs  map[string][]byte
	failCmd string
}

func (f *fakeRunner) Run(_ context.Context, command Command) ([]byte, error) {
	joined := command.Name + " " + strings.Join(command.Args, " ")
	if command.Name == "ufw" {
		f.calls = append(f.calls, command.Args)
	}
	if f.failCmd != "" && strings.Contains(joined, f.failCmd) {
		return []byte("boom"), errors.New("exit status 1")
	}
	if out, ok := f.byArgs[strings.Join(command.Args, " ")]; ok {
		return out, nil
	}
	return nil, nil
}

func TestChangeValidate(t *testing.T) {
	cases := []struct {
		name    string
		change  Change
		wantErr bool
	}{
		{"enable", Change{Action: ActionEnable}, false},
		{"disable", Change{Action: ActionDisable}, false},
		{"allow port", Change{Action: ActionAllow, Rule: Rule{Port: "80", Protocol: "tcp"}}, false},
		{"allow range", Change{Action: ActionAllow, Rule: Rule{Port: "8000:8100"}}, false},
		{"bad port", Change{Action: ActionAllow, Rule: Rule{Port: "70000"}}, true},
		{"non-numeric port", Change{Action: ActionAllow, Rule: Rule{Port: "abc"}}, true},
		{"bad protocol", Change{Action: ActionAllow, Rule: Rule{Port: "80", Protocol: "sctp"}}, true},
		{"source without protocol", Change{Action: ActionAllow, Rule: Rule{Port: "80", From: "10.0.0.1"}}, true},
		{"source with protocol", Change{Action: ActionAllow, Rule: Rule{Port: "80", Protocol: "tcp", From: "10.0.0.0/8"}}, false},
		{"bad source", Change{Action: ActionAllow, Rule: Rule{Port: "80", Protocol: "tcp", From: "not-an-ip"}}, true},
		{"delete needs action", Change{Action: ActionDelete, Rule: Rule{Port: "80", Protocol: "tcp"}}, true},
		{"delete with action", Change{Action: ActionDelete, Rule: Rule{Port: "80", Protocol: "tcp", Action: ActionAllow}}, false},
		{"unknown action", Change{Action: "flush"}, true},
		{"bad comment", Change{Action: ActionAllow, Rule: Rule{Port: "80", Comment: "bad\nnewline"}}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.change.validate()
			if (err != nil) != tc.wantErr {
				t.Fatalf("validate() error = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}

func TestRuleArgs(t *testing.T) {
	got := ruleArgs(ActionAllow, Rule{Port: "80", Protocol: "tcp"})
	if want := []string{"allow", "80/tcp"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("simple allow = %v, want %v", got, want)
	}
	got = ruleArgs(ActionAllow, Rule{Port: "80", Protocol: "tcp", Comment: "panel"})
	if want := []string{"allow", "80/tcp", "comment", "panel"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("allow with comment = %v, want %v", got, want)
	}
	got = ruleArgs(ActionDeny, Rule{Port: "3306", Protocol: "tcp", From: "203.0.113.0/24"})
	if want := []string{"deny", "from", "203.0.113.0/24", "to", "any", "port", "3306", "proto", "tcp"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("scoped deny = %v, want %v", got, want)
	}
}

func TestDeleteArgsDropsComment(t *testing.T) {
	got := deleteArgs(Rule{Port: "80", Protocol: "tcp", Action: ActionAllow, Comment: "panel"})
	want := []string{"delete", "allow", "80/tcp"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("deleteArgs = %v, want %v", got, want)
	}
}

func TestParseStatus(t *testing.T) {
	output := `Status: active
Logging: on (low)
Default: deny (incoming), allow (outgoing), disabled (routed)
New profiles: skip

To                         Action      From
--                         ------      ----
22/tcp                     ALLOW IN    Anywhere
443                        ALLOW IN    Anywhere
3306/tcp                   DENY IN     203.0.113.5
22/tcp (v6)                ALLOW IN    Anywhere (v6)
`
	status := parseStatus(output)
	if !status.Active {
		t.Fatal("expected Active")
	}
	if len(status.Rules) != 4 {
		t.Fatalf("expected 4 rules, got %d: %+v", len(status.Rules), status.Rules)
	}
	if r := status.Rules[0]; r.Port != "22" || r.Protocol != "tcp" || r.Action != ActionAllow || r.From != "" || r.V6 {
		t.Fatalf("rule 0 = %+v", r)
	}
	if r := status.Rules[1]; r.Port != "443" || r.Protocol != "" || r.Action != ActionAllow {
		t.Fatalf("rule 1 = %+v", r)
	}
	if r := status.Rules[2]; r.Action != ActionDeny || r.From != "203.0.113.5" {
		t.Fatalf("rule 2 = %+v", r)
	}
	if r := status.Rules[3]; !r.V6 || r.Port != "22" {
		t.Fatalf("rule 3 = %+v", r)
	}
}

func TestParseStatusInactive(t *testing.T) {
	status := parseStatus("Status: inactive\n")
	if status.Active {
		t.Fatal("expected inactive")
	}
	if len(status.Rules) != 0 {
		t.Fatalf("expected no rules, got %+v", status.Rules)
	}
}

func TestEnableAutoAllowsSurvivalPorts(t *testing.T) {
	// Both ufw and sshd invocations flow through the same runner; it serves canned
	// output keyed by the joined argument vector. "-T" answers `sshd -T`.
	runner := &fakeRunner{byArgs: map[string][]byte{
		"-T":             []byte("port 22\nport 2222\n"),
		"status verbose": []byte("Status: active\n"),
	}}
	op, _ := NewHostOperator(runner)

	if _, err := op.Apply(context.Background(), Change{Action: ActionEnable}); err != nil {
		t.Fatalf("enable: %v", err)
	}

	var allowed []string
	sawForceEnable := false
	for _, call := range runner.calls {
		if len(call) == 2 && call[0] == "allow" {
			allowed = append(allowed, call[1])
		}
		if len(call) == 2 && call[0] == "--force" && call[1] == "enable" {
			sawForceEnable = true
		}
	}
	if !sawForceEnable {
		t.Fatal("expected `ufw --force enable` to be issued last")
	}
	for _, want := range []string{"22/tcp", "80/tcp", "443/tcp", "2222/tcp"} {
		if !contains(allowed, want) {
			t.Fatalf("expected survival allow for %s, got %v", want, allowed)
		}
	}
}

func TestApplyRejectsWhenUfwMissing(t *testing.T) {
	runner := &fakeRunner{failCmd: "version"}
	op, _ := NewHostOperator(runner)
	if _, err := op.Apply(context.Background(), Change{Action: ActionEnable}); err == nil {
		t.Fatal("expected error when ufw is not installed")
	}
}

func TestDiscoverReportsMissingUfw(t *testing.T) {
	runner := &fakeRunner{failCmd: "version"}
	op, _ := NewHostOperator(runner)
	status, err := op.Discover(context.Background())
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if status.Installed {
		t.Fatal("expected Installed=false when ufw version fails")
	}
}

func contains(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

// `ufw allow OpenSSH` — the documented Ubuntu default — lists in
// `ufw status verbose` with the profile name appended to the port. Read
// verbatim, the trailer becomes the protocol ("tcp (openssh)"), which nothing
// matches: the deploy prepare check reports port 22 closed on a node where SSH
// is plainly allowed, and the Firewall page lists a protocol that is not one.
func TestParseStatusStripsAppProfileNames(t *testing.T) {
	output := `Status: active

To                         Action      From
--                         ------      ----
22/tcp (OpenSSH)           ALLOW IN    Anywhere
80,443/tcp (Nginx Full)    ALLOW IN    Anywhere
22/tcp (OpenSSH (v6))      ALLOW IN    Anywhere (v6)
`
	status := parseStatus(output)
	if len(status.Rules) != 3 {
		t.Fatalf("expected 3 rules, got %d: %+v", len(status.Rules), status.Rules)
	}
	for i, want := range []Rule{
		{Action: ActionAllow, Port: "22", Protocol: "tcp"},
		{Action: ActionAllow, Port: "80,443", Protocol: "tcp"},
		{Action: ActionAllow, Port: "22", Protocol: "tcp", V6: true},
	} {
		if status.Rules[i] != want {
			t.Fatalf("rule %d = %+v, want %+v", i, status.Rules[i], want)
		}
	}
}

// A bare "(v6)" row and a plain numeric row must be untouched by the profile
// strip.
func TestParseStatusLeavesPlainRowsAlone(t *testing.T) {
	status := parseStatus("Status: active\n8080                       ALLOW IN    Anywhere\n")
	if len(status.Rules) != 1 || status.Rules[0] != (Rule{Action: ActionAllow, Port: "8080"}) {
		t.Fatalf("rules = %+v", status.Rules)
	}
}
