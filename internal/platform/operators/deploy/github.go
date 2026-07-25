package deploy

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	// deployKeyName is the only key file the panel manages inside a site's
	// .ssh directory. It is a fixed name so nothing a caller sends can decide
	// which file under the site root gets overwritten by a rotation.
	deployKeyName  = "id_ed25519"
	knownHostsName = "known_hosts"
	sshConfigName  = "config"
	// githubProbeTimeout bounds each network probe on its own, so a forge that
	// accepts the connection and then stalls cannot hold an agent request open.
	githubProbeTimeout = 30 * time.Second
	// probeOutputTailBytes bounds how much command output crosses the agent
	// boundary, matching the scheduled-task run cap
	// (operators/schedules/host.go:23-33).
	probeOutputTailBytes = 64 * 1024
	// probePath is the whole environment the probes get besides HOME. runuser
	// keeps the caller's environment, and the agent's is not one a site
	// account should inherit.
	probePath = "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
)

var (
	// repositoryPattern accepts the SSH remote form and nothing else. An
	// https:// remote would authenticate with a token or anonymously and so
	// would prove nothing about the deploy key, which is the only thing this
	// test exists to prove.
	repositoryPattern = regexp.MustCompile(`^git@github\.com:[A-Za-z0-9._-]{1,64}/[A-Za-z0-9._-]{1,100}(\.git)?$`)
	// authenticatedPattern matches GitHub's greeting. For a deploy key the
	// name is `owner/repository`, not a user, which is why the slash is in the
	// class; nothing outside the class is echoed back to the panel.
	authenticatedPattern = regexp.MustCompile(`Hi ([A-Za-z0-9._/-]{1,120})! You've successfully authenticated`)
)

// DeployKeyRequest asks the node to make sure a site has a deploy key. Rotate
// mints a new pair over the existing one; without it an existing key is left
// alone and only read back, so a repeated ensure is idempotent and cannot
// invalidate a key GitHub already trusts.
type DeployKeyRequest struct {
	Slug     string `json:"slug"`
	UnixUser string `json:"unixUser"`
	RootPath string `json:"rootPath"`
	Rotate   bool   `json:"rotate"`
}

// DeployKeyObservation is everything the control panel is allowed to learn
// about a deploy key: the public half, its fingerprint, and where the private
// half lives. The private half is never in this value, never in a job payload,
// and never in control.db — a leaked backup therefore leaks no repository
// access, and there is no endpoint that could reveal it later.
type DeployKeyObservation struct {
	Slug        string    `json:"slug"`
	Algorithm   string    `json:"algorithm"`
	PublicKey   string    `json:"publicKey"`
	Fingerprint string    `json:"fingerprint"`
	Path        string    `json:"path"`
	KnownHosts  bool      `json:"knownHosts"`
	CreatedAt   time.Time `json:"createdAt"`
}

// GitHubTestRequest names the repository the site's deploy key should be able
// to read. The identity fields carry the same derivation the SSH surface uses.
type GitHubTestRequest struct {
	Slug       string `json:"slug"`
	UnixUser   string `json:"unixUser"`
	RootPath   string `json:"rootPath"`
	Repository string `json:"repository"`
}

// GitHubTestResult is the verdict of the two probes. A failed probe is a
// result, not an error: the operator wants to see the tail and the exit
// status, and only a probe that could not be started at all is an error.
type GitHubTestResult struct {
	AuthOK     bool   `json:"authOk"`
	Account    string `json:"account"`
	LsRemoteOK bool   `json:"lsRemoteOk"`
	RefCount   int    `json:"refCount"`
	OutputTail string `json:"outputTail"`
}

