package deploy

import (
	"context"
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeEnvSystem is the SSH fake plus the shared-environment surface, so a test
// can drive the operator without a release tree on disk.
type fakeEnvSystem struct {
	*fakeSSHSystem
	content string
	present bool
	written []string
	readErr error
}

func newFakeEnvSystem() *fakeEnvSystem {
	return &fakeEnvSystem{fakeSSHSystem: newFakeSSHSystem()}
}

func (f *fakeEnvSystem) ReadSharedEnvFile(context.Context, string) (string, bool, time.Time, error) {
	f.calls = append(f.calls, "readenv")
	if f.readErr != nil {
		return "", false, time.Time{}, f.readErr
	}
	return f.content, f.present, time.Date(2026, 7, 22, 9, 0, 0, 0, time.UTC), nil
}

func (f *fakeEnvSystem) WriteSharedEnvFile(_ context.Context, _, _, content string) (time.Time, error) {
	f.calls = append(f.calls, "writeenv")
	f.content, f.present = content, true
	f.written = append(f.written, content)
	return time.Date(2026, 7, 22, 9, 30, 0, 0, time.UTC), nil
}

var validEnvRequest = EnvRequest{Slug: "blog", UnixUser: "nexa_blog", RootPath: "/srv/nexa/sites/blog"}

// The path is derived, never taken from the caller, and it points inside the
// nested release tree rather than at the site root the SFTP chroot owns.
func TestReadSharedEnvReturnsTheDocumentAndItsDigest(t *testing.T) {
	system := newFakeEnvSystem()
	system.content, system.present = "APP_KEY=base64:abc\n", true
	document, err := operatorWith(system).ReadSharedEnv(context.Background(), validEnvRequest)
	if err != nil {
		t.Fatalf("ReadSharedEnv() = %v, want nil", err)
	}
	if document.Path != "/srv/nexa/sites/blog/app/shared/.env" {
		t.Fatalf("path = %q", document.Path)
	}
	if !document.Present || document.Content != system.content || document.Bytes != len(system.content) {
		t.Fatalf("document = %+v", document)
	}
	if document.SHA256 != SharedEnvDigest(system.content) || len(document.SHA256) != 64 {
		t.Fatalf("sha256 = %q", document.SHA256)
	}
}

// A missing file is the normal starting state of a site that has just been
// switched into deployer mode, not a failure the panel should report.
func TestReadSharedEnvReportsAnAbsentDocumentWithoutFailing(t *testing.T) {
	document, err := operatorWith(newFakeEnvSystem()).ReadSharedEnv(context.Background(), validEnvRequest)
	if err != nil {
		t.Fatalf("ReadSharedEnv() = %v, want nil", err)
	}
	if document.Present || document.Content != "" || document.Bytes != 0 {
		t.Fatalf("document = %+v, want an absent document", document)
	}
}

func TestWriteSharedEnvPublishesTheDocument(t *testing.T) {
	system := newFakeEnvSystem()
	document, err := operatorWith(system).WriteSharedEnv(context.Background(), validEnvRequest, "DB_PASSWORD=hunter2\n")
	if err != nil {
		t.Fatalf("WriteSharedEnv() = %v, want nil", err)
	}
	if len(system.written) != 1 || system.written[0] != "DB_PASSWORD=hunter2\n" {
		t.Fatalf("written = %q", system.written)
	}
	if !document.Present || document.ModifiedAt.IsZero() {
		t.Fatalf("document = %+v", document)
	}
}

// Every request on this operator is re-derived from the slug; a caller who
// names another account's root must not be able to read or replace its secrets.
func TestSharedEnvRejectsRequestWithForgedIdentity(t *testing.T) {
	system := newFakeEnvSystem()
	forged := EnvRequest{Slug: "blog", UnixUser: "root", RootPath: "/srv/nexa/sites/blog"}
	if _, err := operatorWith(system).ReadSharedEnv(context.Background(), forged); err == nil {
		t.Fatal("ReadSharedEnv() accepted a forged unix user")
	}
	if _, err := operatorWith(system).WriteSharedEnv(context.Background(), forged, "A=1\n"); err == nil {
		t.Fatal("WriteSharedEnv() accepted a forged unix user")
	}
	if len(system.calls) != 0 {
		t.Fatalf("calls = %v, want the request refused before the node was touched", system.calls)
	}
}

func TestWriteSharedEnvRefusesAnOversizedOrNULBearingDocument(t *testing.T) {
	system := newFakeEnvSystem()
	for name, content := range map[string]string{
		"oversized": strings.Repeat("A", MaxSharedEnvBytes+1),
		"nul":       "APP_KEY=a\x00b\n",
	} {
		if _, err := operatorWith(system).WriteSharedEnv(context.Background(), validEnvRequest, content); err == nil {
			t.Fatalf("WriteSharedEnv() accepted a %s document", name)
		}
	}
	if len(system.written) != 0 {
		t.Fatalf("written = %q, want nothing on the node", system.written)
	}
}

// A document the panel cannot show whole must not be shown truncated: the
// editor would save what it rendered and silently drop the rest.
func TestReadSharedEnvRefusesADocumentLargerThanTheCap(t *testing.T) {
	system := newFakeEnvSystem()
	system.content, system.present = strings.Repeat("A", MaxSharedEnvBytes+1), true
	if _, err := operatorWith(system).ReadSharedEnv(context.Background(), validEnvRequest); err == nil {
		t.Fatal("ReadSharedEnv() returned an oversized document")
	}
}

// The real node system: the file has to end up owned by the site account with
// the web group and mode 0640, published atomically, with no temporary half of
// a write left behind.
func TestWriteSharedEnvFilePublishesInsideTheReleaseTree(t *testing.T) {
	rootPath := t.TempDir()
	shared := filepath.Join(rootPath, releaseRootName, sharedDirName)
	if err := os.MkdirAll(shared, 0o750); err != nil {
		t.Fatalf("prepare shared directory: %v", err)
	}
	system := hostSystemForCurrentUser(t)
	modified, err := system.WriteSharedEnvFile(context.Background(), rootPath, "nexa_blog", "APP_ENV=production\n")
	if err != nil {
		t.Fatalf("WriteSharedEnvFile() = %v, want nil", err)
	}
	if modified.IsZero() {
		t.Fatal("WriteSharedEnvFile() reported no modification time")
	}
	info, err := os.Stat(filepath.Join(shared, sharedEnvName))
	if err != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("shared .env = %v, %v; want mode 0640", info, err)
	}
	entries, err := os.ReadDir(shared)
	if err != nil {
		t.Fatalf("read shared: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %v, want only the published document", entries)
	}
	content, present, _, err := system.ReadSharedEnvFile(context.Background(), rootPath)
	if err != nil || !present || content != "APP_ENV=production\n" {
		t.Fatalf("ReadSharedEnvFile() = %q, %v, %v", content, present, err)
	}
}

