package deploy

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"os/user"
	"strings"
	"testing"
	"time"
)

// The blob and the fingerprint below are a real ed25519 pair as `ssh-keygen -lf`
// prints it, so the fingerprint the panel shows is checked against ssh-keygen's
// rather than against this package's own arithmetic.
const (
	publicKeyBlob     = "AAAAC3NzaC1lZDI1NTE5AAAAILqtg9fJ0I5QxF1etd+F7vHkZNQQSy4lk3zlaGjEz7OC"
	publicKeyPrint    = "SHA256:yZZaa0L1wkdoM2ZnZ2zOmQbpm1Ysvw6XYlfpWhR6I20"
	generatedPublic   = "ssh-ed25519 " + publicKeyBlob + " nexa-blog-deploy\n"
	greeting          = "Hi nexa-panel/site! You've successfully authenticated, but GitHub does not provide shell access.\n"
	lsRemoteOutput    = "6c1f0e\trefs/heads/main\n9ab2c4\trefs/heads/next\n"
	deployKeyRootPath = "/srv/nexa/sites/blog"
)

// fakeDeploySystem is the SSH fake plus the deploy-key surface, so the ordering
// assertions below cover both halves of one operator.
type fakeDeploySystem struct {
	*fakeSSHSystem
	files     map[string]string
	modes     map[string]os.FileMode
	present   map[string]bool
	modTime   time.Time
	generated int
	commands  [][]string
	results   []probeResult
}

type probeResult struct {
	exitCode int
	output   string
	err      error
}

func newFakeDeploySystem() *fakeDeploySystem {
	return &fakeDeploySystem{
		fakeSSHSystem: newFakeSSHSystem(),
		files:         map[string]string{},
		modes:         map[string]os.FileMode{},
		present:       map[string]bool{},
		modTime:       time.Date(2026, 7, 22, 9, 0, 0, 0, time.UTC),
	}
}

func (f *fakeDeploySystem) StatSiteSSHFile(_ context.Context, _, name string) (time.Time, bool, error) {
	f.calls = append(f.calls, "stat:"+name)
	if !f.present[name] {
		return time.Time{}, false, nil
	}
	return f.modTime, true, nil
}

func (f *fakeDeploySystem) WriteSiteSSHFile(_ context.Context, _, _, name, content string, mode os.FileMode) error {
	f.calls = append(f.calls, "write:"+name)
	f.files[name] = content
	f.modes[name] = mode
	f.present[name] = true
	return nil
}

func (f *fakeDeploySystem) ReadSiteSSHFile(_ context.Context, _, name string) (string, error) {
	f.calls = append(f.calls, "read:"+name)
	return f.files[name], nil
}

func (f *fakeDeploySystem) GenerateSiteKey(_ context.Context, _, _, name, comment string) error {
	f.calls = append(f.calls, "generate:"+name)
	f.generated++
	f.present[name] = true
	f.files[name+".pub"] = "ssh-ed25519 " + publicKeyBlob + " " + comment + "\n"
	f.present[name+".pub"] = true
	return nil
}

func (f *fakeDeploySystem) RunAsSite(_ context.Context, _, _ string, argv []string) (int, []byte, error) {
	f.calls = append(f.calls, "run:"+argv[0])
	f.commands = append(f.commands, argv)
	if len(f.results) == 0 {
		return 0, nil, nil
	}
	result := f.results[0]
	f.results = f.results[1:]
	return result.exitCode, []byte(result.output), result.err
}

var validDeployKeyRequest = DeployKeyRequest{Slug: "blog", UnixUser: "nexa_blog", RootPath: deployKeyRootPath}

var validGitHubTestRequest = GitHubTestRequest{Slug: "blog", UnixUser: "nexa_blog", RootPath: deployKeyRootPath, Repository: "git@github.com:nexa-panel/site.git"}

