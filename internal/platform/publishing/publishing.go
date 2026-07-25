// Package publishing holds the panel's authoritative record of how it is
// published to the network: the hostname it answers on, the port, whether its
// own vhost terminates TLS, and whether TLS is terminated in front of it.
//
// This record exists because the alternative was inference. Every lifecycle
// operation that had to keep publishing unchanged — a packaging sync, an
// update, a re-run of the installer without publishing flags — re-derived the
// mode by grepping the live vhost for "managed by Certbot" and sed-ing its
// listen and server_name lines back out. That reads a file the panel does not
// own: Certbot rewrites it, an operator edits it, an nginx upgrade reformats
// it, and each of those silently changes what the panel believes about itself.
// Guessing wrong here is not cosmetic. Guessing "plaintext" on a TLS node
// republishes the panel unencrypted; guessing "TLS" on a plaintext node leaves
// an operator locked out of a node that no longer answers.
//
// So the panel writes the record when it publishes, and reads it every time
// afterwards. Vhost inspection survives only as a one-time migration for a node
// installed before the record existed (see InferFromVhost/Migrate).
package publishing

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/platform/fsutil"
)

// DefaultStatePath is where the record lives. It sits beside the other
// root-owned panel configuration rather than under /var/lib, because it is
// configuration an operator may legitimately read (0644) and only root may
// write.
const DefaultStatePath = "/etc/nexa-panel/publishing.json"

// DefaultVhostPath is the managed panel vhost, read only by the one-time
// migration for nodes that predate the record.
const DefaultVhostPath = "/etc/nginx/sites-available/nexa-panel.conf"

// currentVersion tags the on-disk shape. A record from a future version is
// refused rather than reinterpreted: publishing the panel on a guess is exactly
// what this package exists to stop.
const currentVersion = 1

// ErrNoRecord reports that no publishing record exists yet. Callers distinguish
// it from a read failure, because "never written" is a node to migrate and
// "unreadable" is a node to leave alone.
var ErrNoRecord = errors.New("no publishing record")

// State is the record. It describes the panel's own listener plus the one fact
// about the world in front of it that changes how the panel must be published.
type State struct {
	Version int `json:"version"`
	// Hostname is the server_name the panel answers on. "_" (any host) and
	// "localhost" are the two placeholder publications of a node that has not
	// been given a real name yet.
	Hostname string `json:"hostname"`
	// ListenAddress binds the listener. Empty means every interface; "127.0.0.1"
	// is the loopback quick-start an operator reaches over an SSH tunnel.
	ListenAddress string `json:"listenAddress,omitempty"`
	Port          int    `json:"port"`
	// TLS reports that the panel's own vhost terminates TLS on Port. It is the
	// panel's certificate (Certbot-managed in practice), so the vhost carries
	// certificate directives this package must never re-render.
	TLS bool `json:"tls"`
	// ExternalTLS reports that something in front of this node terminates TLS
	// and forwards plaintext to the panel. The panel is publicly encrypted while
	// its own listener is not, which is the one case a plaintext listener is not
	// a downgrade — and the one case inference could never tell apart from an
	// insecure install.
	ExternalTLS bool      `json:"externalTls"`
	UpdatedAt   time.Time `json:"updatedAt"`
	// Source records how this record came to exist: "install" when publishing
	// wrote it deliberately, "vhost-migration" when it was recovered once from a
	// node that predates the record. It is evidence for an operator reading the
	// file, not an input to any decision.
	Source string `json:"source,omitempty"`
}

// Mode names the publication for humans: log lines, CLI output, and runbooks.
func (s State) Mode() string {
	switch {
	case s.TLS:
		return "tls"
	case s.ExternalTLS:
		return "external-tls"
	case s.ListenAddress == "127.0.0.1" || s.ListenAddress == "::1":
		return "loopback"
	default:
		return "plaintext"
	}
}

// Encrypted reports whether a browser reaches this panel over HTTPS, by either
// route. It is the question every "is this node safe to publish" check is
// actually asking.
func (s State) Encrypted() bool { return s.TLS || s.ExternalTLS }

