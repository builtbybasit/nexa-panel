package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	selfupdateoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/selfupdate"
)

// runSelfUpdate is the operator-facing CLI for the panel self-update. It reaches
// the privileged agent over the same authenticated unix socket the control plane
// uses, so the download, checksum verification, atomic swap, and detached
// restart all happen inside nexa-agent — the CLI only issues the request and
// prints the outcome.
func runSelfUpdate(args []string) error {
	if len(args) > 0 && args[0] == "rollback" {
		return runSelfUpdateRollback(args[1:])
	}

	flags := flag.NewFlagSet("self-update", flag.ContinueOnError)
	socket := flags.String("socket", envOrDefault("NEXA_AGENT_SOCKET", "/tmp/nexa-panel/agent.sock"), "privileged agent Unix socket")
	tokenPath := flags.String("token", envOrDefault("NEXA_AGENT_TOKEN", "/tmp/nexa-panel/agent.token"), "shared agent credential path")
	check := flags.Bool("check", false, "report the installed and latest versions without installing")
	target := flags.String("version", "", "install a specific release version instead of the latest")
	binary := flags.String("binary", "", "install a binary already staged on this host instead of downloading a release")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *binary != "" && (*check || *target != "") {
		return errors.New("--binary installs a local file and cannot be combined with --check or --version")
	}

	client := selfupdateoperator.NewUnixClient(*socket, *tokenPath)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if *binary != "" {
		absolute, err := filepath.Abs(*binary)
		if err != nil {
			return fmt.Errorf("resolve binary path: %w", err)
		}
		applyCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()
		result, err := client.Apply(applyCtx, selfupdateoperator.Change{BinaryPath: absolute})
		if err != nil {
			return fmt.Errorf("install %s: %w", absolute, err)
		}
		fmt.Printf("Installed %s (was %s) from %s.\n", result.TargetVersion, result.PreviousVersion, absolute)
		printRestart(result)
		return nil
	}

	if *check {
		checkCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		availability, err := client.Latest(checkCtx)
		if err != nil {
			return fmt.Errorf("check for updates: %w", err)
		}
		fmt.Printf("Installed: %s\n", availability.InstalledVersion)
		if availability.Latest == nil {
			fmt.Println("Latest:    unknown (no published release found)")
			return nil
		}
		fmt.Printf("Latest:    %s\n", availability.Latest.Version)
		if availability.UpdateAvailable {
			fmt.Println("An update is available. Run `nexa self-update` to install it.")
		} else {
			fmt.Println("This node is up to date.")
		}
		return nil
	}

	// The apply covers a full release download; give it room beyond a check.
	applyCtx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	result, err := client.Apply(applyCtx, selfupdateoperator.Change{Version: *target})
	if err != nil {
		return fmt.Errorf("apply update: %w", err)
	}
	fmt.Printf("Updated Nexa Panel from %s to %s.\n", result.PreviousVersion, result.TargetVersion)
	printRestart(result)
	return nil
}

// runSelfUpdateRollback reverts the panel to the binary preserved by the last
// swap. Like the forward update, the validation and atomic swap happen inside
// nexa-agent; the CLI only issues the request and prints the outcome.
func runSelfUpdateRollback(args []string) error {
	flags := flag.NewFlagSet("self-update rollback", flag.ContinueOnError)
	socket := flags.String("socket", envOrDefault("NEXA_AGENT_SOCKET", "/tmp/nexa-panel/agent.sock"), "privileged agent Unix socket")
	tokenPath := flags.String("token", envOrDefault("NEXA_AGENT_TOKEN", "/tmp/nexa-panel/agent.token"), "shared agent credential path")
	if err := flags.Parse(args); err != nil {
		return err
	}

	client := selfupdateoperator.NewUnixClient(*socket, *tokenPath)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	rollbackCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	result, err := client.Rollback(rollbackCtx)
	if err != nil {
		return fmt.Errorf("roll back update: %w", err)
	}
	fmt.Printf("Rolled back to %s (was %s).\n", result.TargetVersion, result.PreviousVersion)
	printRestart(result)
	return nil
}

func printRestart(result selfupdateoperator.Result) {
	if result.RestartScheduled {
		fmt.Printf("The panel services will restart automatically in %s.\n", result.RestartDelay)
	} else {
		fmt.Println("The binary was replaced; restart nexa-agent and nexa-api to finish.")
	}
}