func TestEnsureDeployKeyGeneratesTheKeyAndReturnsOnlyItsPublicHalf(t *testing.T) {
	system := newFakeDeploySystem()
	observation, err := operatorWith(system).EnsureDeployKey(context.Background(), validDeployKeyRequest)
	if err != nil {
		t.Fatalf("EnsureDeployKey() = %v, want nil", err)
	}
	if observation.Algorithm != "ssh-ed25519" || observation.PublicKey != "ssh-ed25519 "+publicKeyBlob {
		t.Fatalf("observation key = %q %q", observation.Algorithm, observation.PublicKey)
	}
	if observation.Fingerprint != publicKeyPrint {
		t.Fatalf("fingerprint = %q, want ssh-keygen's %q", observation.Fingerprint, publicKeyPrint)
	}
	if observation.Path != deployKeyRootPath+"/.ssh/id_ed25519" || !observation.KnownHosts || !observation.CreatedAt.Equal(system.modTime) {
		t.Fatalf("observation = %+v", observation)
	}
	want := []string{"ensure", "write:known_hosts", "write:config", "stat:id_ed25519", "generate:id_ed25519", "stat:id_ed25519", "read:id_ed25519.pub"}
	if strings.Join(system.calls, ",") != strings.Join(want, ",") {
		t.Fatalf("calls = %v, want %v", system.calls, want)
	}
	if system.modes[knownHostsName] != 0o644 || system.modes[sshConfigName] != 0o600 {
		t.Fatalf("modes = %v, want a readable known_hosts and a private config", system.modes)
	}
	// Nothing that could carry the private half may appear in the observation,
	// which is the value the control plane persists.
	for _, field := range []string{observation.PublicKey, observation.Fingerprint, observation.Path} {
		if strings.Contains(field, "PRIVATE KEY") {
			t.Fatalf("observation field %q carries private key material", field)
		}
	}
}

func TestEnsureDeployKeyKeepsAnExistingKeyUnlessRotationIsAsked(t *testing.T) {
	system := newFakeDeploySystem()
	system.present[deployKeyName] = true
	system.files[deployKeyName+".pub"] = generatedPublic
	if _, err := operatorWith(system).EnsureDeployKey(context.Background(), validDeployKeyRequest); err != nil {
		t.Fatalf("EnsureDeployKey() = %v, want nil", err)
	}
	if system.generated != 0 {
		t.Fatal("an ensure must not replace a key GitHub already trusts")
	}
	rotate := validDeployKeyRequest
	rotate.Rotate = true
	if _, err := operatorWith(system).EnsureDeployKey(context.Background(), rotate); err != nil {
		t.Fatalf("EnsureDeployKey() = %v, want nil", err)
	}
	if system.generated != 1 {
		t.Fatalf("generated = %d, want the rotation to mint exactly one new pair", system.generated)
	}
}

func TestEnsureDeployKeyRejectsRequestWithForgedIdentity(t *testing.T) {
	cases := map[string]DeployKeyRequest{
		"mismatched user": {Slug: "blog", UnixUser: "root", RootPath: deployKeyRootPath},
		"escaped root":    {Slug: "blog", UnixUser: "nexa_blog", RootPath: "/root"},
		"invalid slug":    {Slug: "../evil", UnixUser: "nexa_blog", RootPath: deployKeyRootPath},
	}
	for name, request := range cases {
		t.Run(name, func(t *testing.T) {
			system := newFakeDeploySystem()
			if _, err := operatorWith(system).EnsureDeployKey(context.Background(), request); err == nil {
				t.Fatal("EnsureDeployKey() = nil, want a derivation failure")
			}
			if len(system.calls) != 0 {
				t.Fatalf("calls = %v, want none; a forged request must never reach the node", system.calls)
			}
		})
	}
}

// A `.pub` the panel did not write is the one file in .ssh a compromised site
// process could plant, so it goes through the same allowlist an installed key
// faces instead of being echoed back into the panel.
func TestEnsureDeployKeyRejectsAPublicKeyItCannotParse(t *testing.T) {
	cases := map[string]string{
		"empty":                 "",
		"one field":             "ssh-ed25519\n",
		"unsupported algorithm": "ssh-dss " + publicKeyBlob + "\n",
		"body is not base64":    "ssh-ed25519 not/base64!!\n",
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			system := newFakeDeploySystem()
			system.present[deployKeyName] = true
			system.files[deployKeyName+".pub"] = content
			if _, err := operatorWith(system).EnsureDeployKey(context.Background(), validDeployKeyRequest); err == nil {
				t.Fatal("EnsureDeployKey() = nil, want the public key refused")
			}
		})
	}
}