// A standard-mode site has no release tree at all, and the caller is told that
// rather than being handed an opaque node failure.
func TestSharedEnvFileReportsAMissingReleaseTree(t *testing.T) {
	system := hostSystemForCurrentUser(t)
	_, _, _, err := system.ReadSharedEnvFile(context.Background(), t.TempDir())
	if !errors.Is(err, ErrSharedEnvMissing) {
		t.Fatalf("ReadSharedEnvFile() = %v, want ErrSharedEnvMissing", err)
	}
}

// A symlink where the document belongs is refused rather than followed: the
// site account owns the shared directory, so it could otherwise aim a panel
// read at any file root can open.
func TestReadSharedEnvFileRefusesAnUnmanagedEntry(t *testing.T) {
	rootPath := t.TempDir()
	shared := filepath.Join(rootPath, releaseRootName, sharedDirName)
	if err := os.MkdirAll(shared, 0o750); err != nil {
		t.Fatalf("prepare shared directory: %v", err)
	}
	if err := os.Symlink("/etc/shadow", filepath.Join(shared, sharedEnvName)); err != nil {
		t.Fatalf("plant symlink: %v", err)
	}
	_, _, _, err := hostSystemForCurrentUser(t).ReadSharedEnvFile(context.Background(), rootPath)
	if err == nil || !strings.Contains(err.Error(), "not a managed file") {
		t.Fatalf("ReadSharedEnvFile() = %v, want a refusal", err)
	}
}

// hostSystemForCurrentUser builds the real node system with the account and
// group lookups pointed at whoever is running the test, so the chown a
// privileged node would do is a no-op here instead of a failure.
func hostSystemForCurrentUser(t *testing.T) *SSHHostSystem {
	t.Helper()
	current, err := user.Current()
	if err != nil {
		t.Fatalf("current user: %v", err)
	}
	group := &user.Group{Gid: current.Gid, Name: webGroupName}
	return &SSHHostSystem{
		command:     runCommand,
		lookupUser:  func(string) (*user.User, error) { return current, nil },
		lookupGroup: func(string) (*user.Group, error) { return group, nil },
	}
}
