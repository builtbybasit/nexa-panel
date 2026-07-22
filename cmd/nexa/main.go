package main

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/nexa-panel/nexa-panel/internal/platform/version"
)

func main() {
	os.Exit(runMain(os.Args[1:], os.Stderr))
}

// runMain is main with the log stream injected, so the exit paths can be tested
// without ending the test binary. Logs go to stderr for every subcommand
// because stdout is a data channel: `nexa doctor --json` is parsed by
// scripts/install.sh and `nexa version` is read by scripts. Keeping the two
// apart at the process level is what stops a late log line — the "nexa stopped"
// one below fires after the report is written — from corrupting that data.
func runMain(args []string, logs io.Writer) int {
	args, logger, err := configureLogging(args, logs)
	if err != nil {
		fmt.Fprintln(logs, "nexa:", err)
		return 1
	}

	if err := run(args, logger); err != nil {
		logger.Error("nexa stopped", "error", err)
		return 1
	}
	return 0
}

func run(args []string, logger *slog.Logger) error {
	if len(args) == 0 {
		printUsage()
		return errors.New("a command is required")
	}

	switch args[0] {
	case "api":
		return runAPI(args[1:], logger)
	case "agent":
		return runAgent(args[1:], logger)
	case "agent-token":
		return runAgentToken(args[1:])
	case "self-update":
		return runSelfUpdate(args[1:])
	case "backup":
		return runBackup(args[1:], logger)
	case "doctor":
		return runDoctor(args[1:], logger)
	case "audit":
		return runAudit(args[1:])
	case "version":
		fmt.Println(version.String())
		return nil
	case "help", "-h", "--help":
		printUsage()
		return nil
	default:
		printUsage()
		return fmt.Errorf("unknown command %q", args[0])
	}
}

// configureLogging strips the logging flags off the front of the argument list
// and builds the logger every subcommand shares. Logging is a process-wide
// concern rather than a per-command one, so it is settled before dispatch
// instead of being repeated in each subcommand's flag set. A flag beats the
// environment, and the environment beats the info/json default.
func configureLogging(args []string, output io.Writer) ([]string, *slog.Logger, error) {
	level := slog.LevelInfo
	if value := strings.TrimSpace(os.Getenv("NEXA_LOG_LEVEL")); value != "" {
		parsed, err := parseLogLevel(value)
		if err != nil {
			return nil, nil, fmt.Errorf("NEXA_LOG_LEVEL: %w", err)
		}
		level = parsed
	}
	format := "json"
	if value := strings.TrimSpace(os.Getenv("NEXA_LOG_FORMAT")); value != "" {
		if err := validLogFormat(value); err != nil {
			return nil, nil, fmt.Errorf("NEXA_LOG_FORMAT: %w", err)
		}
		format = strings.ToLower(value)
	}

	for len(args) > 0 && strings.HasPrefix(args[0], "-") && args[0] != "-" {
		name, value, assigned := strings.Cut(strings.TrimLeft(args[0], "-"), "=")
		if name != "log-level" && name != "log-format" {
			break
		}
		args = args[1:]
		if !assigned {
			if len(args) == 0 {
				return nil, nil, fmt.Errorf("--%s requires a value", name)
			}
			value, args = args[0], args[1:]
		}
		if name == "log-level" {
			parsed, err := parseLogLevel(value)
			if err != nil {
				return nil, nil, fmt.Errorf("--log-level: %w", err)
			}
			level = parsed
			continue
		}
		if err := validLogFormat(value); err != nil {
			return nil, nil, fmt.Errorf("--log-format: %w", err)
		}
		format = strings.ToLower(value)
	}

	options := &slog.HandlerOptions{Level: level}
	if format == "text" {
		return args, slog.New(slog.NewTextHandler(output, options)), nil
	}
	return args, slog.New(slog.NewJSONHandler(output, options)), nil
}

func parseLogLevel(value string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("unknown log level %q (want debug, info, warn, or error)", value)
	}
}

func validLogFormat(value string) error {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "json", "text":
		return nil
	default:
		return fmt.Errorf("unknown log format %q (want json or text)", value)
	}
}

func envOrDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func printUsage() {
	fmt.Fprintln(os.Stderr, `Nexa Panel - Modern Server Management Platform

Usage:
  nexa [--log-level info] [--log-format json] <command> [flags]

Commands:
  nexa api [--address 127.0.0.1:8888 | --unix-socket /run/nexa-panel/api.sock] [--state /var/lib/nexa-panel/control.db] [--master-key /etc/nexa-panel/master.key]
  nexa agent [--socket /run/nexa-panel/agent.sock] [--token /run/nexa-panel/agent.token]
  nexa agent-token [--path /etc/nexa-panel/agent.token]
  nexa self-update [--check | --version X.Y.Z | --binary /path/to/nexa-linux-ARCH] [--socket /run/nexa-panel/agent.sock] [--token /run/nexa-panel/agent.token]
  nexa self-update rollback [--socket /run/nexa-panel/agent.sock] [--token /run/nexa-panel/agent.token]
  nexa backup system --account <id|name> [--state /var/lib/nexa-panel/control.db]
  nexa backup system-restore --archive nexa-panel-system.tar.gz [--state /var/lib/nexa-panel/control.db] [--master-key /etc/nexa-panel/master.key] [--force]
  nexa doctor [--preflight] [--json] [--allow-existing] [--timeout 20s]
  nexa audit verify [--state /var/lib/nexa-panel/control.db]
  nexa version

Doctor flags:
  --preflight       exit non-zero when a check reports a blocker, so an installer can abort
  --json            render the report as JSON instead of text
  --allow-existing  treat an already-running Nexa Panel as an in-place upgrade rather than a blocker
  --timeout         budget for the whole report (default 20s)

Global flags (before the command; NEXA_LOG_LEVEL and NEXA_LOG_FORMAT set the same
values, and a flag overrides the environment):
  --log-level   debug|info|warn|error (default info)
  --log-format  json|text (default json)`)
}