// deployKeySystem is the privileged surface the deploy-key methods drive. It is
// asserted from the operator's SSHNodeSystem instead of being folded into it so
// the SSH-access surface and this one can be faked independently.
type deployKeySystem interface {
	// StatSiteSSHFile reports a file's modification time and whether it is
	// there at all, so an ensure can tell "generate" from "read back".
	StatSiteSSHFile(ctx context.Context, rootPath, name string) (time.Time, bool, error)
	// WriteSiteSSHFile publishes one file inside {root}/.ssh owned by the site
	// account. Only the fixed managed names are ever passed as name.
	WriteSiteSSHFile(ctx context.Context, rootPath, unixUser, name, content string, mode os.FileMode) error
	ReadSiteSSHFile(ctx context.Context, rootPath, name string) (string, error)
	// GenerateSiteKey mints an ed25519 pair straight into {root}/.ssh. The
	// private half never leaves the node system: no argument and no return
	// value of this interface carries it.
	GenerateSiteKey(ctx context.Context, rootPath, unixUser, name, comment string) error
	// RunAsSite runs one command de-escalated to the site account and reports
	// its exit code. A non-nil error means the command could not be started;
	// a command that ran and failed reports its status in the exit code.
	RunAsSite(ctx context.Context, unixUser, home string, argv []string) (int, []byte, error)
}

// The real node system must satisfy both halves of the split surface. A Go
// interface cannot be declared across two files, so deployKeySystem is asserted
// from SSHNodeSystem at run time in deployKeys(); these assertions make the
// build, rather than a deploy, the thing that catches a missing method.
var (
	_ SSHNodeSystem   = (*SSHHostSystem)(nil)
	_ deployKeySystem = (*SSHHostSystem)(nil)
)

func (r DeployKeyRequest) validate() error {
	return validateIdentity(r.Slug, r.UnixUser, r.RootPath)
}

func (r GitHubTestRequest) validate() error {
	if err := validateIdentity(r.Slug, r.UnixUser, r.RootPath); err != nil {
		return err
	}
	if !repositoryPattern.MatchString(r.Repository) {
		return errors.New("repository must be an SSH GitHub remote such as git@github.com:owner/name.git")
	}
	return nil
}

func (r DeployKeyRequest) keyPath() string {
	return filepath.Join(r.RootPath, ".ssh", deployKeyName)
}

// EnsureDeployKey settles a site's GitHub material: the pinned host keys, the
// client config that pins them, and the key pair itself. It returns the public
// half only — the private half stays in {root}/.ssh, readable by the site
// account and root and by nothing else.
func (o *SSHHostOperator) EnsureDeployKey(ctx context.Context, request DeployKeyRequest) (DeployKeyObservation, error) {
	if err := request.validate(); err != nil {
		return DeployKeyObservation{}, err
	}
	system, err := o.deployKeys()
	if err != nil {
		return DeployKeyObservation{}, err
	}
	if err := o.system.EnsureHomeSSHDir(ctx, request.RootPath, request.UnixUser); err != nil {
		return DeployKeyObservation{}, fmt.Errorf("prepare site SSH directory: %w", err)
	}
	// The host keys are written before the key pair, so a key that exists is
	// always a key the site can already use against a verified GitHub.
	hosts, err := renderKnownHosts()
	if err != nil {
		return DeployKeyObservation{}, fmt.Errorf("render GitHub host keys: %w", err)
	}
	if err := system.WriteSiteSSHFile(ctx, request.RootPath, request.UnixUser, knownHostsName, hosts, 0o644); err != nil {
		return DeployKeyObservation{}, fmt.Errorf("write GitHub host keys: %w", err)
	}
	if err := system.WriteSiteSSHFile(ctx, request.RootPath, request.UnixUser, sshConfigName, renderSSHClientConfig(request.RootPath), 0o600); err != nil {
		return DeployKeyObservation{}, fmt.Errorf("write SSH client configuration: %w", err)
	}
	createdAt, present, err := system.StatSiteSSHFile(ctx, request.RootPath, deployKeyName)
	if err != nil {
		return DeployKeyObservation{}, fmt.Errorf("inspect deploy key: %w", err)
	}
	if !present || request.Rotate {
		if err := system.GenerateSiteKey(ctx, request.RootPath, request.UnixUser, deployKeyName, "nexa-"+request.Slug+"-deploy"); err != nil {
			return DeployKeyObservation{}, fmt.Errorf("generate deploy key: %w", err)
		}
		if createdAt, _, err = system.StatSiteSSHFile(ctx, request.RootPath, deployKeyName); err != nil {
			return DeployKeyObservation{}, fmt.Errorf("inspect deploy key: %w", err)
		}
	}
	public, err := system.ReadSiteSSHFile(ctx, request.RootPath, deployKeyName+".pub")
	if err != nil {
		return DeployKeyObservation{}, fmt.Errorf("read deploy public key: %w", err)
	}
	key, err := parsePublicKey(public)
	if err != nil {
		return DeployKeyObservation{}, err
	}
	fingerprint, err := publicKeyFingerprint(key.Blob)
	if err != nil {
		return DeployKeyObservation{}, err
	}
	return DeployKeyObservation{
		Slug:        request.Slug,
		Algorithm:   key.Algorithm,
		PublicKey:   key.Algorithm + " " + key.Blob,
		Fingerprint: fingerprint,
		Path:        request.keyPath(),
		KnownHosts:  true,
		CreatedAt:   createdAt.UTC(),
	}, nil
}

