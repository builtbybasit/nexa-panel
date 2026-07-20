package main

import (
	"flag"
	"fmt"

	"github.com/nexa-panel/nexa-panel/internal/platform/agentauth"
)

// runAgentToken prepares the shared credential before systemd considers the
// privileged agent started. It deliberately never prints the credential.
func runAgentToken(args []string) error {
	flags := flag.NewFlagSet("agent-token", flag.ContinueOnError)
	path := flags.String("path", envOrDefault("NEXA_AGENT_TOKEN", "/tmp/nexa-panel/agent.token"), "shared agent credential path")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("agent-token does not accept positional arguments: %q", flags.Args())
	}
	if _, err := agentauth.OpenOrCreate(*path); err != nil {
		return fmt.Errorf("prepare agent credential: %w", err)
	}
	return nil
}
