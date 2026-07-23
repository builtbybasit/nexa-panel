package selfupdate

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestApplyKeepsPreviousBinary(t *testing.T) {
	binary := []byte("new-nexa-binary-bytes")
	downloader := fakeDownloader{assets: releaseAssets(releaseArchive(t, "0.2.0", binary, nil))}
	source := fakeSource{release: testRelease()}
	runner := &fakeRunner{versionOutput: "0.2.0 (commit abc, built now)"}
	operator, binaryPath := newTestOperator(t, "0.1.0", source, downloader, runner)

	_, err := operator.Apply(context.Background(), Change{})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}

	// The replaced binary must be preserved inside the root-only transaction.
	state, err := readTransaction(operator.transactionPath())
	if err != nil {
		t.Fatalf("read transaction: %v", err)
	}
	prev, err := os.ReadFile(state.PreservedBinary)
	if err != nil {
		t.Fatalf("read preserved binary: %v", err)
	}
	if string(prev) != "old-binary" {
		t.Fatalf("preserved binary = %q, want the original old-binary", prev)
	}
	if data, _ := os.ReadFile(binaryPath); string(data) != string(binary) {
		t.Fatalf("live binary = %q, want the new bytes", data)
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
	if !result.Swapped || !result.Activated {
		t.Fatalf("expected a health-verified rollback activation, got %+v", result)
	}
	if data, _ := os.ReadFile(binaryPath); string(data) != "old-binary" {
		t.Fatalf("rollback did not restore the original binary; got %q", data)
	}
	// Rolling back snapshots the binary it displaced, so a rollback is undoable.
	state, err := readTransaction(operator.transactionPath())
	if err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(state.PreservedBinary); string(data) != string(binary) {
		t.Fatalf("rollback should re-preserve the displaced binary; got %q", data)
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

// TestRollbackRestoresExactHostSnapshot proves rollback does not depend on a
// previous release archive. The destinations that existed immediately before
// the update are restored byte-for-byte, including after multiple releases.
func TestRollbackRestoresExactHostSnapshot(t *testing.T) {
	const secondArchiveURL = "https://api.github.com/repos/o/r/releases/assets/4"
	const secondChecksumURL = "https://api.github.com/repos/o/r/releases/assets/5"
	const secondSignatureURL = "https://api.github.com/repos/o/r/releases/assets/6"

	first := releaseArchive(t, "0.2.0", []byte("nexa-0.2.0"), nil)
	second := releaseArchive(t, "0.3.0", []byte("nexa-0.3.0"), nil)
	source := fakeSource{byVer: map[string]Release{
		"0.2.0": testRelease(),
		"0.3.0": {Version: "0.3.0", Tag: "v0.3.0", AssetName: amd64AssetName, AssetURL: secondArchiveURL, ChecksumURL: secondChecksumURL, SignatureURL: secondSignatureURL},
	}}
	downloader := fakeDownloader{assets: map[string][]byte{
		archiveURL:         first,
		checksumURL:        checksumOf(first),
		signatureURL:       []byte("signed-first"),
		secondArchiveURL:   second,
		secondChecksumURL:  checksumOf(second),
		secondSignatureURL: []byte("signed-second"),
	}}
	managed := filepath.Join(t.TempDir(), "nexa-api.service")
	if err := os.WriteFile(managed, []byte("packaging-0.1.0"), 0o644); err != nil {
		t.Fatalf("seed managed packaging: %v", err)
	}
	packagingVersion := 1
	runner := &fakeRunner{versionOutput: "0.2.0 (commit abc)"}
	runner.packagingHook = func(Command) error {
		packagingVersion++
		return os.WriteFile(managed, []byte("packaging-0."+string(rune('0'+packagingVersion))+".0"), 0o644)
	}
	operator, binaryPath := newTestOperator(t, "0.1.0", source, downloader, runner)
	operator.managedPaths = []string{managed}

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
	if data, _ := os.ReadFile(managed); string(data) != "packaging-0.2.0" {
		t.Fatalf("rollback restored %q, want the exact pre-0.3.0 packaging", data)
	}
}

func TestFirstUpdateRollbackRestoresPreUpdatePackaging(t *testing.T) {
	binary := []byte("new-nexa-binary-bytes")
	downloader := fakeDownloader{assets: releaseAssets(releaseArchive(t, "0.2.0", binary, nil))}
	managed := filepath.Join(t.TempDir(), "nexa-agent.service")
	if err := os.WriteFile(managed, []byte("original-packaging"), 0o644); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{versionOutput: "0.2.0 (commit abc, built now)", packagingHook: func(Command) error {
		return os.WriteFile(managed, []byte("new-packaging"), 0o644)
	}}
	operator, binaryPath := newTestOperator(t, "0.1.0", fakeSource{release: testRelease()}, downloader, runner)
	operator.managedPaths = []string{managed}

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
	if !result.PackagingSynced || !result.Activated {
		t.Fatalf("first rollback must restore binary and packaging, got %+v", result)
	}
	if data, _ := os.ReadFile(managed); string(data) != "original-packaging" {
		t.Fatalf("first rollback restored %q, want original packaging", data)
	}
}