// ListenDirective renders the value of the nginx `listen` directive this state
// implies, in exactly the form the packaged template expects.
func (s State) ListenDirective() string {
	directive := strconv.Itoa(s.Port)
	if s.ListenAddress != "" {
		directive = s.ListenAddress + ":" + directive
	}
	if s.TLS {
		directive += " ssl"
	}
	return directive
}

// Validate refuses a record that cannot describe a real publication. It runs on
// both write and read: a hand-edited or truncated file must fail loudly rather
// than become a plausible-looking wrong answer.
func (s State) Validate() error {
	if s.Version != currentVersion {
		return fmt.Errorf("unsupported publishing record version %d (this build understands %d)", s.Version, currentVersion)
	}
	if strings.TrimSpace(s.Hostname) == "" {
		return errors.New("publishing hostname is required")
	}
	if strings.ContainsAny(s.Hostname, " \t;{}\n") {
		return fmt.Errorf("publishing hostname %q contains characters nginx would not accept in server_name", s.Hostname)
	}
	if s.Port < 1 || s.Port > 65535 {
		return fmt.Errorf("publishing port %d is out of range", s.Port)
	}
	if s.TLS && s.ExternalTLS {
		// Both cannot be true: TLS says this vhost holds the certificate,
		// ExternalTLS says something else does and this vhost is plaintext.
		return errors.New("a publication terminates TLS here or in front of this node, not both")
	}
	return nil
}

// DefaultPort is the port a publication uses when none is named. It is here
// rather than in the CLI because the installer and the panel must agree on it:
// a TLS publication that quietly defaulted to 80 would be unreachable.
func DefaultPort(tls bool) int {
	if tls {
		return 443
	}
	return 80
}

// Load reads the record. It returns ErrNoRecord when the file has never been
// written, so a caller can tell a fresh node from a broken one.
func Load(path string) (State, error) {
	content, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return State{}, ErrNoRecord
	}
	if err != nil {
		return State{}, fmt.Errorf("read publishing record: %w", err)
	}
	var state State
	if err := json.Unmarshal(content, &state); err != nil {
		return State{}, fmt.Errorf("decode publishing record %s: %w", path, err)
	}
	if err := state.Validate(); err != nil {
		return State{}, fmt.Errorf("publishing record %s: %w", path, err)
	}
	return state, nil
}

// Save writes the record atomically. A half-written publishing record is worse
// than none — the next read would refuse to parse it and the caller would have
// no idea how the node is published — so the content is fsynced, renamed into
// place, and the directory entry fsynced behind it.
func Save(path string, state State) error {
	if state.Version == 0 {
		state.Version = currentVersion
	}
	if state.UpdatedAt.IsZero() {
		state.UpdatedAt = time.Now()
	}
	state.UpdatedAt = state.UpdatedAt.UTC().Truncate(time.Second)
	if err := state.Validate(); err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode publishing record: %w", err)
	}
	encoded = append(encoded, '\n')

	// Readable by anyone, writable only by root: an operator reading how their
	// node is published needs no privilege, changing it needs all of it.
	if err := fsutil.Write(path, encoded, 0o644); err != nil {
		return fmt.Errorf("activate publishing record: %w", err)
	}
	return nil
}

var (
	listenPattern     = regexp.MustCompile(`^\s*listen\s+([^;]+);`)
	serverNamePattern = regexp.MustCompile(`^\s*server_name\s+([^;]+);`)
)

