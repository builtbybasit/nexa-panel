package deploy

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// fakeSSHSystem records the calls ApplySSHAccess drives so tests can assert
// ordering and the rollback behavior without touching a real sshd.
type fakeSSHSystem struct {
	calls       []string
	dropIn      map[string]string
	keys        map[string]string
	sftpPresent bool
	sftpErr     error
	ensureErr   error
	validateErr error
	reloadErr   error
	shellErr    error
	lastShell   string
}

func newFakeSSHSystem() *fakeSSHSystem {
	return &fakeSSHSystem{dropIn: map[string]string{}, keys: map[string]string{}}
}

func (f *fakeSSHSystem) SFTPDropInExists(context.Context, string) (bool, error) {
	f.calls = append(f.calls, "sftp")
	return f.sftpPresent, f.sftpErr
}

func (f *fakeSSHSystem) EnsureHomeSSHDir(context.Context, string, string) error {
	f.calls = append(f.calls, "ensure")
	return f.ensureErr
}

func (f *fakeSSHSystem) WriteAuthorizedKeys(_ context.Context, path, content string) error {
	f.calls = append(f.calls, "writekeys")
	f.keys[path] = content
	return nil
}

func (f *fakeSSHSystem) RemoveAuthorizedKeys(_ context.Context, path string) error {
	f.calls = append(f.calls, "removekeys")
	delete(f.keys, path)
	return nil
}

func (f *fakeSSHSystem) WriteDropIn(_ context.Context, path, content string) error {
	f.calls = append(f.calls, "write")
	f.dropIn[path] = content
	return nil
}

func (f *fakeSSHSystem) RemoveDropIn(_ context.Context, path string) error {
	f.calls = append(f.calls, "remove")
	delete(f.dropIn, path)
	return nil
}

func (f *fakeSSHSystem) SetShell(_ context.Context, _, shell string) error {
	f.calls = append(f.calls, "shell")
	f.lastShell = shell
	return f.shellErr
}

func (f *fakeSSHSystem) ValidateSSHD(context.Context) error {
	f.calls = append(f.calls, "validate")
	return f.validateErr
}

func (f *fakeSSHSystem) ReloadSSHD(context.Context) error {
	f.calls = append(f.calls, "reload")
	return f.reloadErr
}

