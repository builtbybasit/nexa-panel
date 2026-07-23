package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunUninstallDelegatesTheExactOperatorArguments(t *testing.T) {
	directory := t.TempDir()
	argumentsPath := filepath.Join(directory, "arguments")
	scriptPath := filepath.Join(directory, "uninstall.sh")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"" + argumentsPath + "\"\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	previous := uninstallScriptPath
	uninstallScriptPath = scriptPath
	t.Cleanup(func() { uninstallScriptPath = previous })

	if err := runUninstall([]string{"--dry-run", "--purge-data"}); err != nil {
		t.Fatal(err)
	}
	written, err := os.ReadFile(argumentsPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Fields(string(written)); strings.Join(got, " ") != "--dry-run --purge-data" {
		t.Fatalf("delegated arguments = %q", got)
	}
}

func TestRunUninstallRejectsANonExecutableLifecycleFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "uninstall.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	previous := uninstallScriptPath
	uninstallScriptPath = path
	t.Cleanup(func() { uninstallScriptPath = previous })
	if err := runUninstall([]string{"--dry-run"}); err == nil {
		t.Fatal("non-executable uninstaller was accepted")
	}
}