// TestGitHub proves the deploy key works, from the site account and with the
// pinned host keys, by doing exactly what a deployment would do: authenticate,
// then list the repository's branches.
func (o *SSHHostOperator) TestGitHub(ctx context.Context, request GitHubTestRequest) (GitHubTestResult, error) {
	if err := request.validate(); err != nil {
		return GitHubTestResult{}, err
	}
	system, err := o.deployKeys()
	if err != nil {
		return GitHubTestResult{}, err
	}
	var transcript strings.Builder
	// StrictHostKeyChecking=yes is the whole point of the pinned known_hosts:
	// without it an unknown host key would be accepted and the test would pass
	// against whatever answered. BatchMode keeps a prompt from hanging.
	exitCode, output, err := o.probe(ctx, system, request, &transcript, []string{"ssh", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes", "-T", "git@github.com"})
	if err != nil {
		return GitHubTestResult{}, fmt.Errorf("probe GitHub over SSH: %w", err)
	}
	result := GitHubTestResult{}
	// GitHub refuses the shell and exits 1 on success; 255 is ssh's own
	// transport or authentication failure, so the greeting is what decides.
	if match := authenticatedPattern.FindStringSubmatch(output); exitCode != 255 && match != nil {
		result.AuthOK = true
		result.Account = match[1]
	}
	if result.AuthOK {
		exitCode, output, err = o.probe(ctx, system, request, &transcript, []string{"git", "ls-remote", "--heads", request.Repository})
		if err != nil {
			return GitHubTestResult{}, fmt.Errorf("list repository branches: %w", err)
		}
		if exitCode == 0 {
			result.LsRemoteOK = true
			result.RefCount = countRefs(output)
		}
	}
	result.OutputTail = tailBytes(transcript.String(), probeOutputTailBytes)
	return result, nil
}

// probe runs one command as the site account and appends it, with its output,
// to the transcript the caller returns to the panel.
func (o *SSHHostOperator) probe(ctx context.Context, system deployKeySystem, request GitHubTestRequest, transcript *strings.Builder, argv []string) (int, string, error) {
	probeCtx, cancel := context.WithTimeout(ctx, githubProbeTimeout)
	defer cancel()
	exitCode, output, err := system.RunAsSite(probeCtx, request.UnixUser, request.RootPath, argv)
	transcript.WriteString("$ " + strings.Join(argv, " ") + "\n")
	transcript.Write(output)
	if err != nil {
		return exitCode, string(output), err
	}
	transcript.WriteString("\n[exit " + strconv.Itoa(exitCode) + "]\n")
	return exitCode, string(output), nil
}

// deployKeys narrows the operator's node system to the deploy-key surface. The
// real system implements both; a fake that does not is a programming error, so
// the failure is reported rather than panicked on.
func (o *SSHHostOperator) deployKeys() (deployKeySystem, error) {
	system, ok := o.system.(deployKeySystem)
	if !ok {
		return nil, errors.New("this node system does not implement deploy keys")
	}
	return system, nil
}

// renderSSHClientConfig pins github.com to the site's own key and to the
// known_hosts written beside it. CheckHostIP is off deliberately: GitHub's
// addresses rotate, and an IP entry that no longer matches would be reported
// as a host-key failure on an otherwise healthy deploy.
func renderSSHClientConfig(rootPath string) string {
	sshDir := filepath.Join(rootPath, ".ssh")
	var builder strings.Builder
	builder.WriteString("# Managed by Nexa Panel. Per-site deploy key — do not edit.\n")
	builder.WriteString("Host github.com\n")
	builder.WriteString("    HostName github.com\n")
	builder.WriteString("    User git\n")
	builder.WriteString("    IdentityFile " + filepath.Join(sshDir, deployKeyName) + "\n")
	builder.WriteString("    IdentitiesOnly yes\n")
	builder.WriteString("    UserKnownHostsFile " + filepath.Join(sshDir, knownHostsName) + "\n")
	builder.WriteString("    StrictHostKeyChecking yes\n")
	builder.WriteString("    CheckHostIP no\n")
	builder.WriteString("    BatchMode yes\n")
	return builder.String()
}

// parsePublicKey reads back the two fields of a `.pub` file and puts them
// through the same allowlist an installed key faces, so a hand-planted file in
// a site's .ssh cannot smuggle a shape the panel would later re-emit.
func parsePublicKey(content string) (AuthorizedKey, error) {
	fields := strings.Fields(strings.TrimSpace(content))
	if len(fields) < 2 {
		return AuthorizedKey{}, errors.New("deploy public key is malformed")
	}
	key := AuthorizedKey{Algorithm: fields[0], Blob: fields[1]}
	if err := key.validate(); err != nil {
		return AuthorizedKey{}, fmt.Errorf("deploy public key: %w", err)
	}
	return key, nil
}

// publicKeyFingerprint computes the `SHA256:<base64>` form ssh-keygen -l
// prints, so the panel and the GitHub UI show the same string.
func publicKeyFingerprint(blob string) (string, error) {
	decoded, err := base64.StdEncoding.DecodeString(blob)
	if err != nil {
		return "", errors.New("deploy public key body is not base64")
	}
	sum := sha256.Sum256(decoded)
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:]), nil
}