func (f *fakeSSHSystem) GenerateKeyPair(_ context.Context, comment string) (GeneratedKey, error) {
	f.calls = append(f.calls, "generate")
	return GeneratedKey{PublicKey: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5 " + comment, PrivateKey: "PRIVATE"}, nil
}

func operatorWith(system SSHNodeSystem) *SSHHostOperator {
	operator, _ := NewSSHHostOperator(system)
	return operator
}

const (
	dropInPath = "/etc/ssh/sshd_config.d/nexa-access-blog.conf"
	keysPath   = "/etc/nexa-panel/generated/ssh/nexa_blog/authorized_keys"
)

var validKey = AuthorizedKey{Algorithm: "ssh-ed25519", Blob: "AAAAC3NzaC1lZDI1NTE5AAAAIB0Fx9YQqZ1r3kW7mJ4a2sTQ9v8", Comment: "deploy@laptop"}

var validSSHRequest = SSHAccessRequest{Slug: "blog", UnixUser: "nexa_blog", RootPath: "/srv/nexa/sites/blog", Enabled: true, Keys: []AuthorizedKey{validKey}}

func TestApplySSHAccessEnableWritesKeysThenConfigThenFlipsShell(t *testing.T) {
	system := newFakeSSHSystem()
	observation, err := operatorWith(system).ApplySSHAccess(context.Background(), validSSHRequest)
	if err != nil {
		t.Fatalf("ApplySSHAccess() = %v, want nil", err)
	}
	if !observation.Enabled || observation.Shell != "/bin/bash" || observation.KeyCount != 1 {
		t.Fatalf("observation = %+v, want enabled with a login shell and one key", observation)
	}
	if observation.DropInPath != dropInPath || observation.AuthorizedKeysPath != keysPath {
		t.Fatalf("observation paths = %q, %q", observation.DropInPath, observation.AuthorizedKeysPath)
	}
	want := []string{"sftp", "ensure", "writekeys", "write", "validate", "reload", "shell"}
	if strings.Join(system.calls, ",") != strings.Join(want, ",") {
		t.Fatalf("calls = %v, want %v", system.calls, want)
	}
	content := system.dropIn[dropInPath]
	for _, fragment := range []string{"Match User nexa_blog", "ChrootDirectory none", "ForceCommand none", "AuthorizedKeysFile " + keysPath, "PasswordAuthentication no", "AuthenticationMethods publickey"} {
		if !strings.Contains(content, fragment) {
			t.Fatalf("drop-in missing %q:\n%s", fragment, content)
		}
	}
	if system.keys[keysPath] != "ssh-ed25519 "+validKey.Blob+" deploy@laptop\n" {
		t.Fatalf("authorized keys = %q", system.keys[keysPath])
	}
}

func TestApplySSHAccessRefusesWhileTheSFTPJailIsInstalled(t *testing.T) {
	system := newFakeSSHSystem()
	system.sftpPresent = true
	_, err := operatorWith(system).ApplySSHAccess(context.Background(), validSSHRequest)
	if !errors.Is(err, ErrSFTPJailPresent) {
		t.Fatalf("ApplySSHAccess() error = %v, want ErrSFTPJailPresent", err)
	}
	if strings.Join(system.calls, ",") != "sftp" {
		t.Fatalf("calls = %v, want the request refused before anything is written", system.calls)
	}
}

func TestApplySSHAccessEnableRemovesBothFilesWhenSSHDValidationFails(t *testing.T) {
	system := newFakeSSHSystem()
	system.validateErr = errors.New("bad config")
	_, err := operatorWith(system).ApplySSHAccess(context.Background(), validSSHRequest)
	if err == nil || !strings.Contains(err.Error(), "validate SSH configuration") {
		t.Fatalf("ApplySSHAccess() error = %v, want a validation failure", err)
	}
	if _, present := system.dropIn[dropInPath]; present {
		t.Fatal("a drop-in that fails sshd -t must be pulled back out, not left in place")
	}
	if _, present := system.keys[keysPath]; present {
		t.Fatal("the authorized keys of a rolled-back enable must not survive")
	}
	want := []string{"sftp", "ensure", "writekeys", "write", "validate", "remove", "removekeys", "validate"}
	if strings.Join(system.calls, ",") != strings.Join(want, ",") {
		t.Fatalf("calls = %v, want %v", system.calls, want)
	}
	if system.lastShell != "" {
		t.Fatal("the account must stay nologin when the drop-in never took effect")
	}
}

func TestApplySSHAccessDisableFlipsShellAndKeepsTheHomeSSHDirectory(t *testing.T) {
	system := newFakeSSHSystem()
	system.dropIn[dropInPath] = "stale"
	system.keys[keysPath] = "stale"
	request := validSSHRequest
	request.Enabled = false
	observation, err := operatorWith(system).ApplySSHAccess(context.Background(), request)
	if err != nil {
		t.Fatalf("ApplySSHAccess() = %v, want nil", err)
	}
	if observation.Enabled || observation.Shell != "/usr/sbin/nologin" {
		t.Fatalf("observation = %+v, want disabled with a nologin shell", observation)
	}
	want := []string{"shell", "remove", "removekeys", "validate", "reload"}
	if strings.Join(system.calls, ",") != strings.Join(want, ",") {
		t.Fatalf("calls = %v, want %v", system.calls, want)
	}
	for _, call := range system.calls {
		if call == "ensure" {
			t.Fatal("disabling SSH must not touch {root}/.ssh; the deploy key lives there")
		}
	}
}

func TestApplySSHAccessRejectsRequestWithForgedIdentity(t *testing.T) {
	cases := map[string]SSHAccessRequest{
		"mismatched user": {Slug: "blog", UnixUser: "root", RootPath: "/srv/nexa/sites/blog", Enabled: true},
		"escaped root":    {Slug: "blog", UnixUser: "nexa_blog", RootPath: "/etc", Enabled: true},
		"invalid slug":    {Slug: "../evil", UnixUser: "nexa_blog", RootPath: "/srv/nexa/sites/blog", Enabled: true},
	}
	for name, request := range cases {
		t.Run(name, func(t *testing.T) {
			system := newFakeSSHSystem()
			if _, err := operatorWith(system).ApplySSHAccess(context.Background(), request); err == nil {
				t.Fatal("ApplySSHAccess() = nil, want a derivation failure")
			}
			if len(system.calls) != 0 {
				t.Fatalf("calls = %v, want none; a forged request must never reach the node", system.calls)
			}
		})
	}
}

// An authorized_keys line is field-separated, so anything that can carry a space
// or a newline into it can add an options field — and `command=` there is code
// execution as the site account. These are the shapes that would do it.
func TestApplySSHAccessRejectsKeysThatCouldInjectOptions(t *testing.T) {
	cases := map[string]AuthorizedKey{
		"options in the algorithm": {Algorithm: `command="/bin/sh" ssh-ed25519`, Blob: validKey.Blob},
		"options before the blob":  {Algorithm: "ssh-ed25519", Blob: `AAAA command="/bin/sh"`},
		"newline in the blob":      {Algorithm: "ssh-ed25519", Blob: "AAAA\nssh-rsa BBBB"},
		"newline in the comment":   {Algorithm: "ssh-ed25519", Blob: validKey.Blob, Comment: "ok\nssh-rsa BBBB"},
		"carriage return":          {Algorithm: "ssh-ed25519", Blob: validKey.Blob, Comment: "ok\rssh-rsa BBBB"},
		"option-shaped comment":    {Algorithm: "ssh-ed25519", Blob: validKey.Blob, Comment: `-oProxyCommand=sh`},
		"unsupported algorithm":    {Algorithm: "ssh-dss", Blob: validKey.Blob},
		"empty blob":               {Algorithm: "ssh-ed25519", Blob: ""},
		"oversized comment":        {Algorithm: "ssh-ed25519", Blob: validKey.Blob, Comment: strings.Repeat("a", maxCommentLength+1)},
	}
	for name, key := range cases {
		t.Run(name, func(t *testing.T) {
			system := newFakeSSHSystem()
			request := validSSHRequest
			request.Keys = []AuthorizedKey{key}
			if _, err := operatorWith(system).ApplySSHAccess(context.Background(), request); err == nil {
				t.Fatal("ApplySSHAccess() = nil, want the key refused")
			}
			if len(system.calls) != 0 {
				t.Fatalf("calls = %v, want none; a rejected key must never be written", system.calls)
			}
		})
	}
}

func TestRenderAuthorizedKeysEmitsOneUnadornedLinePerKey(t *testing.T) {
	content := renderAuthorizedKeys([]AuthorizedKey{
		{Algorithm: "ssh-ed25519", Blob: "AAAA", Comment: "one"},
		{Algorithm: "ssh-rsa", Blob: "BBBB"},
	})
	want := "ssh-ed25519 AAAA one\nssh-rsa BBBB\n"
	if content != want {
		t.Fatalf("renderAuthorizedKeys() = %q, want %q", content, want)
	}
}

// The SFTP jail's file sorts after this one and sshd keeps the first value it
// reads for each keyword, so every keyword the jail sets has to be restated
// here or it would leak into an interactive session.
func TestRenderSSHDropInOverridesEverySFTPJailKeyword(t *testing.T) {
	content := renderSSHDropIn(validSSHRequest)
	for _, keyword := range []string{"ChrootDirectory", "ForceCommand", "PasswordAuthentication", "AuthenticationMethods", "AllowTcpForwarding", "AllowAgentForwarding", "X11Forwarding", "PermitTunnel"} {
		if !strings.Contains(content, "    "+keyword+" ") {
			t.Fatalf("drop-in does not restate %q:\n%s", keyword, content)
		}
	}
	if !strings.HasPrefix(content, "# Managed by Nexa Panel.") {
		t.Fatalf("drop-in is missing its managed-file banner:\n%s", content)
	}
}