// InferFromVhost recovers a publishing state from a managed panel vhost. This is
// the whole of what used to be done on every lifecycle operation, kept for the
// single case it is defensible: a node installed before the record existed, read
// exactly once by Migrate and then never again.
//
// It reads the whole of the first server block, which is the panel's own: a
// Certbot-added `listen 80` redirect block is always generated after the TLS
// block it redirects to. Reading the block rather than the first matching line
// matters because that block holds several listeners — Certbot adds an IPv6 one
// beside the IPv4 one, and the managed template pairs them too — and taking
// whichever happened to be printed first would record the panel as bound to
// something it is not.
func InferFromVhost(source io.Reader) (State, error) {
	state := State{Version: currentVersion, Source: "vhost-migration"}
	scanner := bufio.NewScanner(source)
	var listens []string
	var serverName string
	depth, started := 0, false
	for scanner.Scan() {
		line := scanner.Text()
		if index := strings.IndexByte(line, '#'); index >= 0 {
			// Trailing "# managed by Certbot" comments are the exact noise that made
			// grepping this file unreliable; strip them before matching.
			line = line[:index]
		}
		if match := listenPattern.FindStringSubmatch(line); match != nil {
			listens = append(listens, strings.TrimSpace(match[1]))
		}
		if serverName == "" {
			if match := serverNamePattern.FindStringSubmatch(line); match != nil {
				serverName = strings.TrimSpace(match[1])
			}
		}
		depth += strings.Count(line, "{") - strings.Count(line, "}")
		if depth > 0 {
			started = true
		}
		if started && depth <= 0 {
			// End of the first server block; anything after it belongs to the
			// redirect block or another vhost.
			break
		}
	}
	if err := scanner.Err(); err != nil {
		return State{}, fmt.Errorf("read panel vhost: %w", err)
	}
	if len(listens) == 0 || serverName == "" {
		return State{}, errors.New("the panel vhost carries no listen/server_name pair to recover a publishing state from")
	}

	// TLS is a property of the block, not of one listener: if any listener in it
	// terminates TLS, the panel is published over TLS.
	primary := ""
	for _, listen := range listens {
		fields := strings.Fields(listen)
		for _, field := range fields[1:] {
			if field == "ssl" {
				state.TLS = true
			}
		}
		// The IPv4/hostless listener is the canonical one; an IPv6 literal
		// listener is the companion added beside it.
		if primary == "" || (strings.HasPrefix(primary, "[") && !strings.HasPrefix(fields[0], "[")) {
			primary = fields[0]
		}
	}
	address := primary
	// An IPv6 literal binds as [::1]:8888; splitting on the last colon keeps it
	// intact while still handling 127.0.0.1:8888 and a bare port.
	if index := strings.LastIndexByte(address, ':'); index >= 0 {
		state.ListenAddress = strings.Trim(address[:index], "[]")
		address = address[index+1:]
	}
	port, err := strconv.Atoi(address)
	if err != nil {
		return State{}, fmt.Errorf("the panel vhost listens on %q, which is not a port this record can describe", primary)
	}
	state.Port = port
	// server_name may carry aliases; the first is the canonical publication.
	state.Hostname = strings.Fields(serverName)[0]
	// A plaintext listener behind an external terminator is indistinguishable
	// from an insecure publication in the vhost alone — which is precisely why
	// inference cannot be the source of truth. A migrated node is recorded as
	// what its own vhost proves, and an operator running external TLS restates
	// it once with `nexa publishing set --external-tls`.
	if err := state.Validate(); err != nil {
		return State{}, err
	}
	return state, nil
}

// Migrate gives a node that predates the record one, from its live vhost, and
// returns it. It is a no-op that returns the existing record on every node that
// already has one, so it is safe to run on each install and update.
func Migrate(statePath, vhostPath string, now time.Time) (State, bool, error) {
	state, err := Load(statePath)
	if err == nil {
		return state, false, nil
	}
	if !errors.Is(err, ErrNoRecord) {
		return State{}, false, err
	}
	file, err := os.Open(vhostPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// The panel was never published on this node. There is nothing to
			// recover and nothing to guess.
			return State{}, false, ErrNoRecord
		}
		return State{}, false, fmt.Errorf("open panel vhost: %w", err)
	}
	defer file.Close()
	inferred, err := InferFromVhost(file)
	if err != nil {
		return State{}, false, err
	}
	inferred.UpdatedAt = now
	if err := Save(statePath, inferred); err != nil {
		return State{}, false, err
	}
	return inferred, true, nil
}
