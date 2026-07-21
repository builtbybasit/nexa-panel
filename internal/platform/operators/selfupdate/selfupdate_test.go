package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fakeSource serves a single canned release for any architecture.
type fakeSource struct {
	release Release
	err     error
	byVer   map[string]Release
}

func (f fakeSource) Latest(context.Context, string) (Release, error) {
	return f.release, f.err
}

func (f fakeSource) ByVersion(_ context.Context, _, version string) (Release, error) {
	if f.byVer != nil {
		if release, ok := f.byVer[version]; ok {
			return release, nil
		}
		return Release{}, errors.New("no matching release was published")
	}
	return f.release, f.err
}

// fakeDownloader serves canned bytes per URL.
type fakeDownloader struct {
	assets map[string][]byte
	err    error
}

func (f fakeDownloader) Fetch(_ context.Context, url string, _ int64) ([]byte, error) {
	if f.err != nil {
		return nil, f.err
	}
	data, ok := f.assets[url]
	if !ok {
		return nil, errors.New("asset not found: " + url)
	}
	return data, nil
}

// fakeRunner answers `version` with a configurable string and records every
// other invocation (notably systemd-run).
type fakeRunner struct {
	versionOutput string
	versionErr    error
	commands      []Command
}

func (r *fakeRunner) Run(_ context.Context, command Command) ([]byte, error) {
	r.commands = append(r.commands, command)
	if len(command.Args) == 1 && command.Args[0] == "version" {
		return []byte(r.versionOutput), r.versionErr
	}
	return nil, nil
}

func checksumOf(data []byte) []byte {
	sum := sha256.Sum256(data)
	line := hex.EncodeToString(sum[:]) + "  nexa-linux-amd64\n"
	return []byte(line)
}

func newTestOperator(t *testing.T, installed string, source ReleaseSource, downloader Downloader, runner Runner) (*HostOperator, string) {
	t.Helper()
	binaryPath := filepath.Join(t.TempDir(), "nexa")
	if err := os.WriteFile(binaryPath, []byte("old-binary"), 0o755); err != nil {
		t.Fatalf("seed binary: %v", err)
	}
	operator, err := NewHostOperator(HostConfig{
		InstalledVersion: installed,
		Source:           source,
		Downloader:       downloader,
		Runner:           runner,
		BinaryPath:       binaryPath,
		RestartDelay:     2 * time.Second,
		Arch:             "amd64",
	})
	if err != nil {
		t.Fatalf("new operator: %v", err)
	}
	return operator, binaryPath
}

func TestLatestReportsUpdateAvailable(t *testing.T) {
	source := fakeSource{release: Release{Version: "0.2.0", Tag: "v0.2.0", AssetURL: "a", ChecksumURL: "c"}}
	operator, _ := newTestOperator(t, "0.1.0", source, fakeDownloader{}, &fakeRunner{})

	availability, err := operator.Latest(context.Background())
	if err != nil {
		t.Fatalf("latest: %v", err)
	}
	if !availability.UpdateAvailable {
		t.Fatal("expected an update to be available")
	}
	if availability.InstalledVersion != "0.1.0" || availability.Latest == nil || availability.Latest.Version != "0.2.0" {
		t.Fatalf("unexpected availability: %+v", availability)
	}
}

func TestLatestReportsUpToDate(t *testing.T) {
	source := fakeSource{release: Release{Version: "0.1.0", Tag: "v0.1.0", AssetURL: "a", ChecksumURL: "c"}}
	operator, _ := newTestOperator(t, "0.1.0", source, fakeDownloader{}, &fakeRunner{})

	availability, err := operator.Latest(context.Background())
	if err != nil {
		t.Fatalf("latest: %v", err)
	}
	if availability.UpdateAvailable {
		t.Fatal("expected no update when already at the latest version")
	}
}

