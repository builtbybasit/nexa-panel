package sftp

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// fakeSystem records the calls Apply drives so tests can assert ordering and
// the rollback behavior without touching a real sshd.
type fakeSystem struct {
	calls        []string
	dropIn       map[string]string
	sshPresent   bool
	sshErr       error
	validateErr  error
	reloadErr    error
	setPassErr   error
	ensureErr    error
	lockErr      error
	lastPassword string
}

func newFakeSystem() *fakeSystem { return &fakeSystem{dropIn: map[string]string{}} }

func (f *fakeSystem) SSHAccessDropInExists(context.Context, string) (bool, error) {
	f.calls = append(f.calls, "ssh")
	return f.sshPresent, f.sshErr
}

func (f *fakeSystem) EnsureChrootRoot(_ context.Context, _ string) error {
	f.calls = append(f.calls, "ensure")
	return f.ensureErr
}

func (f *fakeSystem) WriteDropIn(_ context.Context, path, content string) error {
	f.calls = append(f.calls, "write")
	f.dropIn[path] = content
	return nil
}

func (f *fakeSystem) RemoveDropIn(_ context.Context, path string) error {
	f.calls = append(f.calls, "remove")
	delete(f.dropIn, path)
	return nil
}

func (f *fakeSystem) ValidateSSHD(context.Context) error {
	f.calls = append(f.calls, "validate")
	return f.validateErr
}

func (f *fakeSystem) ReloadSSHD(context.Context) error {
	f.calls = append(f.calls, "reload")
	return f.reloadErr
}

func (f *fakeSystem) SetPassword(_ context.Context, _, password string) error {
	f.calls = append(f.calls, "setpass")
	f.lastPassword = password
	return f.setPassErr
}

func (f *fakeSystem) LockPassword(context.Context, string) error {
	f.calls = append(f.calls, "lock")
	return f.lockErr
}

func operatorWith(system NodeSystem) *HostOperator {
	operator, _ := NewHostOperator(system)
	return operator
}

var validRequest = Request{Slug: "blog", UnixUser: "nexa_blog", RootPath: "/srv/nexa/sites/blog", Enabled: true, Password: "a-strong-password-value"}

func TestApplyEnableWritesValidatesReloadsThenSetsPassword(t *testing.T) {
	system := newFakeSystem()
	observation, err := operatorWith(system).Apply(context.Background(), validRequest)
	if err != nil {
		t.Fatalf("Apply() = %v, want nil", err)
	}
	if !observation.Enabled || !observation.PasswordSet {
		t.Fatalf("observation = %+v, want enabled and password set", observation)
	}
	want := []string{"ssh", "ensure", "write", "validate", "reload", "setpass"}
	if strings.Join(system.calls, ",") != strings.Join(want, ",") {
		t.Fatalf("calls = %v, want %v", system.calls, want)
	}
	content := system.dropIn["/etc/ssh/sshd_config.d/nexa-site-blog.conf"]
	for _, fragment := range []string{"Match User nexa_blog", "ChrootDirectory /srv/nexa/sites/blog", "ForceCommand internal-sftp", "PasswordAuthentication yes"} {
		if !strings.Contains(content, fragment) {
			t.Fatalf("drop-in missing %q:\n%s", fragment, content)
		}
	}
}

func TestApplyEnableRemovesDropInWhenSSHDValidationFails(t *testing.T) {
	system := newFakeSystem()
	system.validateErr = errors.New("bad config")
	_, err := operatorWith(system).Apply(context.Background(), validRequest)
	if err == nil || !strings.Contains(err.Error(), "validate SSH configuration") {
		t.Fatalf("Apply() error = %v, want a validation failure", err)
	}
	if _, present := system.dropIn["/etc/ssh/sshd_config.d/nexa-site-blog.conf"]; present {
		t.Fatal("a drop-in that fails sshd -t must be pulled back out, not left in place")
	}
	if system.calls[len(system.calls)-1] != "remove" {
		t.Fatalf("calls = %v, want the failed drop-in removed last", system.calls)
	}
}

// The SSH-access block sorts before this one and sshd keeps the first value of
// each keyword, so a jail written next to it is not enforced and the password it
// issues cannot authenticate. Refuse before anything is written.
func TestApplyRefusesWhileSSHAccessIsInstalled(t *testing.T) {
	system := newFakeSystem()
	system.sshPresent = true
	_, err := operatorWith(system).Apply(context.Background(), validRequest)
	if !errors.Is(err, ErrSSHAccessPresent) {
		t.Fatalf("Apply() error = %v, want ErrSSHAccessPresent", err)
	}
	if strings.Join(system.calls, ",") != "ssh" {
		t.Fatalf("calls = %v, want the request refused before anything is written", system.calls)
	}
	if system.lastPassword != "" {
		t.Fatal("a refused enable must not set a password the jail cannot serve")
	}
}

func TestApplyDisableRemovesDropInAndLocksAccount(t *testing.T) {
	system := newFakeSystem()
	system.dropIn["/etc/ssh/sshd_config.d/nexa-site-blog.conf"] = "stale"
	request := validRequest
	request.Enabled = false
	request.Password = ""
	observation, err := operatorWith(system).Apply(context.Background(), request)
	if err != nil {
		t.Fatalf("Apply() = %v, want nil", err)
	}
	if observation.Enabled {
		t.Fatal("observation.Enabled = true, want false after disable")
	}
	want := []string{"remove", "validate", "reload", "lock"}
	if strings.Join(system.calls, ",") != strings.Join(want, ",") {
		t.Fatalf("calls = %v, want %v", system.calls, want)
	}
}

func TestApplyRejectsRequestWithForgedIdentity(t *testing.T) {
	cases := map[string]Request{
		"mismatched user": {Slug: "blog", UnixUser: "root", RootPath: "/srv/nexa/sites/blog", Enabled: true},
		"escaped root":    {Slug: "blog", UnixUser: "nexa_blog", RootPath: "/etc", Enabled: true},
		"invalid slug":    {Slug: "../evil", UnixUser: "nexa_blog", RootPath: "/srv/nexa/sites/blog", Enabled: true},
	}
	for name, request := range cases {
		t.Run(name, func(t *testing.T) {
			system := newFakeSystem()
			if _, err := operatorWith(system).Apply(context.Background(), request); err == nil {
				t.Fatal("Apply() = nil, want a derivation failure")
			}
			if len(system.calls) != 0 {
				t.Fatalf("calls = %v, want none; a forged request must never reach the node", system.calls)
			}
		})
	}
}

func TestApplyEnableSkipsPasswordWhenNoneSupplied(t *testing.T) {
	system := newFakeSystem()
	request := validRequest
	request.Password = ""
	observation, err := operatorWith(system).Apply(context.Background(), request)
	if err != nil {
		t.Fatalf("Apply() = %v, want nil", err)
	}
	if observation.PasswordSet {
		t.Fatal("observation.PasswordSet = true, want false when no password was supplied")
	}
	for _, call := range system.calls {
		if call == "setpass" {
			t.Fatal("chpasswd must not run when the request carries no password")
		}
	}
}
