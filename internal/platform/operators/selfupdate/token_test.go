package selfupdate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeToken writes a token file with an explicit mode, bypassing the umask so
// the permission cases below test what they claim to.
func writeToken(t *testing.T, contents string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "release.token")
	if err := os.WriteFile(path, []byte(contents), mode); err != nil {
		t.Fatalf("write token: %v", err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("chmod token: %v", err)
	}
	return path
}

func TestTokenReadsTightlyPermissionedFile(t *testing.T) {
	t.Setenv(releaseTokenEnv, "")
	path := writeToken(t, "ghp_secret\n", 0o600)
	value, err := newReleaseTokens(path).token()
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	if value != "ghp_secret" {
		t.Fatalf("token = %q, want the trimmed file contents", value)
	}
}

func TestTokenIsOptional(t *testing.T) {
	// A node with no token file must not fail: the same requests are issued
	// unauthenticated, which is exactly what a public repository needs.
	t.Setenv(releaseTokenEnv, "")
	value, err := newReleaseTokens(filepath.Join(t.TempDir(), "absent")).token()
	if err != nil {
		t.Fatalf("an absent token must not be an error: %v", err)
	}
	if value != "" {
		t.Fatalf("token = %q, want empty", value)
	}
}

func TestTokenRefusesLoosePermissions(t *testing.T) {
	t.Setenv(releaseTokenEnv, "")
	for _, mode := range []os.FileMode{0o644, 0o640, 0o604, 0o666} {
		path := writeToken(t, "ghp_secret", mode)
		value, err := newReleaseTokens(path).token()
		if err == nil {
			t.Fatalf("mode %v must be refused", mode)
		}
		if value != "" {
			t.Fatal("no token may be returned from a rejected file")
		}
		if strings.Contains(err.Error(), "ghp_secret") {
			t.Fatal("the error must never quote the credential")
		}
	}
}

func TestTokenRefusesSymlinkAndEmptyFile(t *testing.T) {
	t.Setenv(releaseTokenEnv, "")
	directory := t.TempDir()
	real := filepath.Join(directory, "real.token")
	if err := os.WriteFile(real, []byte("ghp_secret"), 0o600); err != nil {
		t.Fatalf("write token: %v", err)
	}
	link := filepath.Join(directory, "link.token")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	// A symlink is refused rather than followed: the permissions that were
	// checked would not be the permissions of the file that is read.
	if _, err := newReleaseTokens(link).token(); err == nil {
		t.Fatal("expected a symlinked token file to be refused")
	}
	if _, err := newReleaseTokens(writeToken(t, "   \n", 0o600)).token(); err == nil {
		t.Fatal("expected an empty token file to be refused")
	}
}

func TestTokenRefusesUnsendableValues(t *testing.T) {
	t.Setenv(releaseTokenEnv, "")
	// A value carrying a newline would smuggle a second header into the request.
	path := writeToken(t, "ghp_secret\nX-Injected: 1", 0o600)
	if _, err := newReleaseTokens(path).token(); err == nil {
		t.Fatal("expected a token containing a newline to be refused")
	}
}

func TestTokenEnvironmentOverridesTheFile(t *testing.T) {
	path := writeToken(t, "from-file", 0o600)
	t.Setenv(releaseTokenEnv, "from-environment")
	value, err := newReleaseTokens(path).token()
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	if value != "from-environment" {
		t.Fatalf("token = %q, want the environment value", value)
	}
}

func TestTokenIsRereadSoRotationNeedsNoRestart(t *testing.T) {
	t.Setenv(releaseTokenEnv, "")
	path := writeToken(t, "first", 0o600)
	tokens := newReleaseTokens(path)
	if value, err := tokens.token(); err != nil || value != "first" {
		t.Fatalf("token = %q, %v", value, err)
	}
	if err := os.WriteFile(path, []byte("rotated"), 0o600); err != nil {
		t.Fatalf("rotate token: %v", err)
	}
	value, err := tokens.token()
	if err != nil {
		t.Fatalf("token after rotation: %v", err)
	}
	if value != "rotated" {
		t.Fatalf("token = %q, want the rotated value; it must not be cached", value)
	}
}
