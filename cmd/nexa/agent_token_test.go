package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nexa-panel/nexa-panel/internal/platform/agentauth"
)

func TestRunAgentTokenCreatesCredentialWithoutRotatingIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.token")
	if err := runAgentToken([]string{"--path", path}); err != nil {
		t.Fatal(err)
	}
	first, err := agentauth.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := runAgentToken([]string{"--path", path}); err != nil {
		t.Fatal(err)
	}
	second, err := agentauth.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("re-running agent-token rotated the shared credential")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("credential mode = %#o, want 0600", got)
	}
}

func TestRunAgentTokenRejectsUnexpectedArguments(t *testing.T) {
	if err := runAgentToken([]string{"extra"}); err == nil {
		t.Fatal("expected unexpected positional argument to be rejected")
	}
}
