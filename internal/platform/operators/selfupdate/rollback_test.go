package selfupdate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplyKeepsPreviousBinary(t *testing.T) {
	binary := []byte("new-nexa-binary-bytes")
	downloader := fakeDownloader{assets: releaseAssets(releaseArchive(t, "0.2.0", binary, nil))}
	source := fakeSource{release: testRelease()}
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
	downloader := fakeDownloader{assets: releaseAssets(releaseArchive(t, "0.2.0", binary, nil))}
	source := fakeSource{release: testRelease()}
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

// TestRollbackRestoresPackagingWhenRetained covers the case that matters after
// two self-updates: the previous release's tree is still on the node, so a
// rollback puts the binary AND its packaging back together.
func TestRollbackRestoresPackagingWhenRetained(t *testing.T) {
	const secondArchiveURL = "https://api.github.com/repos/o/r/releases/assets/3"
	const secondChecksumURL = "https://api.github.com/repos/o/r/releases/assets/4"

	first := releaseArchive(t, "0.2.0", []byte("nexa-0.2.0"), nil)
	second := releaseArchive(t, "0.3.0", []byte("nexa-0.3.0"), nil)
	source := fakeSource{byVer: map[string]Release{
		"0.2.0": testRelease(),
		"0.3.0": {Version: "0.3.0", Tag: "v0.3.0", AssetURL: secondArchiveURL, ChecksumURL: secondChecksumURL},
	}}
	downloader := fakeDownloader{assets: map[string][]byte{
		archiveURL:        first,
		checksumURL:       checksumOf(first),
		secondArchiveURL:  second,
		secondChecksumURL: checksumOf(second),
	}}
	runner := &fakeRunner{versionOutput: "0.2.0 (commit abc)"}
	operator, binaryPath := newTestOperator(t, "0.1.0", source, downloader, runner)

	if _, err := operator.Apply(context.Background(), Change{Version: "0.2.0"}); err != nil {
		t.Fatalf("apply 0.2.0: %v", err)
	}
	runner.versionOutput = "0.3.0 (commit def)"
	if _, err := operator.Apply(context.Background(), Change{Version: "0.3.0"}); err != nil {
		t.Fatalf("apply 0.3.0: %v", err)
	}
	runner.commands = nil

	result, err := operator.Rollback(context.Background())
	if err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if !result.PackagingSynced || result.PackagingNote != "" {
		t.Fatalf("expected the retained packaging to be restored, got %+v", result)
	}
	if data, _ := os.ReadFile(binaryPath); string(data) != "nexa-0.2.0" {
		t.Fatalf("rollback did not restore the 0.2.0 binary; got %q", data)
	}
	sync := packagingSync(runner)
	if sync == nil {
		t.Fatal("expected the previous release's installer to be re-run")
	}
	if !strings.Contains(sync.Args[0], previousPackagingDir) {
		t.Fatalf("rollback must run the retained previous installer, got %q", sync.Args[0])
	}
	// The rollback is itself undoable: the tree it restored is now current and
	// the one it rolled away from is what a second rollback would re-apply.
	if _, err := os.Stat(filepath.Join(operator.workRoot, previousPackagingDir, "scripts", "install.sh")); err != nil {
		t.Fatalf("the displaced packaging should be retained: %v", err)
	}
}

// TestRollbackWithoutRetainedPackagingIsHonest covers a node's first
// self-update: the packaging it shipped with was never captured, so the binary
// goes back but the units do not — and the result has to say so rather than
// imply a clean revert.
func TestRollbackWithoutRetainedPackagingIsHonest(t *testing.T) {
	binary := []byte("new-nexa-binary-bytes")
	downloader := fakeDownloader{assets: releaseAssets(releaseArchive(t, "0.2.0", binary, nil))}
	runner := &fakeRunner{versionOutput: "0.2.0 (commit abc, built now)"}
	operator, binaryPath := newTestOperator(t, "0.1.0", fakeSource{release: testRelease()}, downloader, runner)

	if _, err := operator.Apply(context.Background(), Change{}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	runner.commands = nil

	result, err := operator.Rollback(context.Background())
	if err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if data, _ := os.ReadFile(binaryPath); string(data) != "old-binary" {
		t.Fatalf("rollback did not restore the original binary; got %q", data)
	}
	if result.PackagingSynced {
		t.Fatal("no packaging was retained, so none can have been synced")
	}
	if result.PackagingNote == "" {
		t.Fatal("a binary-only rollback must be reported as such")
	}
	if sync := packagingSync(runner); sync != nil {
		t.Fatalf("no installer should run when nothing was retained, got %+v", sync)
	}
}
