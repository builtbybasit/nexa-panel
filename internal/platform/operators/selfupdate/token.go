package selfupdate

import (
	"fmt"
	"net/http"
	"os"
	"strings"
	"syscall"
)

// defaultReleaseTokenPath is where the node installer writes the credential that
// grants read access to the release repository. The repository is private, so
// both the release metadata and the release assets are 404s without it.
const defaultReleaseTokenPath = "/etc/nexa-panel/release.token"

// releaseTokenEnv overrides the token file for a one-off run (an operator
// testing a release before the installer has written the file). It is read from
// the agent's own environment, never from an RPC, so it stays outside a caller's
// reach exactly like the repository constants.
const releaseTokenEnv = "NEXA_RELEASE_TOKEN"

// maxTokenBytes caps the token file. A GitHub token is under a hundred bytes;
// anything larger is a misplaced file, not a credential.
const maxTokenBytes = 4 * 1024

// releaseTokens reads the release credential from disk on demand.
//
// The token is deliberately NOT cached: it is re-read for every operation so an
// operator can rotate a leaked or expired credential by rewriting the file,
// without restarting the privileged agent. The cost is one small read per
// update check, which is dwarfed by the HTTPS round trip it precedes.
//
// Nothing in this type ever puts the token itself into an error: every failure
// names the path or the condition, never the contents. A token that reaches a
// log line, a job payload, or an API response is an incident.
type releaseTokens struct {
	path string
}

func newReleaseTokens(path string) *releaseTokens {
	if path == "" {
		path = defaultReleaseTokenPath
	}
	return &releaseTokens{path: path}
}

// token returns the release credential, or "" when the node has none. An absent
// token is not an error: the same requests are then issued unauthenticated, so
// nothing has to change here if the repository is ever made public.
func (t *releaseTokens) token() (string, error) {
	if value := strings.TrimSpace(os.Getenv(releaseTokenEnv)); value != "" {
		if !isSafeHeaderValue(value) {
			return "", fmt.Errorf("the %s value is not a usable release token", releaseTokenEnv)
		}
		return value, nil
	}
	info, err := os.Lstat(t.path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("read the release token at %s: %w", t.path, err)
	}
	// Lstat, and a regular-file requirement, so a symlink cannot point the read
	// at a file whose permissions were never checked.
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("the release token at %s is not a regular file", t.path)
	}
	if info.Size() > maxTokenBytes {
		return "", fmt.Errorf("the release token at %s is too large to be a credential", t.path)
	}
	// A world- or group-readable GitHub token is a real incident, so refuse to
	// use one rather than quietly depending on it. The owner check compares
	// against the running euid: the agent is root on a node, so this is the
	// "root-owned" requirement, while still being exercisable unprivileged in
	// tests. A file owned by any other account is refused either way.
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		if stat.Uid != 0 && int(stat.Uid) != os.Geteuid() {
			return "", fmt.Errorf("the release token at %s must be owned by root", t.path)
		}
	}
	if info.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("the release token at %s must be mode 0600; it is readable by other accounts", t.path)
	}
	raw, err := os.ReadFile(t.path)
	if err != nil {
		return "", fmt.Errorf("read the release token at %s: %w", t.path, err)
	}
	value := strings.TrimSpace(string(raw))
	if value == "" {
		return "", fmt.Errorf("the release token at %s is empty", t.path)
	}
	if !isSafeHeaderValue(value) {
		return "", fmt.Errorf("the release token at %s is not a usable credential", t.path)
	}
	return value, nil
}

// authorize attaches the release credential to a request when the node has one.
// A node without a token sends the request unauthenticated and untouched.
func (t *releaseTokens) authorize(request *http.Request) error {
	value, err := t.token()
	if err != nil || value == "" {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+value)
	return nil
}

// isSafeHeaderValue rejects anything that cannot go into an Authorization
// header verbatim. A token carrying a newline would otherwise smuggle a second
// header into the request, so a malformed file is refused rather than sent.
func isSafeHeaderValue(value string) bool {
	for _, r := range value {
		if r < 0x21 || r > 0x7e {
			return false
		}
	}
	return true
}
