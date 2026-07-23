package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/nexa-panel/nexa-panel/internal/platform/preflight"
)

type stubCheck struct {
	result preflight.Result
}

func (c stubCheck) Name() string                         { return c.result.Name }
func (c stubCheck) Run(context.Context) preflight.Result { return c.result }

// TestDoctorJSONKeepsStdoutClean pins the contract scripts/install.sh relies on:
// the report is the only thing on stdout, so a log line cannot corrupt it.
func TestDoctorJSONKeepsStdoutClean(t *testing.T) {
	var stdout, stderr, logged bytes.Buffer
	// The process logger already targets stderr (see runMain); this stands in
	// for it so the assertion below can tell the two streams apart.
	logger := slog.New(slog.NewTextHandler(&logged, nil))
	env := doctorEnv{
		stdout: &stdout,
		stderr: &stderr,
		checks: func(preflight.Config) []preflight.Check {
			return []preflight.Check{stubCheck{result: preflight.Result{Name: "memory", Title: "Memory", Status: preflight.StatusOK, Detail: "plenty"}}}
		},
	}

	if err := runDoctorIn([]string{"--preflight", "--json"}, logger, env); err != nil {
		t.Fatalf("doctor: %v", err)
	}

	var report preflight.Report
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("stdout is not one JSON document (%v): %s", err, stdout.String())
	}
	if len(report.Checks) != 1 || report.Checks[0].Name != "memory" {
		t.Fatalf("unexpected report: %+v", report)
	}
	if !strings.Contains(logged.String(), "doctor completed") {
		t.Fatalf("the log line did not reach the log stream: %s", logged.String())
	}
}

// TestDoctorJSONGatesOnBlockers covers the case scripts/install.sh actually
// parses: a host with a blocker, where the command also returns an error and
// the caller logs it. stdout must still be nothing but the report.
func TestDoctorJSONGatesOnBlockers(t *testing.T) {
	var stdout, stderr, logged bytes.Buffer
	env := doctorEnv{
		stdout: &stdout,
		stderr: &stderr,
		checks: func(preflight.Config) []preflight.Check {
			return []preflight.Check{stubCheck{result: preflight.Result{Name: "ports", Title: "Ports", Status: preflight.StatusBlocker, Detail: "80 (apache2)"}}}
		},
	}

	logger := slog.New(slog.NewTextHandler(&logged, nil))
	err := runDoctorIn([]string{"--preflight", "--json"}, logger, env)
	if err == nil {
		t.Fatal("a blocker must fail the gate")
	}
	// What main does with that error, on the stream main logs to.
	logger.Error("nexa stopped", "error", err)

	var report preflight.Report
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("stdout is not one JSON document (%v): %s", err, stdout.String())
	}
	if !report.HasBlocker() {
		t.Fatalf("report does not carry the blocker: %+v", report)
	}
	if strings.Contains(stdout.String(), "nexa stopped") {
		t.Fatalf("a log line landed in the report: %s", stdout.String())
	}
}

func TestRenderReport(t *testing.T) {
	var out bytes.Buffer
	renderReport(&out, preflight.Report{
		Version: "1.2.3",
		Checks: []preflight.Result{
			{Name: "memory", Title: "Memory", Status: preflight.StatusOK, Detail: "8.0 GiB total, 6.0 GiB available, profile pro"},
			{Name: "podman", Title: "Podman", Status: preflight.StatusWarning, Detail: "unavailable (not installed)", Remediation: "install it"},
			{Name: "ports", Title: "Ports", Status: preflight.StatusBlocker, Detail: "already listened on: 80 (apache2)", Remediation: "stop apache2"},
		},
	})

	want := strings.Join([]string{
		"Nexa Panel 1.2.3",
		"Memory: 8.0 GiB total, 6.0 GiB available, profile pro",
		"Podman: warning - unavailable (not installed)",
		"  install it",
		"Ports: BLOCKER - already listened on: 80 (apache2)",
		"  stop apache2",
		"",
	}, "\n")
	if out.String() != want {
		t.Fatalf("rendered:\n%s\nwant:\n%s", out.String(), want)
	}
}
