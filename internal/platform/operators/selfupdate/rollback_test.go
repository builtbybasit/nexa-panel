package selfupdate

import (
	"context"
	"os"
	"testing"
)

func TestApplyKeepsPreviousBinary(t *testing.T) {
	binary := []byte("new-nexa-binary-bytes")
	source := fakeSource{release: Release{Version: "0.2.0", Tag: "v0.2.0", AssetURL: "https://x/asset", ChecksumURL: "https://x/asset.sha256"}}
	downloader := fakeDownloader{assets: map[string][]byte{
		"https://x/asset":        binary,
		"https://x/asset.sha256": checksumOf(binary),
	}}
	runner := &fakeRunner{versionOutput: "0.2.0 (commit abc, built now)"}
	operator, binaryPath := newTestOperator(t, "0.1.0", source, downloader, runner)

	result, err := operator.Apply(context.Background(), Change{})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}

	// The replaced binary must be preserved verbatim next to the live one.
	prev, err := os.ReadFile(binaryPath + ".prev")
	if err != nil {
		t.Fatalf("read preserved binary: %v", err)
	}
	if string(prev) != "old-binary" {
		t.Fatalf("preserved binary = %q, want the original old-binary", prev)
	}
	if data, _ := os.ReadFile(binaryPath); string(data) != string(binary) {
		t.Fatalf("live binary = %q, want the new bytes", data)
	}
	if result.PreviousBinaryPath != binaryPath+".prev" {
		t.Fatalf("result should surface the rollback target, got %q", result.PreviousBinaryPath)
	}
}

func TestRollbackRestoresPreviousBinary(t *testing.T) {
	binary := []byte("new-nexa-binary-bytes")
	source := fakeSource{release: Release{Version: "0.2.0", Tag: "v0.2.0", AssetURL: "https://x/asset", ChecksumURL: "https://x/asset.sha256"}}
	downloader := fakeDownloader{assets: map[string][]byte{
		"https://x/asset":        binary,
		"https://x/asset.sha256": checksumOf(binary),
	}}
	runner := &fakeRunner{versionOutput: "0.2.0 (commit abc, built now)"}
	operator, binaryPath := newTestOperator(t, "0.1.0", source, downloader, runner)

	if _, err := operator.Apply(context.Background(), Change{}); err != nil {
		t.Fatalf("apply: %v", err)
	}

	result, err := operator.Rollback(context.Background())
	if err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if !result.Swapped || !result.RestartScheduled {
		t.Fatalf("expected swap and restart after rollback, got %+v", result)
	}
	if data, _ := os.ReadFile(binaryPath); string(data) != "old-binary" {
		t.Fatalf("rollback did not restore the original binary; got %q", data)
	}
	// Rolling back re-preserves the binary it displaced, so a rollback is undoable.
	if data, _ := os.ReadFile(binaryPath + ".prev"); string(data) != string(binary) {
		t.Fatalf("rollback should re-preserve the displaced binary; got %q", data)
	}
	// A detached restart covering both units must be armed.
	var restart *Command
	for index := range runner.commands {
		if runner.commands[index].Name == "systemd-run" {
			restart = &runner.commands[index]
		}
	}
	if restart == nil {
		t.Fatal("expected a systemd-run restart to be scheduled after rollback")
	}
	if !containsAll(restart.Args, "systemctl", "restart", "nexa-agent.service", "nexa-api.service") {
		t.Fatalf("unexpected restart args: %v", restart.Args)
	}
}

func TestRollbackWithoutPreviousFails(t *testing.T) {
	operator, binaryPath := newTestOperator(t, "0.1.0", fakeSource{}, fakeDownloader{}, &fakeRunner{versionOutput: "0.1.0"})

	if _, err := operator.Rollback(context.Background()); err == nil {
		t.Fatal("expected rollback to fail when no previous binary exists")
	}
	if data, _ := os.ReadFile(binaryPath); string(data) != "old-binary" {
		t.Fatalf("binary must be untouched when rollback has nothing to restore; got %q", data)
	}
}
