package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/platform/publishing"
)

const publishingUsage = `usage:
  nexa publishing show [--json | --shell] [--state-file PATH] [--vhost PATH]
  sudo nexa publishing set --hostname HOST [--port N] [--tls] [--external-tls] [--listen-address ADDR] [--state-file PATH]
  sudo nexa publishing migrate [--state-file PATH] [--vhost PATH]`

// runPublishing exposes the managed publishing record to the one caller that is
// not written in Go: the packaged installer, which needs to know how this node
// is published without re-deriving it from the live vhost. `show --shell` is
// meant to be eval'd there, replacing the sed/grep pair that used to read the
// listener back out of a file Certbot also writes to.
func runPublishing(args []string) error {
	if len(args) == 0 {
		return errors.New(publishingUsage)
	}
	switch args[0] {
	case "show":
		return runPublishingShow(args[1:], os.Stdout)
	case "set":
		return runPublishingSet(args[1:], os.Stdout)
	case "migrate":
		return runPublishingMigrate(args[1:], os.Stdout)
	default:
		return errors.New(publishingUsage)
	}
}

// runPublishingShow never writes. A read of the record must be usable from any
// context — a health check, a support session, a script — without the chance of
// mutating how the node is published as a side effect. Recovering the record on
// a node that predates it is `migrate`'s job, and it is explicit.
func runPublishingShow(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("publishing show", flag.ContinueOnError)
	statePath := flags.String("state-file", publishing.DefaultStatePath, "managed publishing record")
	vhostPath := flags.String("vhost", publishing.DefaultVhostPath, "panel vhost, read only when no record exists yet")
	asJSON := flags.Bool("json", false, "render the record as JSON")
	asShell := flags.Bool("shell", false, "render the record as shell assignments for eval")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *asJSON && *asShell {
		return errors.New("choose either --json or --shell")
	}

	state, err := publishing.Load(*statePath)
	inferred := false
	if errors.Is(err, publishing.ErrNoRecord) {
		// Fall back to the vhost so this command answers on a node that has not
		// been migrated yet, and say so, so nobody mistakes a guess for the record.
		file, openErr := os.Open(*vhostPath)
		if openErr != nil {
			return fmt.Errorf("no publishing record at %s and no panel vhost to recover one from: %w", *statePath, openErr)
		}
		defer file.Close()
		state, err = publishing.InferFromVhost(file)
		inferred = true
	}
	if err != nil {
		return err
	}

	switch {
	case *asJSON:
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(state)
	case *asShell:
		fmt.Fprintf(out, "NEXA_PUBLISH_HOSTNAME=%s\n", shellQuote(state.Hostname))
		fmt.Fprintf(out, "NEXA_PUBLISH_LISTEN=%s\n", shellQuote(state.ListenDirective()))
		fmt.Fprintf(out, "NEXA_PUBLISH_PORT=%s\n", strconv.Itoa(state.Port))
		fmt.Fprintf(out, "NEXA_PUBLISH_TLS=%s\n", boolFlag(state.TLS))
		fmt.Fprintf(out, "NEXA_PUBLISH_EXTERNAL_TLS=%s\n", boolFlag(state.ExternalTLS))
		fmt.Fprintf(out, "NEXA_PUBLISH_MODE=%s\n", shellQuote(state.Mode()))
		fmt.Fprintf(out, "NEXA_PUBLISH_RECORDED=%s\n", boolFlag(!inferred))
		return nil
	default:
		fmt.Fprintf(out, "Publishing: %s\n", state.Mode())
		fmt.Fprintf(out, "  Hostname: %s\n", state.Hostname)
		fmt.Fprintf(out, "  Listen:   %s\n", state.ListenDirective())
		fmt.Fprintf(out, "  HTTPS:    %t\n", state.Encrypted())
		if inferred {
			fmt.Fprintf(out, "  Source:   inferred from %s; run `sudo nexa publishing migrate` to record it\n", *vhostPath)
			return nil
		}
		fmt.Fprintf(out, "  Source:   %s (recorded %s)\n", state.Source, state.UpdatedAt.Format(time.RFC3339))
		return nil
	}
}

func runPublishingSet(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("publishing set", flag.ContinueOnError)
	statePath := flags.String("state-file", publishing.DefaultStatePath, "managed publishing record")
	hostname := flags.String("hostname", "", "server name the panel answers on")
	listenAddress := flags.String("listen-address", "", "bind address; empty publishes on every interface")
	port := flags.Int("port", 0, "listener port; defaults to 443 with --tls and 80 without")
	tls := flags.Bool("tls", false, "the panel vhost terminates TLS itself")
	externalTLS := flags.Bool("external-tls", false, "TLS is terminated in front of this node")
	source := flags.String("source", "install", "what wrote this record, for the operator reading it")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if os.Geteuid() != 0 {
		return errors.New("publishing set writes root-owned configuration and must be run as root")
	}
	if *hostname == "" {
		return errors.New("--hostname is required: " + publishingUsage)
	}
	if *port == 0 {
		*port = publishing.DefaultPort(*tls)
	}

	state := publishing.State{
		Hostname: *hostname, ListenAddress: *listenAddress, Port: *port,
		TLS: *tls, ExternalTLS: *externalTLS, UpdatedAt: time.Now(), Source: *source,
	}
	if err := publishing.Save(*statePath, state); err != nil {
		return err
	}
	fmt.Fprintf(out, "Recorded publishing state in %s: %s on %s (listen %s).\n",
		*statePath, state.Mode(), state.Hostname, state.ListenDirective())
	return nil
}

func runPublishingMigrate(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("publishing migrate", flag.ContinueOnError)
	statePath := flags.String("state-file", publishing.DefaultStatePath, "managed publishing record")
	vhostPath := flags.String("vhost", publishing.DefaultVhostPath, "panel vhost to recover the record from, once")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if os.Geteuid() != 0 {
		return errors.New("publishing migrate writes root-owned configuration and must be run as root")
	}
	state, migrated, err := publishing.Migrate(*statePath, *vhostPath, time.Now())
	if errors.Is(err, publishing.ErrNoRecord) {
		// Nothing published and nothing to recover: not an error, just a node the
		// installer has not published yet.
		fmt.Fprintf(out, "No panel vhost at %s; nothing to record.\n", *vhostPath)
		return nil
	}
	if err != nil {
		return err
	}
	if !migrated {
		fmt.Fprintf(out, "Publishing state is already recorded in %s: %s on %s.\n", *statePath, state.Mode(), state.Hostname)
		return nil
	}
	fmt.Fprintf(out, "Recovered publishing state from %s: %s on %s (listen %s).\n",
		*vhostPath, state.Mode(), state.Hostname, state.ListenDirective())
	fmt.Fprintln(out, "If TLS is terminated in front of this node, restate it once with `sudo nexa publishing set --external-tls`.")
	return nil
}

// shellQuote renders a value for `eval` in the installer. Hostnames and listen
// directives come from the record rather than a user, but the record is a file
// root can edit, and a shell-quoted value cannot become a command either way.
func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

func boolFlag(value bool) string {
	if value {
		return "1"
	}
	return "0"
}
