package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/platform/audit"
	"github.com/nexa-panel/nexa-panel/internal/platform/identity"
	"github.com/nexa-panel/nexa-panel/internal/platform/persistence"
)

const mfaUsage = "usage: sudo nexa mfa reset --user <username> [--state /var/lib/nexa-panel/control.db]"

func runMFA(args []string) error {
	if len(args) == 0 {
		return errors.New(mfaUsage)
	}
	switch args[0] {
	case "reset":
		return runMFAReset(args[1:], os.Stdout)
	default:
		return errors.New(mfaUsage)
	}
}

// runMFAReset implements `sudo nexa mfa reset --user <name>`: the break-glass
// path for an account that enrolled a second factor and then lost both the
// authenticator and every recovery code. Without it that account is permanently
// locked out, because every in-panel recovery route needs a credential the
// operator no longer has.
//
// It is root-only and offline by design, following the same euid gate as the
// local self-update paths: the one credential a locked-out operator can still
// prove is root on the node, and root can already read the state database. It
// does not talk to the API, so it works whether the panel is up or down — and
// running it against a live panel is safe, because SQLite/WAL serialises the one
// transaction it performs against the API's own writes.
//
// Enrollment is optional by policy, so this deliberately does not leave the
// account unable to sign in: the factor is removed, every session is revoked,
// and the next sign-in is asked to enroll again.
func runMFAReset(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("mfa reset", flag.ContinueOnError)
	username := flags.String("user", "", "username of the account whose second factor is cleared")
	statePath := flags.String("state", envOrDefault("NEXA_STATE_DATABASE", "/var/lib/nexa-panel/control.db"), "control-plane state database path")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *username == "" {
		return errors.New("--user is required: " + mfaUsage)
	}
	// Checked before the database is opened so a non-root operator is told why
	// rather than being handed a permission error on control.db.
	if os.Geteuid() != 0 {
		return errors.New("mfa reset is a local privileged operation and must be run as root")
	}

	ctx := context.Background()
	store, err := persistence.Open(*statePath)
	if err != nil {
		return fmt.Errorf("open state database: %w", err)
	}
	defer store.Close()
	log, err := audit.New(ctx, store)
	if err != nil {
		return fmt.Errorf("initialize audit log: %w", err)
	}

	result, err := identity.ResetSecondFactor(ctx, store, log, *username, time.Now())
	if errors.Is(err, identity.ErrUnknownAccount) {
		return err
	}
	if err != nil {
		return fmt.Errorf("reset the second factor for %q: %w", *username, err)
	}

	fmt.Fprintf(out, "Cleared multi-factor authentication for %s (%s).\n", result.Username, result.Role)
	if !result.WasEnrolled {
		fmt.Fprintln(out, "Note: this account had no confirmed second factor; any half-finished enrollment was cleared.")
	}
	fmt.Fprintf(out, "Sessions signed out: %d\n", result.SessionsRevoked)
	fmt.Fprintln(out, "The account now signs in with its password alone and is asked to enroll a new authenticator.")
	fmt.Fprintln(out, "Recorded as identity.mfa_break_glass_reset in the audit log; see docs/runbooks/mfa-recovery.md.")
	return nil
}
