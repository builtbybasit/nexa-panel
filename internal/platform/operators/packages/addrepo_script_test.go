package packages

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The other database tests stop at the fake runner, so addRepoScript itself had
// never been executed by anything: the shell that configures a vendor repository
// was only ever asserted on as a string. That is precisely where the defect was.
// These tests run the real script against a stub curl and a stub vendor key.

// runAddRepo executes addRepoScript exactly as ensureDatabaseRepo does, with a
// curl stub on PATH that serves keyFixture, and returns the keyring path, the
// sources path, the combined output and the error.
func runAddRepo(t *testing.T, home, keyFixture, wantFingerprint string) (string, string, string, error) {
	t.Helper()
	if _, err := exec.LookPath("gpg"); err != nil {
		t.Skip("gpg is not installed; the repository setup script cannot be exercised")
	}
	root := t.TempDir()
	stubBin := filepath.Join(root, "bin")
	if err := os.MkdirAll(stubBin, 0o755); err != nil {
		t.Fatalf("create stub bin: %v", err)
	}
	// The stub honours only the -o flag the script passes; anything else is a
	// change to the script that this test should be updated alongside.
	stub := "#!/bin/sh\nwhile [ $# -gt 0 ]; do\n  if [ \"$1\" = \"-o\" ]; then shift; cp " +
		shellQuote(keyFixture) + " \"$1\"; exit 0; fi\n  shift\ndone\nexit 1\n"
	if err := os.WriteFile(filepath.Join(stubBin, "curl"), []byte(stub), 0o755); err != nil {
		t.Fatalf("write curl stub: %v", err)
	}

	keyring := filepath.Join(root, "keyrings", "nexa-vendor.gpg")
	list := filepath.Join(root, "sources", "nexa-vendor.list")
	command := exec.Command("sh", "-c", addRepoScript, "nexa-add-repo",
		"https://vendor.example.invalid/key.asc", wantFingerprint, keyring,
		"https://vendor.example.invalid/repo/ubuntu", "noble", "main", list)
	command.Env = append(
		os.Environ(),
		"HOME="+home,
		"PATH="+stubBin+string(os.PathListSeparator)+os.Getenv("PATH"),
	)
	output, err := command.CombinedOutput()
	return keyring, list, string(output), err
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

const fixtureKeyFingerprint = "4581DD08E91E9552DB560ADB87085B70B3BEC2B2"

// The live defect: nexa-agent.service sets ProtectHome=true, so gpg could not
// create $HOME/.gnupg and exited before reading anything. The empty fingerprint
// that left behind was then reported as "signing key fingerprint mismatch:
// downloaded none", which named the wrong problem entirely — no database engine
// from a vendor repository could be installed on any node, and the error blamed
// the vendor's key. HOME here points inside a directory that does not exist,
// which reproduces that for any uid, root included.
func TestRepositorySetupConfiguresAVendorRepositoryWithoutAWritableHome(t *testing.T) {
	keyring, list, output, err := runAddRepo(t, "/nonexistent/nexa", "testdata/vendor-signing-key.asc", fixtureKeyFingerprint)
	if err != nil {
		t.Fatalf("repository setup failed with an unwritable HOME: %v\n%s", err, output)
	}
	if strings.Contains(output, "can't create directory") {
		t.Fatalf("gpg still wants a home directory it cannot create:\n%s", output)
	}
	if info, err := os.Stat(keyring); err != nil {
		t.Fatalf("keyring was not written: %v", err)
	} else if info.Mode().Perm() != 0o644 {
		t.Fatalf("keyring mode = %v, want 0644", info.Mode().Perm())
	}
	contents, err := os.ReadFile(list)
	if err != nil {
		t.Fatalf("sources file was not written: %v", err)
	}
	want := "deb [signed-by=" + keyring + "] https://vendor.example.invalid/repo/ubuntu noble main\n"
	if string(contents) != want {
		t.Fatalf("sources file = %q, want %q", contents, want)
	}
}

// The fingerprint pin is the whole trust anchor, so it has to still refuse a key
// that does not match it — and to say so with the fingerprint it actually read,
// not the empty string a crashed gpg leaves behind.
func TestRepositorySetupRefusesAKeyThatIsNotThePinnedOne(t *testing.T) {
	_, _, output, err := runAddRepo(t, "/nonexistent/nexa", "testdata/vendor-signing-key.asc", strings.Repeat("A", 40))
	if err == nil {
		t.Fatalf("an unpinned signing key was accepted:\n%s", output)
	}
	if !strings.Contains(output, "downloaded "+fixtureKeyFingerprint) {
		t.Fatalf("mismatch did not name the fingerprint it read:\n%s", output)
	}
}