func TestTestGitHubAuthenticatesThenCountsTheRepositoryBranches(t *testing.T) {
	system := newFakeDeploySystem()
	system.results = []probeResult{{exitCode: 1, output: greeting}, {exitCode: 0, output: lsRemoteOutput}}
	result, err := operatorWith(system).TestGitHub(context.Background(), validGitHubTestRequest)
	if err != nil {
		t.Fatalf("TestGitHub() = %v, want nil", err)
	}
	if !result.AuthOK || result.Account != "nexa-panel/site" || !result.LsRemoteOK || result.RefCount != 2 {
		t.Fatalf("result = %+v, want an authenticated probe with two branches", result)
	}
	if !strings.Contains(result.OutputTail, "refs/heads/main") || !strings.Contains(result.OutputTail, "[exit 1]") {
		t.Fatalf("output tail = %q, want both probes transcribed", result.OutputTail)
	}
	// The pinned known_hosts is only worth anything if the probe refuses an
	// unknown host key, and BatchMode is what keeps a prompt from hanging.
	probe := strings.Join(system.commands[0], " ")
	if !strings.Contains(probe, "StrictHostKeyChecking=yes") || !strings.Contains(probe, "BatchMode=yes") || !strings.HasSuffix(probe, "-T git@github.com") {
		t.Fatalf("ssh probe = %q", probe)
	}
	if strings.Join(system.commands[1], " ") != "git ls-remote --heads git@github.com:nexa-panel/site.git" {
		t.Fatalf("ls-remote probe = %q", strings.Join(system.commands[1], " "))
	}
}

func TestTestGitHubReportsAFailedAuthenticationWithoutListingTheRepository(t *testing.T) {
	system := newFakeDeploySystem()
	system.results = []probeResult{{exitCode: 255, output: "git@github.com: Permission denied (publickey).\n"}}
	result, err := operatorWith(system).TestGitHub(context.Background(), validGitHubTestRequest)
	if err != nil {
		t.Fatalf("TestGitHub() = %v, want a verdict rather than an error", err)
	}
	if result.AuthOK || result.LsRemoteOK || result.RefCount != 0 {
		t.Fatalf("result = %+v, want a failed verdict", result)
	}
	if len(system.commands) != 1 {
		t.Fatalf("commands = %v, want the repository probe skipped once authentication failed", system.commands)
	}
	if !strings.Contains(result.OutputTail, "Permission denied") {
		t.Fatalf("output tail = %q, want the failure explained", result.OutputTail)
	}
}

// The repository string is the only free-form field that reaches an argv, so
// everything that is not GitHub's SSH remote form is refused before it can.
func TestTestGitHubRejectsARepositoryItCannotDerive(t *testing.T) {
	cases := map[string]string{
		"https remote":     "https://github.com/nexa-panel/site.git",
		"another forge":    "git@gitlab.com:nexa-panel/site.git",
		"upload-pack flag": "git@github.com:nexa-panel/site.git --upload-pack=sh",
		"path traversal":   "git@github.com:../../etc/passwd",
		"empty":            "",
	}
	for name, repository := range cases {
		t.Run(name, func(t *testing.T) {
			system := newFakeDeploySystem()
			request := validGitHubTestRequest
			request.Repository = repository
			if _, err := operatorWith(system).TestGitHub(context.Background(), request); err == nil {
				t.Fatal("TestGitHub() = nil, want the repository refused")
			}
			if len(system.commands) != 0 {
				t.Fatalf("commands = %v, want none; a refused repository must never reach a probe", system.commands)
			}
		})
	}
}

func TestTestGitHubRejectsRequestWithForgedIdentity(t *testing.T) {
	system := newFakeDeploySystem()
	request := validGitHubTestRequest
	request.UnixUser = "root"
	if _, err := operatorWith(system).TestGitHub(context.Background(), request); err == nil {
		t.Fatal("TestGitHub() = nil, want a derivation failure")
	}
	if len(system.commands) != 0 {
		t.Fatalf("commands = %v, want none; a probe must never run as an attacker-chosen account", system.commands)
	}
}

// The probes must never run as root, and they must not inherit the agent's
// environment: HOME and PATH are restated for the site account, and a git
// credential prompt is turned into an immediate failure.
func TestRunAsSiteDeEscalatesThroughRunuserAndPinsTheEnvironment(t *testing.T) {
	var recorded []string
	system := &SSHHostSystem{
		command: func(_ context.Context, name string, args ...string) ([]byte, error) {
			recorded = append([]string{name}, args...)
			return []byte("out"), nil
		},
		lookupUser: user.Lookup,
	}
	exitCode, output, err := system.RunAsSite(context.Background(), "nexa_blog", deployKeyRootPath, []string{"git", "ls-remote"})
	if err != nil || exitCode != 0 || string(output) != "out" {
		t.Fatalf("RunAsSite() = %d, %q, %v", exitCode, output, err)
	}
	want := "runuser -u nexa_blog -- env HOME=" + deployKeyRootPath + " GIT_TERMINAL_PROMPT=0 " + probePath + " git ls-remote"
	if strings.Join(recorded, " ") != want {
		t.Fatalf("command = %q, want %q", strings.Join(recorded, " "), want)
	}
}