func TestApplySwapsAndSchedulesRestart(t *testing.T) {
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
	if !result.Swapped || !result.RestartScheduled {
		t.Fatalf("expected swap and restart scheduled, got %+v", result)
	}
	if result.PreviousVersion != "0.1.0" || result.TargetVersion != "0.2.0" {
		t.Fatalf("unexpected result versions: %+v", result)
	}
	swapped, err := os.ReadFile(binaryPath)
	if err != nil {
		t.Fatalf("read swapped binary: %v", err)
	}
	if string(swapped) != string(binary) {
		t.Fatalf("binary was not swapped in; got %q", swapped)
	}
	// The restart must be armed as a detached systemd-run timer covering both units.
	var restart *Command
	for index := range runner.commands {
		if runner.commands[index].Name == "systemd-run" {
			restart = &runner.commands[index]
		}
	}
	if restart == nil {
		t.Fatal("expected a systemd-run restart to be scheduled")
	}
	joined := restart.Args
	if !containsAll(joined, "--collect", "--on-active=2", "systemctl", "restart", "nexa-agent.service", "nexa-api.service") {
		t.Fatalf("unexpected restart args: %v", joined)
	}
}

func TestApplyRejectsChecksumMismatch(t *testing.T) {
	binary := []byte("new-nexa-binary-bytes")
	source := fakeSource{release: Release{Version: "0.2.0", Tag: "v0.2.0", AssetURL: "https://x/asset", ChecksumURL: "https://x/asset.sha256"}}
	downloader := fakeDownloader{assets: map[string][]byte{
		"https://x/asset":        binary,
		"https://x/asset.sha256": checksumOf([]byte("different-bytes")),
	}}
	runner := &fakeRunner{versionOutput: "0.2.0"}
	operator, binaryPath := newTestOperator(t, "0.1.0", source, downloader, runner)

	if _, err := operator.Apply(context.Background(), Change{}); err == nil {
		t.Fatal("expected checksum mismatch to abort the apply")
	}
	if data, _ := os.ReadFile(binaryPath); string(data) != "old-binary" {
		t.Fatalf("binary must be untouched after a checksum failure; got %q", data)
	}
	for _, command := range runner.commands {
		if command.Name == "systemd-run" {
			t.Fatal("no restart should be scheduled after a checksum failure")
		}
	}
}

func TestApplyRejectsMismatchedBinaryVersion(t *testing.T) {
	binary := []byte("new-nexa-binary-bytes")
	source := fakeSource{release: Release{Version: "0.2.0", Tag: "v0.2.0", AssetURL: "https://x/asset", ChecksumURL: "https://x/asset.sha256"}}
	downloader := fakeDownloader{assets: map[string][]byte{
		"https://x/asset":        binary,
		"https://x/asset.sha256": checksumOf(binary),
	}}
	// The staged binary reports a different version than the release promised.
	runner := &fakeRunner{versionOutput: "9.9.9 (commit zzz)"}
	operator, binaryPath := newTestOperator(t, "0.1.0", source, downloader, runner)

	if _, err := operator.Apply(context.Background(), Change{}); err == nil {
		t.Fatal("expected a version-mismatch to abort the swap")
	}
	if data, _ := os.ReadFile(binaryPath); string(data) != "old-binary" {
		t.Fatalf("binary must be untouched after validation failure; got %q", data)
	}
}

func TestApplyRejectsDowngradeAndNoOp(t *testing.T) {
	source := fakeSource{byVer: map[string]Release{
		"0.1.0": {Version: "0.1.0", Tag: "v0.1.0", AssetURL: "a", ChecksumURL: "c"},
		"0.0.9": {Version: "0.0.9", Tag: "v0.0.9", AssetURL: "a", ChecksumURL: "c"},
	}}
	operator, _ := newTestOperator(t, "0.1.0", source, fakeDownloader{}, &fakeRunner{})

	if _, err := operator.Apply(context.Background(), Change{Version: "0.1.0"}); err == nil {
		t.Fatal("expected applying the installed version to be rejected")
	}
	if _, err := operator.Apply(context.Background(), Change{Version: "0.0.9"}); err == nil {
		t.Fatal("expected a downgrade to be rejected")
	}
}