// countRefs counts `<sha>\t<ref>` lines. Anything else ls-remote printed —
// warnings, redirects — is not a branch and must not inflate the count.
func countRefs(output string) int {
	count := 0
	for _, line := range strings.Split(output, "\n") {
		if strings.Contains(strings.TrimSpace(line), "\trefs/") {
			count++
		}
	}
	return count
}

func tailBytes(output string, limit int) string {
	if len(output) <= limit {
		return output
	}
	return output[len(output)-limit:]
}

// StatSiteSSHFile reports when {root}/.ssh/{name} was created, and whether it
// exists at all.
//
// It, ReadSiteSSHFile, WriteSiteSSHFile, and GenerateSiteKey all reach
// {root}/.ssh through nested os.Roots, so a symlink planted anywhere under the
// site root cannot redirect a write, a chown, or a read of the private key at a
// path outside it.
func (s *SSHHostSystem) StatSiteSSHFile(_ context.Context, rootPath, name string) (time.Time, bool, error) {
	directory, err := openSiteSSHDir(rootPath)
	if err != nil {
		return time.Time{}, false, err
	}
	defer directory.Close()
	info, err := directory.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, err
	}
	if !info.Mode().IsRegular() {
		return time.Time{}, false, fmt.Errorf("%s is not a managed file", name)
	}
	return info.ModTime(), true, nil
}

func (s *SSHHostSystem) ReadSiteSSHFile(_ context.Context, rootPath, name string) (string, error) {
	directory, err := openSiteSSHDir(rootPath)
	if err != nil {
		return "", err
	}
	defer directory.Close()
	content, err := directory.ReadFile(name)
	if err != nil {
		return "", err
	}
	return string(content), nil
}

func (s *SSHHostSystem) WriteSiteSSHFile(_ context.Context, rootPath, unixUser, name, content string, mode os.FileMode) error {
	uid, gid, err := s.accountIDs(unixUser)
	if err != nil {
		return err
	}
	directory, err := openSiteSSHDir(rootPath)
	if err != nil {
		return err
	}
	defer directory.Close()
	return publishOwnedFile(directory, name, []byte(content), mode, uid, gid)
}

