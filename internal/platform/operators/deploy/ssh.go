// Package deploy is the privileged operator behind per-site deployment access.
// Its first surface is SSH: one OpenSSH drop-in per SSH-enabled site — a `Match
// User` block that undoes the SFTP jail's defaults, points sshd at a root-owned
// authorized-keys file, and refuses everything but public-key authentication —
// plus the login-shell flip on that account.
//
// It is a sibling of the sftp operator, not a replacement: the two write
// different files and neither reconciles the other's. They must never both be
// enabled for one site, because `nexa-access-<slug>.conf` sorts before
// `nexa-site-<slug>.conf` and sshd keeps the first setting of each keyword, so
// the SSH block would silently unchroot the SFTP jail. The control plane
// enforces the exclusion; ApplySSHAccess re-checks it on the node so a racing
// caller cannot slip past.
package deploy

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/nexa-panel/nexa-panel/internal/platform/naming"
)

// blobPattern is unpadded-or-padded base64 and nothing else. It exists so a
// key body can never carry whitespace, which is the only way a caller could
// smuggle an extra field into an authorized_keys line.
var blobPattern = regexp.MustCompile(`^[A-Za-z0-9+/]+={0,3}$`)

const (
	// SSHDDropInDir holds the per-site Match blocks. The node's sshd_config
	// Includes this directory (the installer guarantees the Include line).
	SSHDDropInDir = "/etc/ssh/sshd_config.d"
	// AuthorizedKeysDir is the root-owned tree the Match blocks point sshd at.
	// The keys deliberately live outside the site home: the site root is
	// root:root 0755 so it can double as an SFTP chroot, and a root-owned key
	// file also means a compromised site process cannot append its own key.
	AuthorizedKeysDir = "/etc/nexa-panel/generated/ssh"
	siteRootBase      = "/srv/nexa/sites"
	loginShell        = "/bin/bash"
	nologinShell      = "/usr/sbin/nologin"
	// maxBlobLength bounds a single key body. Real ed25519 and 4096-bit RSA keys
	// are orders of magnitude smaller; this only stops a caller from filling the
	// node's /etc with one request.
	maxBlobLength    = 16 * 1024
	maxCommentLength = 128
)

// keyAlgorithms is the allowlist of public-key types the panel will install.
// Anything outside it is refused rather than passed through, so an unknown
// token can never end up in the algorithm position of an authorized_keys line.
var keyAlgorithms = map[string]bool{
	"ssh-ed25519":                true,
	"ssh-rsa":                    true,
	"ecdsa-sha2-nistp256":        true,
	"ecdsa-sha2-nistp384":        true,
	"ecdsa-sha2-nistp521":        true,
	"sk-ssh-ed25519@openssh.com": true,
}

// SupportedKeyAlgorithm reports whether this operator will install a key of the
// given type. It is exported so the control plane can refuse an unsupported
// paste where it is pasted, instead of storing a key the node rejects later — on
// an unrelated apply that then fails as a whole.
func SupportedKeyAlgorithm(algorithm string) bool { return keyAlgorithms[algorithm] }

// AuthorizedKey is one installed public key. Comment is the operator-visible
// label; it is re-emitted verbatim as the trailing comment field.
type AuthorizedKey struct {
	Algorithm string `json:"algorithm"`
	Blob      string `json:"blob"`
	Comment   string `json:"comment"`
}

// SSHAccessRequest is the desired SSH state for a single site. The key list is
// the complete desired set: the node rewrites the authorized-keys file from it
// rather than merging, so a revoked key is gone after one Apply.
type SSHAccessRequest struct {
	Slug     string          `json:"slug"`
	UnixUser string          `json:"unixUser"`
	RootPath string          `json:"rootPath"`
	Enabled  bool            `json:"enabled"`
	Keys     []AuthorizedKey `json:"keys"`
}

// SSHAccessObservation reports the state the node settled into after Apply.
type SSHAccessObservation struct {
	Slug               string `json:"slug"`
	Enabled            bool   `json:"enabled"`
	Username           string `json:"username"`
	Shell              string `json:"shell"`
	DropInPath         string `json:"dropInPath"`
	AuthorizedKeysPath string `json:"authorizedKeysPath"`
	KeyCount           int    `json:"keyCount"`
}

// Operator is the node-side surface the control plane drives over the agent
// socket. SSHHostOperator is the real implementation; UnixClient is the proxy.
type Operator interface {
	ApplySSHAccess(ctx context.Context, request SSHAccessRequest) (SSHAccessObservation, error)
	// GenerateUserKey mints an ed25519 pair on the node and returns both halves
	// once. It installs nothing; the public half only takes effect when a later
	// ApplySSHAccess carries it.
	GenerateUserKey(ctx context.Context, request SSHAccessRequest) (GeneratedKey, error)
	// EnsureDeployKey settles the site's GitHub material on the node and reports
	// the public half. The private half stays on the node, so this is the only
	// thing the control plane ever learns about the key.
	EnsureDeployKey(ctx context.Context, request DeployKeyRequest) (DeployKeyObservation, error)
	// TestGitHub proves the deploy key can read a repository, as the site
	// account and against the pinned host keys.
	TestGitHub(ctx context.Context, request GitHubTestRequest) (GitHubTestResult, error)
	// ReadSharedEnv and WriteSharedEnv carry the deployer layout's shared
	// environment file. Both are synchronous — a job's request and result are
	// persisted, and this document is a credential store.
	ReadSharedEnv(ctx context.Context, request EnvRequest) (EnvDocument, error)
	WriteSharedEnv(ctx context.Context, request EnvRequest, content string) (EnvDocument, error)
	// Prepare verifies, and where it can installs, the tooling a deployer needs
	// on this node. It is the one call here that may run apt, so it is also the
	// one with a timeout measured in tens of minutes.
	Prepare(ctx context.Context, request PrepareRequest) (PrepareObservation, error)
	// FPMReloader grants and withdraws the site's narrow permission to reload
	// the PHP-FPM master that serves it. It is embedded rather than restated so
	// the agent, which holds an Operator, can route to it.
	FPMReloader
}

