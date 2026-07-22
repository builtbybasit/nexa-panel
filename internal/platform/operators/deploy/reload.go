package deploy

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	// ReloadWrapperDir holds the per-site reload wrappers. It is root-owned and
	// 0711 (packaging/tmpfiles/nexa-panel.conf): a site account may traverse it
	// to reach its own wrapper and may not list, add to, or replace anything in
	// it.
	ReloadWrapperDir = "/etc/nexa-panel/generated/deploy"
	// SudoersDropInDir is sudo's include directory. Files whose name contains a
	// dot or ends in a tilde are ignored by sudo, which is what makes the
	// dot-prefixed temporary this package writes there invisible to it while it
	// is being validated.
	SudoersDropInDir = "/etc/sudoers.d"
	// systemctlPath and visudoPath are absolute because neither the wrapper's
	// PATH nor the agent's may decide which binary runs: one is invoked as root
	// by an unprivileged account, the other is the check that stands between a
	// rendered rule and a host-wide broken sudo.
	systemctlPath = "/usr/bin/systemctl"
	visudoPath    = "/usr/sbin/visudo"
	// reloadWrapperMode is 0555 — readable and executable by everyone, writable
	// by nobody at all, not even root without a chmod. The schedules operator's
	// wrapper is 0750 root:<unixUser> because cron runs it as the site account;
	// this one runs as root through sudo, so the site account must not be able
	// to edit it.
	reloadWrapperMode = os.FileMode(0o555)
	// sudoersMode is what sudo itself demands: 0440 root:root, or it refuses to
	// read the file and logs it as unsafe.
	sudoersMode = os.FileMode(0o440)
)

// phpBranchPattern gates the shape of the branch whose FPM master the wrapper
// reloads. It is restated rather than imported from the sites operator, which
// this package must not depend on, and it is the only thing standing between a
// request field and a command line: no space, slash, quote, semicolon, or
// newline can survive it.
var phpBranchPattern = regexp.MustCompile(`^[0-9]{1,2}\.[0-9]{1,2}$`)

// sudoersRulePattern is the shape the rendered rule must have before it is
// allowed anywhere near /etc/sudoers.d. Rendering is already fed from validated
// fields; this is the second, independent reading of the same bytes, because a
// sudoers file that grants more than it should is not a bug that fails loudly.
var sudoersRulePattern = regexp.MustCompile(`^nexa_[a-z][a-z0-9_]{1,31} ALL=\(root\) NOPASSWD: ` +
	regexp.QuoteMeta(ReloadWrapperDir) + `/nexa-fpm-reload-[a-z][a-z0-9-]{1,31}\.sh$`)

// FPMReloadRequest is the desired state of one site's reload grant. Enabled
// false is the teardown path: it removes both artifacts, so the sudoers surface
// on a node is exactly the set of sites still in deployer mode.
type FPMReloadRequest struct {
	Slug       string `json:"slug"`
	UnixUser   string `json:"unixUser"`
	RootPath   string `json:"rootPath"`
	PHPVersion string `json:"phpVersion"`
	Enabled    bool   `json:"enabled"`
}

// FPMReloadObservation reports the state the node settled into. Service names
// the unit the wrapper reloads, so the panel can be honest that the grant is
// branch-wide rather than pool-scoped.
type FPMReloadObservation struct {
	Slug        string `json:"slug"`
	Enabled     bool   `json:"enabled"`
	Username    string `json:"username"`
	Service     string `json:"service"`
	WrapperPath string `json:"wrapperPath"`
	SudoersPath string `json:"sudoersPath"`
}

// FPMReloader is the reload surface on its own. The prepare job drives it after
// a site is switched into deployer mode, and the teardown and mode-switch paths
// drive it again with Enabled false; both halves can be faked through it. It is
// embedded in Operator, so every implementation of that carries it too.
type FPMReloader interface {
	ApplyFPMReload(ctx context.Context, request FPMReloadRequest) (FPMReloadObservation, error)
}

func (r FPMReloadRequest) validate() error {
	if err := validateIdentity(r.Slug, r.UnixUser, r.RootPath); err != nil {
		return err
	}
	// A removal does not name a branch: the wrapper it deletes already carries
	// whichever one it was written for, and demanding a match would leave a
	// site whose branch moved unable to withdraw its own grant.
	if r.Enabled && !phpBranchPattern.MatchString(r.PHPVersion) {
		return errors.New("fpm reload php version is invalid")
	}
	return nil
}

func (r FPMReloadRequest) wrapperPath() string {
	return filepath.Join(ReloadWrapperDir, "nexa-fpm-reload-"+r.Slug+".sh")
}

// sudoersPath deliberately carries no extension: sudo skips every file in its
// include directory whose name contains a dot.
func (r FPMReloadRequest) sudoersPath() string {
	return filepath.Join(SudoersDropInDir, "nexa-deploy-"+r.Slug)
}

