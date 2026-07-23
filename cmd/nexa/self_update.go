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
	"github.com/nexa-panel/nexa-panel/internal/platform/version"
)

// runSelfUpdate is the operator-facing CLI for panel updates. Published release
// checks and applies reach the authenticated agent; --binary and rollback stay
// local and root-only so no network request can name a host path and recovery
// remains available when the agent is down.
//
// The socket and token default to their packaged locations, like every other
// node-facing command: this is run as `sudo nexa self-update` on a real server,
// where nothing exports NEXA_AGENT_SOCKET, so a development default here would
// only ever produce a confusing "connection refused" on the machine it is meant
// to serve.
func runSelfUpdate(args []string) error {
	if len(args) > 0 && args[0] == "rollback" {
		return runSelfUpdateRollback(args[1:])
	}
	if len(args) > 0 && args[0] == "activate" {
		return runSelfUpdateActivation(args[1:])
	}
	if len(args) > 0 && args[0] == "recover" {
		return runSelfUpdateRecovery(args[1:])
	}

	flags := flag.NewFlagSet("self-update", flag.ContinueOnError)
	socket := flags.String("socket", envOrDefault("NEXA_AGENT_SOCKET", "/run/nexa-panel/agent.sock"), "privileged agent Unix socket")
	tokenPath := flags.String("token", envOrDefault("NEXA_AGENT_TOKEN", "/etc/nexa-panel/agent.token"), "shared agent credential path")
	check := flags.Bool("check", false, "report the installed and latest versions without installing")
	target := flags.String("version", "", "install a specific release version instead of the latest")
	binary := flags.String("binary", "", "install a binary already staged on this host instead of downloading a release")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *binary != "" && (*check || *target != "") {
		return errors.New("--binary installs a local file and cannot be combined with --check or --version")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if *binary != "" {
		if os.Geteuid() != 0 {
			return errors.New("--binary is a local privileged operation and must be run as root")
		}
		absolute, err := filepath.Abs(*binary)
		if err != nil {
			return fmt.Errorf("resolve binary path: %w", err)
		}
		applyCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()
		operator, err := newLocalSelfUpdateOperator()
		if err != nil {
			return err
		}
		result, err := operator.ApplyLocalBinary(applyCtx, absolute)
		if err != nil {
			return fmt.Errorf("install %s: %w", absolute, err)
		}
		fmt.Printf("Installed %s (was %s) from %s.\n", result.TargetVersion, result.PreviousVersion, absolute)
		printUpdateOutcome(result)
		return nil
	}

	client := selfupdateoperator.NewUnixClient(*socket, *tokenPath)

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
	printUpdateOutcome(result)
	return nil
}

// runSelfUpdateRollback executes locally as root so recovery remains available
// when the agent socket or newly installed service cannot start.
func runSelfUpdateRollback(args []string) error {
	flags := flag.NewFlagSet("self-update rollback", flag.ContinueOnError)
	if err := flags.Parse(args); err != nil {
		return err
	}
	if os.Geteuid() != 0 {
		return errors.New("self-update rollback is a local privileged operation and must be run as root")
	}
	operator, err := newLocalSelfUpdateOperator()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	rollbackCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	result, err := operator.Rollback(rollbackCtx)
	if err != nil {
		return fmt.Errorf("roll back update: %w", err)
	}
	fmt.Printf("Rolled back to %s (was %s).\n", result.TargetVersion, result.PreviousVersion)
	printUpdateOutcome(result)
	return nil
}

// runSelfUpdateActivation is intentionally undocumented operator plumbing. A
// root-owned transient systemd unit invokes it after the agent has durably
// prepared a transaction; accepting only the fixed journal path is enforced by
// the operator itself.
func runSelfUpdateActivation(args []string) error {
	flags := flag.NewFlagSet("self-update activate", flag.ContinueOnError)
	transaction := flags.String("transaction", "", "managed update transaction journal")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if os.Geteuid() != 0 {
		return errors.New("self-update activation must run as root")
	}
	if *transaction == "" {
		return errors.New("--transaction is required")
	}
	operator, err := newLocalSelfUpdateOperator()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	activateCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	return operator.ActivateTransaction(activateCtx, *transaction)
}

func runSelfUpdateRecovery(args []string) error {
	flags := flag.NewFlagSet("self-update recover", flag.ContinueOnError)
	transaction := flags.String("transaction", "/var/lib/nexa-panel-update/transaction.json", "managed update transaction journal")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if os.Geteuid() != 0 {
		return errors.New("self-update recovery must run as root")
	}
	operator, err := newLocalSelfUpdateOperator()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	recoveryCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	return operator.RecoverTransaction(recoveryCtx, *transaction)
}

func newLocalSelfUpdateOperator() (*selfupdateoperator.HostOperator, error) {
	return selfupdateoperator.NewHostOperator(selfupdateoperator.HostConfig{InstalledVersion: version.Version})
}

func printUpdateOutcome(result selfupdateoperator.Result) {
	// A change that moved the binary but not the packaging is something the
	// operator has to know about while they are still at the terminal.
	if result.PackagingNote != "" {
		fmt.Printf("Note: %s.\n", result.PackagingNote)
	}
	if result.Activated {
		fmt.Println("The updated panel services are running and ready.")
	} else {
		fmt.Println("The update did not reach a ready service state.")
	}
}