// validateIdentity re-derives an account and a site root from a slug exactly as
// the sites module would have, so the agent never grants a login to, rewrites
// the keys of, or runs a command as an attacker-chosen account even if a
// request reaches it from a compromised caller. Every request this operator
// accepts starts here.
func validateIdentity(slug, unixUser, rootPath string) error {
	if !naming.ValidSiteSlug(slug) {
		return errors.New("ssh access site slug is invalid")
	}
	if unixUser != naming.SiteUnixUser(slug) {
		return errors.New("ssh access unix user must be derived from the site slug")
	}
	if filepath.Clean(rootPath) != filepath.Join(siteRootBase, slug) {
		return errors.New("ssh access root path must be derived from the site slug")
	}
	return nil
}

// validate puts the request through the shared identity boundary and then the
// key checks, which are the second half of the same boundary: everything that
// reaches an authorized_keys line is constrained here, not at render time.
func (r SSHAccessRequest) validate() error {
	if err := validateIdentity(r.Slug, r.UnixUser, r.RootPath); err != nil {
		return err
	}
	for index, key := range r.Keys {
		if err := key.validate(); err != nil {
			return fmt.Errorf("ssh access key %d: %w", index+1, err)
		}
	}
	return nil
}

func (k AuthorizedKey) validate() error {
	if !keyAlgorithms[k.Algorithm] {
		return errors.New("public key algorithm is not supported")
	}
	if !blobPattern.MatchString(k.Blob) {
		return errors.New("public key body is not base64")
	}
	if len(k.Blob) > maxBlobLength {
		return errors.New("public key body is too long")
	}
	if len(k.Comment) > maxCommentLength {
		return errors.New("public key comment is too long")
	}
	if strings.ContainsAny(k.Comment, "\x00\r\n") {
		return errors.New("public key comment contains a control character")
	}
	// A leading dash would let a comment be read as an option list by a careless
	// reader of the file, and no legitimate comment starts with one.
	if strings.HasPrefix(k.Comment, "-") {
		return errors.New("public key comment must not start with a dash")
	}
	return nil
}

func (r SSHAccessRequest) dropInPath() string {
	return filepath.Join(SSHDDropInDir, "nexa-access-"+r.Slug+".conf")
}

func (r SSHAccessRequest) authorizedKeysPath() string {
	return filepath.Join(AuthorizedKeysDir, r.UnixUser, "authorized_keys")
}

// renderSSHDropIn builds the OpenSSH Match block that gives one site account an
// interactive login. Every keyword the SFTP jail sets is restated here, because
// this file sorts first and sshd keeps the first value it reads: leaving one out
// would let the jail's value apply to an SSH session. Forwarding stays off — the
// account exists to deploy code, not to be a tunnel endpoint.
func renderSSHDropIn(request SSHAccessRequest) string {
	var builder strings.Builder
	builder.WriteString("# Managed by Nexa Panel. Per-site SSH access — do not edit.\n")
	builder.WriteString("Match User " + request.UnixUser + "\n")
	builder.WriteString("    ChrootDirectory none\n")
	builder.WriteString("    ForceCommand none\n")
	builder.WriteString("    AuthorizedKeysFile " + request.authorizedKeysPath() + "\n")
	builder.WriteString("    PubkeyAuthentication yes\n")
	builder.WriteString("    PasswordAuthentication no\n")
	builder.WriteString("    KbdInteractiveAuthentication no\n")
	builder.WriteString("    AuthenticationMethods publickey\n")
	builder.WriteString("    AllowTcpForwarding no\n")
	builder.WriteString("    AllowAgentForwarding no\n")
	builder.WriteString("    X11Forwarding no\n")
	builder.WriteString("    PermitTunnel no\n")
	builder.WriteString("    PermitTTY yes\n")
	return builder.String()
}

// renderAuthorizedKeys emits one `<algorithm> <blob> <comment>` line per key and
// nothing else. The options field is never written: a caller string placed
// before the algorithm token would be parsed as options, and `command=` or
// `environment=` there is arbitrary code execution as the site account.
func renderAuthorizedKeys(keys []AuthorizedKey) string {
	var builder strings.Builder
	for _, key := range keys {
		builder.WriteString(key.Algorithm + " " + key.Blob)
		if key.Comment != "" {
			builder.WriteString(" " + key.Comment)
		}
		builder.WriteString("\n")
	}
	return builder.String()
}