func (r FPMReloadRequest) service() string {
	return "php" + r.PHPVersion + "-fpm.service"
}

// reloadSystem is the privileged surface ApplyFPMReload drives. Like envSystem
// it is asserted from the operator's SSHNodeSystem rather than folded into it,
// so this surface can be faked on its own.
type reloadSystem interface {
	// WriteReloadWrapper publishes the root-owned 0555 script.
	WriteReloadWrapper(ctx context.Context, path, content string) error
	RemoveReloadWrapper(ctx context.Context, path string) error
	// InstallSudoers writes the rule to a temporary file sudo ignores, runs
	// `visudo -cf` against it, and only renames it into place once that
	// passes — a malformed drop-in breaks sudo for every account on the node,
	// including recovery, so the file must never exist unvalidated.
	InstallSudoers(ctx context.Context, path, content string) error
	RemoveSudoers(ctx context.Context, path string) error
}

var _ reloadSystem = (*SSHHostSystem)(nil)

// ApplyFPMReload settles the site's ability to reload the PHP-FPM master that
// serves it: one fixed, argument-less, root-owned wrapper and one sudoers rule
// naming exactly that path. Nothing a caller sends reaches either file's
// content except through validateIdentity and phpBranchPattern.
func (o *SSHHostOperator) ApplyFPMReload(ctx context.Context, request FPMReloadRequest) (FPMReloadObservation, error) {
	if err := request.validate(); err != nil {
		return FPMReloadObservation{}, err
	}
	system, err := o.reloads()
	if err != nil {
		return FPMReloadObservation{}, err
	}
	if request.Enabled {
		return o.grantReload(ctx, system, request)
	}
	return o.revokeReload(ctx, system, request)
}

func (o *SSHHostOperator) grantReload(ctx context.Context, system reloadSystem, request FPMReloadRequest) (FPMReloadObservation, error) {
	wrapper, sudoers := request.wrapperPath(), request.sudoersPath()
	rule := renderSudoersRule(request)
	// Read the rendered bytes back as bytes, independently of the fields they
	// came from, before they are allowed to become a privilege grant.
	if err := validateSudoersDocument(rule); err != nil {
		return FPMReloadObservation{}, err
	}
	// The wrapper first: a rule naming a path that does not exist yet grants
	// nothing, whereas a wrapper with no rule is an unreachable script.
	if err := system.WriteReloadWrapper(ctx, wrapper, renderReloadWrapper(request)); err != nil {
		return FPMReloadObservation{}, fmt.Errorf("write FPM reload wrapper: %w", err)
	}
	if err := system.InstallSudoers(ctx, sudoers, rule); err != nil {
		_ = system.RemoveReloadWrapper(context.WithoutCancel(ctx), wrapper)
		return FPMReloadObservation{}, fmt.Errorf("install sudo rule: %w", err)
	}
	return request.observation(true, wrapper, sudoers), nil
}

func (o *SSHHostOperator) revokeReload(ctx context.Context, system reloadSystem, request FPMReloadRequest) (FPMReloadObservation, error) {
	wrapper, sudoers := request.wrapperPath(), request.sudoersPath()
	// The rule first, and the wrapper only once it is gone: the reverse order
	// would leave a live grant naming a path root could recreate.
	if err := system.RemoveSudoers(ctx, sudoers); err != nil {
		return FPMReloadObservation{}, fmt.Errorf("remove sudo rule: %w", err)
	}
	if err := system.RemoveReloadWrapper(ctx, wrapper); err != nil {
		return FPMReloadObservation{}, fmt.Errorf("remove FPM reload wrapper: %w", err)
	}
	return request.observation(false, wrapper, sudoers), nil
}

func (r FPMReloadRequest) observation(enabled bool, wrapper, sudoers string) FPMReloadObservation {
	observation := FPMReloadObservation{
		Slug:        r.Slug,
		Enabled:     enabled,
		Username:    r.UnixUser,
		WrapperPath: wrapper,
		SudoersPath: sudoers,
	}
	if enabled {
		observation.Service = r.service()
	}
	return observation
}

// reloads narrows the operator's node system to the reload surface, exactly as
// envs() does for the shared-environment surface.
func (o *SSHHostOperator) reloads() (reloadSystem, error) {
	system, ok := o.system.(reloadSystem)
	if !ok {
		return nil, errors.New("this node system does not implement the FPM reload grant")
	}
	return system, nil
}

// renderReloadWrapper builds the whole of the privileged command. It takes no
// arguments and passes none: `sudo <wrapper>` with anything appended still runs
// this exact systemctl invocation, because the script never reads "$@".
//
// PHP-FPM has no per-pool reload, so this reloads every pool on the branch. The
// grant is narrow in the sense that matters — one fixed command, no arguments,
// no wildcard — but it is not pool-scoped; per-site FPM units are the recorded
// follow-up.
func renderReloadWrapper(request FPMReloadRequest) string {
	return "#!/bin/sh\n" +
		"# Managed by Nexa Panel. Reloads the PHP-FPM master serving " + request.Slug + " — do not edit.\n" +
		"set -eu\n" +
		"exec " + systemctlPath + " reload " + request.service() + "\n"
}

