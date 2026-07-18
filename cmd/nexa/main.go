package main

import (
	"os"

	"errors"
	"fmt"
	"github.com/nexa-panel/nexa-panel/internal/platform/version"
	"log/slog"
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
  nexa api [--address 127.0.0.1:8080] [--state /var/lib/nexa-panel/control.db] [--master-key /var/lib/nexa-panel/master.key]
  nexa agent [--socket /run/nexa-panel/agent.sock] [--token /run/nexa-panel/agent.token]
  nexa doctor
  nexa version`)
}
