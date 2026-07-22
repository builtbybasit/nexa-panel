package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/nexa-panel/nexa-panel/internal/platform/audit"
	"github.com/nexa-panel/nexa-panel/internal/platform/persistence"
)

const auditUsage = "usage: nexa audit verify [--state /var/lib/nexa-panel/control.db]"

func runAudit(args []string) error {
	if len(args) == 0 {
		return errors.New(auditUsage)
	}
	switch args[0] {
	case "verify":
		return runAuditVerify(args, os.Stdout)
	default:
		return errors.New(auditUsage)
	}
}

// runAuditVerify implements `nexa audit verify`: it walks the audit hash chain in
// the control-plane state database and prints the verdict. The chain has been
// verifiable over HTTP since it shipped, but nothing consumed that endpoint, so
// the evidence was invisible to the operator who most needs it — one on a node
// they suspect, with a shell and no working panel. It deliberately reads the
// database directly and applies no migrations: verification must see the log
// exactly as it is on disk.
//
// It exits non-zero when the chain is not intact, so a cron or a monitoring
// check can alert on it.
func runAuditVerify(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("audit verify", flag.ContinueOnError)
	statePath := flags.String("state", envOrDefault("NEXA_STATE_DATABASE", "/var/lib/nexa-panel/control.db"), "control-plane state database path")
	if err := flags.Parse(args[1:]); err != nil {
		return err
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
	result, err := log.Verify(ctx)
	if err != nil {
		return fmt.Errorf("verify audit chain: %w", err)
	}

	renderVerifyResult(out, *statePath, result)
	if !result.Intact {
		return errors.New("the audit chain does not verify; treat the log as tampered with")
	}
	return nil
}

// renderVerifyResult always prints both counts, not just the verdict. The counts
// are the operator's only handle on the rewrites this chain cannot reject: a log
// that reports itself intact while every event it holds is unhashed has been
// blanked, and only the unhashed count shows it.
func renderVerifyResult(out io.Writer, statePath string, result audit.VerifyResult) {
	fmt.Fprintf(out, "Audit chain: %s\n", statePath)
	if result.Intact {
		fmt.Fprintln(out, "Verdict: INTACT")
	} else {
		fmt.Fprintln(out, "Verdict: TAMPERING DETECTED")
	}
	if result.Reason != "" {
		fmt.Fprintf(out, "  Reason: %s\n", result.Reason)
	}
	if result.FirstBrokenID != nil {
		fmt.Fprintf(out, "  First broken event: %d\n", *result.FirstBrokenID)
	}
	fmt.Fprintf(out, "  Hashed events checked: %d\n", result.Checked)
	fmt.Fprintf(out, "  Events carrying no chain hash: %d\n", result.Unhashed)
	if result.Unhashed > 0 {
		fmt.Fprintln(out, "  Note: unhashed events are outside the chain and it proves nothing about them."+
			" Expect them only on a database upgraded from before the chain shipped, and expect the count to stop growing.")
	}
	fmt.Fprintf(out, "  Guarantee: %s\n", result.Guarantee)
	fmt.Fprintln(out, "  See docs/security/audit-chain.md for what a verdict is and is not worth.")
}
