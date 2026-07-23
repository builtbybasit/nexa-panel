package packages

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// findCommand returns the first recorded invocation of name, and whether one
// was recorded at all.
func findCommand(runner *fakeRunner, name string, argContains string) (Command, bool) {
	for _, c := range runner.commands {
		if c.Name != name {
			continue
		}
		if argContains == "" || strings.Contains(strings.Join(c.Args, " "), argContains) {
			return c, true
		}
	}
	return Command{}, false
}

// The live defect: add-apt-repository resolves a PPA through launchpadlib,
// which caches into $HOME/.launchpadlib. The agent unit sets ProtectHome=true,
// so /root is read-only and the whole thing dies with
//
//	OSError: [Errno 30] Read-only file system: '/root/.launchpadlib'
//
// before it ever reaches the network. No PHP version could be installed from
// the panel on any node. Same shape as the gpg/GNUPGHOME defect: a tool that
// insists on a writable home, run under a sandbox that denies it.
func TestPHPRepositorySetupGivesItsToolingAWritableHome(t *testing.T) {
	runner := newFakeRunner()
	operator := newOperator(t, runner)
	home := t.TempDir()
	operator.toolHome = filepath.Join(home, "apt")

	if err := operator.ensureRepo(context.Background(), catalogEntry{App: "php", Version: "8.5", Repo: repoOndrejPHP}); err != nil {
		t.Fatalf("ensure the ondrej/php repository: %v", err)
	}

	command, ok := findCommand(runner, "add-apt-repository", "ondrej")
	if !ok {
		t.Fatal("the ondrej/php repository was never configured")
	}
	want := "HOME=" + operator.toolHome
	if !containsEnv(command.Env, want) {
		t.Fatalf("add-apt-repository env = %v, want it to carry %q", command.Env, want)
	}
	info, err := os.Stat(operator.toolHome)
	if err != nil {
		t.Fatalf("the tooling home was not created: %v", err)
	}
	if !info.IsDir() {
		t.Fatal("the tooling home is not a directory")
	}
}

// The agent runs with UMask=0177, which masks a MkdirAll(0700) down to 0600 --
// a directory with no execute bit, which nothing can enter. Tests running as
// root would not notice, because root ignores the permission check, so the mode
// is asserted directly.
func TestToolHomeIsUsableUnderTheAgentUmask(t *testing.T) {
	// The umask is set only around the call under test: it would otherwise also
	// cripple t.TempDir() and the harness's own bookkeeping.
	operator := newOperator(t, newFakeRunner())
	operator.toolHome = filepath.Join(t.TempDir(), "apt")

	previous := syscall.Umask(0o177)
	path, err := operator.ensureToolHome()
	syscall.Umask(previous)
	if err != nil {
		t.Fatalf("ensure the tooling home: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat the tooling home: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o700 {
		t.Fatalf("tooling home mode = %v, want 0700; the agent umask masked it away", mode)
	}
}

func containsEnv(env []string, want string) bool {
	for _, entry := range env {
		if entry == want {
			return true
		}
	}
	return false
}
