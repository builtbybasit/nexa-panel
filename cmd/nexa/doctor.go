package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/platform/preflight"
	"github.com/nexa-panel/nexa-panel/internal/platform/version"
)

// doctorEnv is what the command writes to and probes with. Tests substitute it
// to keep the report hermetic and to read the two streams apart.
type doctorEnv struct {
	stdout io.Writer
	stderr io.Writer
	checks func(cfg preflight.Config) []preflight.Check
}

func runDoctor(args []string, logger *slog.Logger) error {
	return runDoctorIn(args, logger, doctorEnv{stdout: os.Stdout, stderr: os.Stderr, checks: preflight.Checks})
}

func runDoctorIn(args []string, logger *slog.Logger, env doctorEnv) error {
	flags := flag.NewFlagSet("doctor", flag.ContinueOnError)
	flags.SetOutput(env.stderr)
	gate := flags.Bool("preflight", false, "fail when a check reports a blocker, so an installer can abort")
	asJSON := flags.Bool("json", false, "render the report as JSON")
	allowExisting := flags.Bool("allow-existing", false, "treat an already-running Nexa Panel as an in-place upgrade rather than a blocker")
	timeout := flags.Duration("timeout", 20*time.Second, "budget for the whole report")
	if err := flags.Parse(args); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	config := preflight.DefaultConfig()
	config.AllowExistingInstall = *allowExisting
	report := preflight.Report{
		Version: version.Version,
		Checks:  preflight.Run(ctx, env.checks(config)),
	}

	if *asJSON {
		// scripts/install.sh parses stdout as a single JSON document. Nothing
		// else may be written there — the process logger targets stderr (see
		// runMain), which is what keeps that true on the failing exit path too.
		encoder := json.NewEncoder(env.stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(report); err != nil {
			return fmt.Errorf("render report: %w", err)
		}
	} else {
		renderReport(env.stdout, report)
	}

	logger.Info("doctor completed", "blockers", len(report.Blockers()))
	if *gate && report.HasBlocker() {
		return errors.New("preflight found conditions that must be resolved before installing")
	}
	return nil
}

func renderReport(out io.Writer, report preflight.Report) {
	fmt.Fprintf(out, "Nexa Panel %s\n", report.Version)
	for _, check := range report.Checks {
		switch check.Status {
		case preflight.StatusOK:
			fmt.Fprintf(out, "%s: %s\n", check.Title, check.Detail)
		case preflight.StatusWarning:
			fmt.Fprintf(out, "%s: warning - %s\n", check.Title, check.Detail)
		default:
			fmt.Fprintf(out, "%s: BLOCKER - %s\n", check.Title, check.Detail)
		}
		if check.Status != preflight.StatusOK && check.Remediation != "" {
			fmt.Fprintf(out, "  %s\n", check.Remediation)
		}
	}
}
