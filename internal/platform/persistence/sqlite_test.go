package persistence

import (
	"context"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
)

func TestOpenCreatesNestedDirectoriesAndUsableDatabase(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "nested", "control.db"))
	if err != nil {
		t.Fatalf("Open returned an error: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })

	if err := database.PingContext(context.Background()); err != nil {
		t.Fatalf("database is not usable after Open: %v", err)
	}

	// Re-opening the same path must succeed (idempotent, no schema assumptions).
	again, err := Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatalf("second Open returned an error: %v", err)
	}
	_ = again.Close()
}

// The packaged control panel runs as User=nexa with Group=www-data, so every
// file it creates — including this database — is group-owned by the account
// Nginx runs as, contradicting the nexa:nexa contract the tmpfiles snippet
// declares. Open must hand the file back to the owning account's own group.
func TestAlignStateGroupReclaimsTheFileForTheGivenGroup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.db")
	if err := os.WriteFile(path, []byte("state"), 0o600); err != nil {
		t.Fatal(err)
	}
	// -wal and -shm carry the same secrets and must not be left behind.
	for _, suffix := range []string{"-wal", "-shm"} {
		if err := os.WriteFile(path+suffix, []byte("side"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// A process may always chgrp its own files to a group it belongs to, so the
	// test's own supplementary groups are the only ones it can prove against.
	target := unprivilegedTargetGroup(t, path)
	if err := alignStateGroup(path, target); err != nil {
		t.Fatalf("alignStateGroup returned an error: %v", err)
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		info, err := os.Stat(path + suffix)
		if err != nil {
			t.Fatal(err)
		}
		if gid := int(info.Sys().(*syscall.Stat_t).Gid); gid != target {
			t.Errorf("%s group = %d, want %d", path+suffix, gid, target)
		}
	}
}

// A group that cannot be determined must leave the files exactly as they are
// rather than chowning them to something arbitrary.
func TestAlignStateGroupLeavesUnknownGroupAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.db")
	if err := os.WriteFile(path, []byte("state"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := alignStateGroup(path, -1); err != nil {
		t.Fatalf("alignStateGroup returned an error: %v", err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if before.Sys().(*syscall.Stat_t).Gid != after.Sys().(*syscall.Stat_t).Gid {
		t.Error("an unknown group still changed the file's ownership")
	}
}

// The group to reclaim is a property of the file's OWNER, not of whoever
// happens to be running. A root recovery CLI (nexa mfa reset, nexa audit
// verify, nexa backup system) opens the same database as root; reading the
// group off the running process there would hand control.db to group root and
// break the same tmpfiles contract this code exists to hold. The file's current
// group is deliberately wrong here — that is the drift being corrected — so the
// answer must ignore it too.
func TestOwnerGroupReadsTheOwnerAccountNotTheCallingProcess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.db")
	if err := os.WriteFile(path, []byte("state"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(path, -1, unprivilegedTargetGroup(t, path)); err != nil {
		t.Skipf("cannot chgrp the fixture away from the owner's group: %v", err)
	}

	owner, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	want, err := strconv.Atoi(owner.Gid)
	if err != nil {
		t.Fatal(err)
	}
	if got := ownerGroup(path); got != want {
		t.Errorf("ownerGroup = %d, want the owning account's login group %d", got, want)
	}
}

// The regression only separates "owner" from "caller" when the two differ, and
// only root may create a file it does not own. Running as root is exactly the
// case that broke — sudo nexa mfa reset left control.db group-owned by root —
// so when the suite has the privilege to reproduce it, it must.
func TestOwnerGroupIgnoresARootCallersOwnGroup(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root to own a file as another account")
	}
	owner, err := user.Lookup("daemon")
	if err != nil {
		t.Skipf("no second account to own the fixture: %v", err)
	}
	uid, err := strconv.Atoi(owner.Uid)
	if err != nil {
		t.Fatal(err)
	}
	want, err := strconv.Atoi(owner.Gid)
	if err != nil {
		t.Fatal(err)
	}
	if want == os.Getegid() {
		t.Skip("the second account shares the caller's group, so the two cannot be told apart")
	}

	path := filepath.Join(t.TempDir(), "control.db")
	if err := os.WriteFile(path, []byte("state"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(path, uid, os.Getegid()); err != nil {
		t.Fatal(err)
	}
	if got := ownerGroup(path); got != want {
		t.Errorf("ownerGroup = %d, want the owner's group %d, not the root caller's %d", got, want, os.Getegid())
	}
}

// A path with no owner to read gives no group to align to, and Open must carry
// on rather than chown the file to something arbitrary.
func TestOwnerGroupOfMissingFileIsUnknown(t *testing.T) {
	if got := ownerGroup(filepath.Join(t.TempDir(), "absent.db")); got != -1 {
		t.Errorf("ownerGroup of a missing file = %d, want -1", got)
	}
}

// unprivilegedTargetGroup picks a group the test process may actually chgrp to:
// one of its own supplementary groups that the file does not already have, or
// the file's current group when the process belongs to only one.
func unprivilegedTargetGroup(t *testing.T, path string) int {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	current := int(info.Sys().(*syscall.Stat_t).Gid)
	groups, err := os.Getgroups()
	if err != nil {
		t.Fatal(err)
	}
	for _, gid := range groups {
		if gid != current {
			return gid
		}
	}
	return current
}
