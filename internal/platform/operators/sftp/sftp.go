// Package sftp is the privileged operator behind optional per-site SFTP access.
// It manages one OpenSSH drop-in per SFTP-enabled site — a `Match User` block
// that chroots the site account into its own root and forces the in-process
// sftp subsystem — plus that account's password in /etc/shadow.
//
// It is deliberately independent of the site activation plan. Enabling SFTP
// never touches Nginx or PHP-FPM, and a certificate renewal never disturbs
// SFTP, because neither operator reconciles the other's artifacts. The one
// shared contract is the site root's ownership: the sites operator provisions it
// as root:root 0755 precisely so it can double as a ChrootDirectory here.
package sftp

import (
	"context"
	"errors"
	"path/filepath"
	"regexp"
	"strings"
)

var slugPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{1,31}$`)

// hashPattern accepts a crypt(3) settings string: `$id$...` built from the
// crypt base64 alphabet. It exists so the value fed to `chpasswd -e` can never
// smuggle a colon or newline into the user:hash stdin line, and so a plaintext
// password can never be mistaken for a hash and land in /etc/shadow verbatim.
var hashPattern = regexp.MustCompile(`^\$[a-zA-Z0-9./$=,-]{1,200}$`)

const (
	// SSHDDropInDir holds the per-site Match blocks. The node's sshd_config
	// Includes this directory (the installer guarantees the Include line).
	SSHDDropInDir = "/etc/ssh/sshd_config.d"
	siteRootBase  = "/srv/nexa/sites"
	// minPasswordLength is a floor; the control plane generates far longer
	// secrets. It only guards against a caller supplying something trivial.
	minPasswordLength = 12
)

// Request is the desired SFTP state for a single site. Password is never
// persisted by the control plane: it travels this struct to the node and is
// written straight into /etc/shadow via chpasswd. An empty Password with Enabled
// true reconciles the configuration without rotating the existing password.
//
// PasswordHash is the alternative credential form for the staged-at-creation
// flow: the control plane holds only a crypt(3) hash until activation, and this
// carries that hash to the node, where chpasswd -e installs it verbatim. At
// most one of Password and PasswordHash may be set.
type Request struct {
	Slug         string `json:"slug"`
	UnixUser     string `json:"unixUser"`
	RootPath     string `json:"rootPath"`
	Enabled      bool   `json:"enabled"`
	Password     string `json:"password,omitempty"`
	PasswordHash string `json:"passwordHash,omitempty"`
}

// Observation reports the state the node settled into after Apply.
type Observation struct {
	Slug        string `json:"slug"`
	Enabled     bool   `json:"enabled"`
	Username    string `json:"username"`
	PasswordSet bool   `json:"passwordSet"`
	DropInPath  string `json:"dropInPath"`
}

// Operator is the node-side surface the control plane drives over the agent
// socket. HostOperator is the real implementation; UnixClient is the proxy.
type Operator interface {
	Apply(ctx context.Context, request Request) (Observation, error)
}

// validate re-derives the request's identity from its slug exactly as the sites
// module would have, so the agent never chroots into or repoints an
// attacker-chosen path even if a request reaches it from a compromised caller.
func (r Request) validate() error {
	if !slugPattern.MatchString(r.Slug) {
		return errors.New("sftp site slug is invalid")
	}
	if r.UnixUser != "nexa_"+strings.ReplaceAll(r.Slug, "-", "_") {
		return errors.New("sftp unix user must be derived from the site slug")
	}
	if filepath.Clean(r.RootPath) != filepath.Join(siteRootBase, r.Slug) {
		return errors.New("sftp root path must be derived from the site slug")
	}
	if r.Enabled && r.Password != "" && len(r.Password) < minPasswordLength {
		return errors.New("sftp password is too short")
	}
	if r.Password != "" && r.PasswordHash != "" {
		return errors.New("sftp request must not carry both a password and a password hash")
	}
	if r.PasswordHash != "" && !hashPattern.MatchString(r.PasswordHash) {
		return errors.New("sftp password hash is not a crypt-format hash")
	}
	return nil
}

func (r Request) dropInPath() string {
	return filepath.Join(SSHDDropInDir, "nexa-site-"+r.Slug+".conf")
}

// renderDropIn builds the OpenSSH Match block that jails one site account.
// PasswordAuthentication is scoped inside the block so enabling password SFTP
// for a site never re-enables password logins for the box as a whole. The
// nologin shell on the account keeps interactive SSH refused; ForceCommand
// internal-sftp is served in-process by sshd and so still works.
func renderDropIn(request Request) string {
	var builder strings.Builder
	builder.WriteString("# Managed by Nexa Panel. Per-site SFTP jail — do not edit.\n")
	builder.WriteString("Match User " + request.UnixUser + "\n")
	builder.WriteString("    ChrootDirectory " + request.RootPath + "\n")
	builder.WriteString("    ForceCommand internal-sftp -d /public\n")
	builder.WriteString("    PasswordAuthentication yes\n")
	builder.WriteString("    AuthenticationMethods password\n")
	builder.WriteString("    AllowTcpForwarding no\n")
	builder.WriteString("    AllowAgentForwarding no\n")
	builder.WriteString("    X11Forwarding no\n")
	builder.WriteString("    PermitTunnel no\n")
	return builder.String()
}