// GenerateSiteKey mints the pair in the agent's PrivateTmp and publishes both
// halves by rename, so a rotation is never observed half-written: a deploy that
// starts mid-rotation either uses the old key or the new one.
func (s *SSHHostSystem) GenerateSiteKey(ctx context.Context, rootPath, unixUser, name, comment string) error {
	uid, gid, err := s.accountIDs(unixUser)
	if err != nil {
		return err
	}
	generated, err := s.GenerateKeyPair(ctx, comment)
	if err != nil {
		return err
	}
	directory, err := openSiteSSHDir(rootPath)
	if err != nil {
		return err
	}
	defer directory.Close()
	if err := publishOwnedFile(directory, name, []byte(generated.PrivateKey), 0o600, uid, gid); err != nil {
		return err
	}
	return publishOwnedFile(directory, name+".pub", []byte(generated.PublicKey+"\n"), 0o644, uid, gid)
}

func (s *SSHHostSystem) RunAsSite(ctx context.Context, unixUser, home string, argv []string) (int, []byte, error) {
	// env(1) carries HOME through runuser, which otherwise resets it from the
	// account's passwd entry, and GIT_TERMINAL_PROMPT=0 turns a credential
	// prompt into an immediate failure instead of a hung probe.
	args := append([]string{"-u", unixUser, "--", "env", "HOME=" + home, "GIT_TERMINAL_PROMPT=0", probePath}, argv...)
	output, err := s.command(ctx, "runuser", args...)
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		return exitError.ExitCode(), output, nil
	}
	if err != nil {
		return -1, output, commandError(output, err)
	}
	return 0, output, nil
}

func (s *SSHHostSystem) accountIDs(unixUser string) (int, int, error) {
	account, err := s.lookupUser(unixUser)
	if err != nil {
		return 0, 0, fmt.Errorf("look up site account: %w", err)
	}
	uid, err := strconv.Atoi(account.Uid)
	if err != nil || uid < 0 {
		return 0, 0, errors.New("site account returned an invalid UID")
	}
	gid, err := strconv.Atoi(account.Gid)
	if err != nil || gid < 0 {
		return 0, 0, errors.New("site account returned an invalid GID")
	}
	return uid, gid, nil
}

func openSiteSSHDir(rootPath string) (*os.Root, error) {
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, fmt.Errorf("open site root: %w", err)
	}
	defer root.Close()
	directory, err := root.OpenRoot(".ssh")
	if err != nil {
		return nil, fmt.Errorf("open site SSH directory: %w", err)
	}
	return directory, nil
}

// publishOwnedFile writes a sibling temp file, gives it its final mode and
// owner, and renames it into place, so a reader never sees a partial file and
// never sees one that is briefly root-owned or briefly world-readable. It is
// the package's one publisher: the deploy key, the pinned host keys, and the
// shared .env all reach the node through it.
func publishOwnedFile(directory *os.Root, name string, content []byte, mode os.FileMode, uid, gid int) error {
	temporary := "." + name + ".new"
	file, err := directory.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|os.O_EXCL, mode)
	if errors.Is(err, os.ErrExist) {
		if err = directory.Remove(temporary); err != nil {
			return err
		}
		file, err = directory.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|os.O_EXCL, mode)
	}
	if err != nil {
		return err
	}
	defer directory.Remove(temporary)
	if _, err := file.Write(content); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Chmod(mode); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Chown(uid, gid); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return directory.Rename(temporary, name)
}

func (c *UnixClient) EnsureDeployKey(ctx context.Context, request DeployKeyRequest) (DeployKeyObservation, error) {
	var observation DeployKeyObservation
	err := c.client.JSON(ctx, http.MethodPost, "/v1/deploy/key/ensure", request, &observation)
	return observation, err
}

func (c *UnixClient) TestGitHub(ctx context.Context, request GitHubTestRequest) (GitHubTestResult, error) {
	var result GitHubTestResult
	err := c.client.JSON(ctx, http.MethodPost, "/v1/deploy/github/test", request, &result)
	return result, err
}