func TestApplyLocalBinaryInstallsWithoutDownloadOrVersionGuard(t *testing.T) {
	// A local push carries no release metadata and no checksum; the same-version
	// re-deploy that the release path rejects must be allowed here.
	runner := &fakeRunner{versionOutput: "0.1.0-dev (commit abc, built now)"}
	operator, binaryPath := newTestOperator(t, "0.1.0-dev", fakeSource{err: errors.New("must not be consulted")}, fakeDownloader{err: errors.New("must not download")}, runner)

	staged := filepath.Join(t.TempDir(), "nexa-linux-amd64")
	newBytes := []byte("freshly-built-local-binary")
	if err := os.WriteFile(staged, newBytes, 0o755); err != nil {
		t.Fatalf("stage local binary: %v", err)
	}

	result, err := operator.Apply(context.Background(), Change{BinaryPath: staged})
	if err != nil {
		t.Fatalf("apply local binary: %v", err)
	}
	if !result.Swapped || !result.RestartScheduled {
		t.Fatalf("expected swap and restart, got %+v", result)
	}
	if result.PreviousVersion != "0.1.0-dev" || result.TargetVersion != "0.1.0-dev" {
		t.Fatalf("unexpected result versions: %+v", result)
	}
	if data, _ := os.ReadFile(binaryPath); string(data) != string(newBytes) {
		t.Fatalf("local binary was not swapped in; got %q", data)
	}
	var restart *Command
	for index := range runner.commands {
		if runner.commands[index].Name == "systemd-run" {
			restart = &runner.commands[index]
		}
	}
	if restart == nil {
		t.Fatal("expected a restart to be scheduled for a local install")
	}
}

func TestApplyLocalBinaryRejectsBadPaths(t *testing.T) {
	operator, binaryPath := newTestOperator(t, "0.1.0", fakeSource{}, fakeDownloader{}, &fakeRunner{versionOutput: "0.1.0"})

	if _, err := operator.Apply(context.Background(), Change{BinaryPath: "relative/path"}); err == nil {
		t.Fatal("expected a relative binary path to be rejected")
	}
	if _, err := operator.Apply(context.Background(), Change{BinaryPath: "/no/such/file/nexa"}); err == nil {
		t.Fatal("expected a missing binary to be rejected")
	}
	if data, _ := os.ReadFile(binaryPath); string(data) != "old-binary" {
		t.Fatalf("binary must be untouched after a bad-path rejection; got %q", data)
	}
}

func TestApplyLocalBinaryRejectsNonRunnable(t *testing.T) {
	// A file that does not run as a nexa binary must never be swapped in.
	runner := &fakeRunner{versionErr: errors.New("exec format error")}
	operator, binaryPath := newTestOperator(t, "0.1.0", fakeSource{}, fakeDownloader{}, runner)

	staged := filepath.Join(t.TempDir(), "not-nexa")
	if err := os.WriteFile(staged, []byte("garbage"), 0o755); err != nil {
		t.Fatalf("stage file: %v", err)
	}
	if _, err := operator.Apply(context.Background(), Change{BinaryPath: staged}); err == nil {
		t.Fatal("expected a non-runnable binary to be rejected")
	}
	if data, _ := os.ReadFile(binaryPath); string(data) != "old-binary" {
		t.Fatalf("binary must be untouched after validation failure; got %q", data)
	}
}

func containsAll(haystack []string, needles ...string) bool {
	set := make(map[string]struct{}, len(haystack))
	for _, item := range haystack {
		set[item] = struct{}{}
	}
	for _, needle := range needles {
		if _, ok := set[needle]; !ok {
			return false
		}
	}
	return true
}