// renderSudoersRule builds the drop-in: one comment and one rule. A single
// absolute path, no wildcard, no ALL command, no environment passthrough, and
// no RunAs group — the account may run this one file as root and nothing else.
func renderSudoersRule(request FPMReloadRequest) string {
	return "# Managed by Nexa Panel. Deployer FPM reload for site " + request.Slug + " — do not edit.\n" +
		request.UnixUser + " ALL=(root) NOPASSWD: " + request.wrapperPath() + "\n"
}

// validateSudoersDocument re-reads a rendered drop-in as text: exactly one
// comment line and one rule matching sudoersRulePattern, nothing else. It exists
// so that a future edit to renderSudoersRule cannot widen a grant quietly — a
// continuation line, a second rule, or a stray `ALL` fails here rather than on
// somebody's node.
func validateSudoersDocument(content string) error {
	if !strings.HasSuffix(content, "\n") {
		return errors.New("sudo rule is not newline terminated")
	}
	lines := strings.Split(strings.TrimSuffix(content, "\n"), "\n")
	if len(lines) != 2 {
		return errors.New("sudo rule must be one comment and one rule")
	}
	if !strings.HasPrefix(lines[0], "# Managed by Nexa Panel.") || strings.ContainsAny(lines[0], "\\\r\t") {
		return errors.New("sudo rule header is not the managed comment")
	}
	if !sudoersRulePattern.MatchString(lines[1]) {
		return errors.New("sudo rule does not name exactly one managed wrapper")
	}
	return nil
}

// WriteReloadWrapper publishes the wrapper root-owned and unwritable. The
// directory is created here as well as by tmpfiles, because the first
// deployer-mode site on a node may arrive before a tmpfiles run.
func (s *SSHHostSystem) WriteReloadWrapper(_ context.Context, path, content string) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o711); err != nil {
		return err
	}
	if err := os.Chmod(directory, 0o711); err != nil {
		return err
	}
	if err := chownPath(directory, 0, 0); err != nil {
		return err
	}
	if err := atomicWrite(path, []byte(content), reloadWrapperMode); err != nil {
		return err
	}
	// atomicWrite creates the temporary as the calling user; on the node that
	// is root already, and the explicit chown keeps it true if it ever is not.
	return chownPath(path, 0, 0)
}

func (s *SSHHostSystem) RemoveReloadWrapper(_ context.Context, path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// InstallSudoers is the install-validate-rollback discipline the sshd drop-in
// uses, tightened by one step: the file is validated while it is still under a
// name sudo ignores, so an invalid rule is never readable by sudo even for the
// instant between write and check.
func (s *SSHHostSystem) InstallSudoers(ctx context.Context, path, content string) error {
	directory := filepath.Dir(path)
	// The include directory is deliberately not created here. It belongs to the
	// sudo package, which owns its 0755 root:root permissions; creating it
	// ourselves on a node without sudo produced a root:nexa 0600 directory that
	// sudo would never have accepted, and left that debris behind after the
	// grant failed at visudo anyway. Absent directory means absent package, and
	// the actionable error says so rather than surfacing a bare fork/exec.
	if info, err := os.Stat(directory); err != nil || !info.IsDir() {
		return fmt.Errorf("sudo is not installed on this node (%s is missing): run the node preparation job first", directory)
	}
	if _, err := os.Stat(visudoPath); err != nil {
		return fmt.Errorf("sudo is not installed on this node (%s is missing): run the node preparation job first", visudoPath)
	}
	// The dot prefix and the ".tmp" suffix each independently put this name in
	// the set sudo's include directory skips.
	temporary, err := os.CreateTemp(directory, ".nexa-deploy-*.tmp")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer os.Remove(name)
	if _, err := temporary.WriteString(content); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Chmod(sudoersMode); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := chownPath(name, 0, 0); err != nil {
		return err
	}
	if output, err := s.command(ctx, visudoPath, "-cf", name); err != nil {
		return commandError(output, err)
	}
	return os.Rename(name, path)
}

func (s *SSHHostSystem) RemoveSudoers(_ context.Context, path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// chownPath is indirected so the host-system tests can exercise the publish
// path without being root. The node agent always runs as root, where it is
// os.Chown and nothing else.
var chownPath = os.Chown

func (c *UnixClient) ApplyFPMReload(ctx context.Context, request FPMReloadRequest) (FPMReloadObservation, error) {
	var observation FPMReloadObservation
	err := c.client.JSON(ctx, http.MethodPost, "/v1/deploy/fpm-reload/apply", request, &observation)
	return observation, err
}
