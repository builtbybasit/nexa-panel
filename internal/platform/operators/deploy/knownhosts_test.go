package deploy

import (
	"strings"
	"testing"
)

func TestRenderKnownHostsPinsEveryPublishedGitHubKey(t *testing.T) {
	content, err := renderKnownHosts()
	if err != nil {
		t.Fatalf("renderKnownHosts() = %v, want nil", err)
	}
	lines := strings.Split(strings.TrimSuffix(content, "\n"), "\n")
	if len(lines) != len(githubHostKeys) {
		t.Fatalf("known_hosts has %d lines, want %d", len(lines), len(githubHostKeys))
	}
	for index, line := range lines {
		key := githubHostKeys[index]
		if line != "github.com "+key.Algorithm+" "+key.Blob {
			t.Fatalf("line %d = %q", index+1, line)
		}
	}
}

func TestRenderKnownHostsIsDeterministic(t *testing.T) {
	first, err := renderKnownHosts()
	if err != nil {
		t.Fatalf("renderKnownHosts() = %v, want nil", err)
	}
	second, err := renderKnownHosts()
	if err != nil {
		t.Fatalf("renderKnownHosts() = %v, want nil", err)
	}
	if first != second {
		t.Fatalf("known_hosts differs between renders:\n%s\n%s", first, second)
	}
}

func TestVerifyHostKeyAcceptsEveryPinnedKey(t *testing.T) {
	for _, key := range githubHostKeys {
		if err := verifyHostKey(key); err != nil {
			t.Fatalf("verifyHostKey(%s) = %v, want nil", key.Algorithm, err)
		}
	}
}

func TestVerifyHostKeyRejectsATamperedBlob(t *testing.T) {
	for _, key := range githubHostKeys {
		tampered := key
		// Flip one base64 character in the middle of the body — the shape stays
		// valid, so only the fingerprint check can catch it.
		body := []byte(tampered.Blob)
		middle := len(body) / 2
		if body[middle] == 'A' {
			body[middle] = 'B'
		} else {
			body[middle] = 'A'
		}
		tampered.Blob = string(body)
		if err := verifyHostKey(tampered); err == nil {
			t.Fatalf("verifyHostKey(tampered %s) = nil, want a fingerprint mismatch", key.Algorithm)
		}
	}
}

func TestVerifyHostKeyRejectsAMismatchedFingerprint(t *testing.T) {
	key := githubHostKeys[0]
	key.Fingerprint = "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	if err := verifyHostKey(key); err == nil {
		t.Fatal("verifyHostKey() = nil, want a fingerprint mismatch")
	}
}

func TestVerifyHostKeyRejectsAMalformedBlob(t *testing.T) {
	key := githubHostKeys[0]
	key.Blob = "not base64!!"
	if err := verifyHostKey(key); err == nil {
		t.Fatal("verifyHostKey() = nil, want a decode error")
	}
}
