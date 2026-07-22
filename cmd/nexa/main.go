package main

import (
	"errors"
	"fmt"
	"log/slog"
	"os"

	"github.com/nexa-panel/nexa-panel/internal/platform/version"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	if err := run(os.Args[1:], logger); err != nil {
		logger.Error("nexa stopped", "error", err)
		os.Exit(1)
	}
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
		return runDoctor(logger)
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

func envOrDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func printUsage() {
	fmt.Fprintln(os.Stderr, `Nexa Panel - Modern Server Management Platform

Usage:
  nexa api [--address 127.0.0.1:8888 | --unix-socket /run/nexa-panel/api.sock] [--state /var/lib/nexa-panel/control.db] [--master-key /var/lib/nexa-panel/master.key]
  nexa agent [--socket /run/nexa-panel/agent.sock] [--token /run/nexa-panel/agent.token]
  nexa agent-token [--path /etc/nexa-panel/agent.token]
  nexa self-update [--check | --version X.Y.Z | --binary /path/to/nexa-linux-ARCH] [--socket /run/nexa-panel/agent.sock] [--token /run/nexa-panel/agent.token]
  nexa self-update rollback [--socket /run/nexa-panel/agent.sock] [--token /run/nexa-panel/agent.token]
  nexa backup system --account <id|name> [--state /var/lib/nexa-panel/control.db]
  nexa backup system-restore --archive nexa-panel-system.tar.gz [--state /var/lib/nexa-panel/control.db] [--master-key /var/lib/nexa-panel/master.key] [--force]
  nexa doctor
  nexa version`)
}
