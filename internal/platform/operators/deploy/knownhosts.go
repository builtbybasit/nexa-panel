package deploy

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// hostKey is one published SSH host key of a code-hosting service. Blob is the
// base64 body exactly as the service publishes it, and Fingerprint is the value
// the panel expects to recompute from that body.
type hostKey struct {
	Algorithm   string
	Blob        string
	Fingerprint string
}

// githubHostKeys are GitHub's published SSH host keys. Each entry's Fingerprint
// is checked against the recomputed SHA256 of Blob before anything is written,
// so a typo or a tampered constant fails closed instead of pinning a wrong key.
//
// Transcribed 2026-07-22 from GitHub's two official sources and cross-checked
// against each other, then against `ssh-keygen -lf`:
//   - https://docs.github.com/en/authentication/keeping-your-account-and-data-secure/githubs-ssh-key-fingerprints
//   - https://api.github.com/meta (ssh_keys and ssh_key_fingerprints)
//
// The deprecated DSA key GitHub still lists is deliberately omitted: OpenSSH
// disables ssh-dss by default and pinning it would only widen the set of
// algorithms a downgrade could reach for. Rotating a key here is a code change
// on purpose — ssh-keyscan is never consulted, because a first-contact scan
// trusts whatever answers on the wire.
var githubHostKeys = []hostKey{
	{
		Algorithm:   "ssh-ed25519",
		Blob:        "AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl",
		Fingerprint: "SHA256:+DiY3wvvV6TuJJhbpZisF/zLDA0zPMSvHdkr4UvCOqU",
	},
	{
		Algorithm:   "ecdsa-sha2-nistp256",
		Blob:        "AAAAE2VjZHNhLXNoYTItbmlzdHAyNTYAAAAIbmlzdHAyNTYAAABBBEmKSENjQEezOmxkZMy7opKgwFB9nkt5YRrYMjNuG5N87uRgg6CLrbo5wAdT/y6v0mKV0U2w0WZ2YB/++Tpockg=",
		Fingerprint: "SHA256:p2QAMXNIC1TJYWeIOttrVc98/R1BUFWu3/LiyKgUfQM",
	},
	{
		Algorithm:   "ssh-rsa",
		Blob:        "AAAAB3NzaC1yc2EAAAADAQABAAABgQCj7ndNxQowgcQnjshcLrqPEiiphnt+VTTvDP6mHBL9j1aNUkY4Ue1gvwnGLVlOhGeYrnZaMgRK6+PKCUXaDbC7qtbW8gIkhL7aGCsOr/C56SJMy/BCZfxd1nWzAOxSDPgVsmerOBYfNqltV9/hWCqBywINIR+5dIg6JTJ72pcEpEjcYgXkE2YEFXV1JHnsKgbLWNlhScqb2UmyRkQyytRLtL+38TGxkxCflmO+5Z8CSSNY7GidjMIZ7Q4zMjA2n1nGrlTDkzwDCsw+wqFPGQA179cnfGWOWRVruj16z6XyvxvjJwbz0wQZ75XK5tKSb7FNyeIEs4TT4jk+S4dhPeAUC5y+bDYirYgM4GC7uEnztnZyaVWQ7B381AK4Qdrwt51ZqExKbQpTUNn+EjqoTwvqNj4kqx5QUCI0ThS/YkOxJCXmPUWZbhjpCg56i+2aB6CmK2JGhn57K5mj0MNdBXA4/WnwH6XoPWJzK5Nyu2zB3nAZp+S5hpQs+p1vN1/wsjk=",
		Fingerprint: "SHA256:uNiVztksCsDhcc0u9e8BujQXVUpKZIDTMczCvj3tD2s",
	},
}

// githubHost is the only host the panel pins. It is written unhashed so an
// operator can read the file and compare it against GitHub's published page.
const githubHost = "github.com"

// verifyHostKey recomputes the fingerprint of a pinned key and compares it to
// the constant sitting next to it. Both halves are hardcoded on purpose: the
// check catches a corrupted transcription or an edit to one field only, which
// is the realistic failure mode for a constant nobody reads after review.
func verifyHostKey(key hostKey) error {
	if key.Algorithm == "" || key.Blob == "" {
		return errors.New("host key is incomplete")
	}
	raw, err := base64.StdEncoding.DecodeString(key.Blob)
	if err != nil {
		return fmt.Errorf("decode %s host key: %w", key.Algorithm, err)
	}
	digest := sha256.Sum256(raw)
	computed := "SHA256:" + base64.RawStdEncoding.EncodeToString(digest[:])
	if computed != key.Fingerprint {
		return fmt.Errorf("%s host key fingerprint is %s, expected %s", key.Algorithm, computed, key.Fingerprint)
	}
	return nil
}

// renderKnownHosts builds the site account's known_hosts file from the pinned
// keys, verifying every one of them first. A mismatch returns an error and no
// content at all: a site with no known_hosts fails its next git fetch loudly,
// whereas a site with a wrong one would trust the wrong server quietly.
func renderKnownHosts() (string, error) {
	var builder strings.Builder
	for _, key := range githubHostKeys {
		if err := verifyHostKey(key); err != nil {
			return "", err
		}
		builder.WriteString(githubHost + " " + key.Algorithm + " " + key.Blob + "\n")
	}
	return builder.String(), nil
}