// A probe that ran and failed is a verdict, so its exit status has to come back
// as a number instead of collapsing into an error the operator cannot read.
func TestRunAsSiteReportsAFailedCommandsExitCode(t *testing.T) {
	system := &SSHHostSystem{
		command: func(ctx context.Context, _ string, _ ...string) ([]byte, error) {
			return exec.CommandContext(ctx, "sh", "-c", "exit 7").CombinedOutput()
		},
		lookupUser: user.Lookup,
	}
	exitCode, _, err := system.RunAsSite(context.Background(), "nexa_blog", deployKeyRootPath, []string{"ssh", "-T", "git@github.com"})
	if err != nil {
		t.Fatalf("RunAsSite() = %v, want the failure reported as an exit code", err)
	}
	if exitCode != 7 {
		t.Fatalf("exit code = %d, want 7", exitCode)
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		t.Fatal("a command that ran must not be returned as an error")
	}
}

func TestRenderSSHClientConfigPinsTheSitesOwnKeyAndKnownHosts(t *testing.T) {
	content := renderSSHClientConfig(deployKeyRootPath)
	for _, fragment := range []string{
		"Host github.com",
		"IdentityFile " + deployKeyRootPath + "/.ssh/id_ed25519",
		"IdentitiesOnly yes",
		"UserKnownHostsFile " + deployKeyRootPath + "/.ssh/known_hosts",
		"StrictHostKeyChecking yes",
		"CheckHostIP no",
		"BatchMode yes",
	} {
		if !strings.Contains(content, fragment) {
			t.Fatalf("client config missing %q:\n%s", fragment, content)
		}
	}
	if !strings.HasPrefix(content, "# Managed by Nexa Panel.") {
		t.Fatalf("client config is missing its managed-file banner:\n%s", content)
	}
}

// The node writes both halves owned by the site account and the private one
// unreadable by anyone else; a deploy key any other account could read would be
// worth no more than a shared credential.
func TestGenerateSiteKeyWritesBothHalvesWithTheirFinalModes(t *testing.T) {
	rootPath := t.TempDir()
	if err := os.Mkdir(rootPath+"/.ssh", 0o700); err != nil {
		t.Fatalf("prepare .ssh: %v", err)
	}
	current, err := user.Current()
	if err != nil {
		t.Fatalf("current user: %v", err)
	}
	system := &SSHHostSystem{command: runCommand, lookupUser: func(string) (*user.User, error) { return current, nil }}
	if err := system.GenerateSiteKey(context.Background(), rootPath, "nexa_blog", deployKeyName, "nexa-blog-deploy"); err != nil {
		t.Fatalf("GenerateSiteKey() = %v, want nil", err)
	}
	private, err := os.Stat(rootPath + "/.ssh/" + deployKeyName)
	if err != nil || private.Mode().Perm() != 0o600 {
		t.Fatalf("private key = %v, %v; want mode 0600", private, err)
	}
	public, err := system.ReadSiteSSHFile(context.Background(), rootPath, deployKeyName+".pub")
	if err != nil {
		t.Fatalf("ReadSiteSSHFile() = %v, want nil", err)
	}
	key, err := parsePublicKey(public)
	if err != nil || key.Algorithm != "ssh-ed25519" {
		t.Fatalf("public key = %+v, %v; want a parsable ed25519 key", key, err)
	}
	modTime, present, err := system.StatSiteSSHFile(context.Background(), rootPath, deployKeyName)
	if err != nil || !present || modTime.IsZero() {
		t.Fatalf("StatSiteSSHFile() = %v, %v, %v", modTime, present, err)
	}
	// No temporary half of a rotation may survive the publish.
	entries, err := os.ReadDir(rootPath + "/.ssh")
	if err != nil {
		t.Fatalf("read .ssh: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %v, want exactly the key pair", entries)
	}
}
