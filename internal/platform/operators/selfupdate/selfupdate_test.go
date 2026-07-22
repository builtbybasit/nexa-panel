package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
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
	// packagingErr fails the release installer, standing in for a node where
	// --sync-packaging cannot complete.
	packagingErr    error
	packagingOutput string
	commands        []Command
}

func (r *fakeRunner) Run(_ context.Context, command Command) ([]byte, error) {
	r.commands = append(r.commands, command)
	if len(command.Args) == 1 && command.Args[0] == "version" {
		return []byte(r.versionOutput), r.versionErr
	}
	if len(command.Args) > 1 && command.Args[1] == releaseInstallerFlag {
		return []byte(r.packagingOutput), r.packagingErr
	}
	return nil, nil
}

func checksumOf(data []byte) []byte {
	sum := sha256.Sum256(data)
	line := hex.EncodeToString(sum[:]) + "  nexa-panel-linux-amd64.tar.gz\n"
	return []byte(line)
}

// releaseArchive builds a release tarball in the published layout: a single
// versioned top-level directory holding bin/nexa, the packaging tree, the
// installer scripts, and the RELEASE stamp. extra adds or replaces members (a
// path relative to the top-level directory) so a test can express a hostile
// archive without hand-rolling the whole tar.
func releaseArchive(t *testing.T, version string, binary []byte, extra map[string][]byte) []byte {
	t.Helper()
	root := "nexa-panel-" + version + "-linux-amd64/"
	members := map[string][]byte{
		"bin/nexa":                             binary,
		"scripts/install.sh":                   []byte("#!/usr/bin/env bash\nexit 0\n"),
		"scripts/nexa-seed-admin.sh":           []byte("#!/usr/bin/env bash\nexit 0\n"),
		"packaging/systemd/nexa-agent.service": []byte("[Service]\n"),
		"RELEASE":                              []byte("version=" + version + "\narch=amd64\n"),
	}
	for name, contents := range extra {
		members[name] = contents
	}
	names := make([]string, 0, len(members))
	for name := range members {
		names = append(names, name)
	}
	sort.Strings(names)

	var buffer bytes.Buffer
	gzipWriter := gzip.NewWriter(&buffer)
	writer := tar.NewWriter(gzipWriter)
	writeHeader(t, writer, &tar.Header{Name: root, Typeflag: tar.TypeDir, Mode: 0o755})
	for _, name := range names {
		writeHeader(t, writer, &tar.Header{Name: root + name, Typeflag: tar.TypeReg, Mode: 0o755, Size: int64(len(members[name]))})
		if _, err := writer.Write(members[name]); err != nil {
			t.Fatalf("write archive member: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	return buffer.Bytes()
}

func writeHeader(t *testing.T, writer *tar.Writer, header *tar.Header) {
	t.Helper()
	if err := writer.WriteHeader(header); err != nil {
		t.Fatalf("write archive header: %v", err)
	}
}

// releaseAssets pairs an archive with the checksum sidecar the operator will
// verify it against, keyed by the URLs the fake source hands out.
func releaseAssets(archive []byte) map[string][]byte {
	return map[string][]byte{
		archiveURL:  archive,
		checksumURL: checksumOf(archive),
	}
}

// archiveURL and checksumURL stand in for the asset API URLs a real release
// resolves to.
const (
	archiveURL  = "https://api.github.com/repos/o/r/releases/assets/1"
	checksumURL = "https://api.github.com/repos/o/r/releases/assets/2"
)

// testRelease is the canned 0.2.0 release the apply tests resolve.
func testRelease() Release {
	return Release{Version: "0.2.0", Tag: "v0.2.0", AssetURL: archiveURL, ChecksumURL: checksumURL}
}

// packagingSync returns the recorded installer invocation, if any.
func packagingSync(runner *fakeRunner) *Command {
	for index := range runner.commands {
		if len(runner.commands[index].Args) > 1 && runner.commands[index].Args[1] == releaseInstallerFlag {
			return &runner.commands[index]
		}
	}
	return nil
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
		WorkRoot:         filepath.Join(t.TempDir(), "work"),
		RestartDelay:     2 * time.Second,
		Arch:             "amd64",
	})
	if err != nil {
		t.Fatalf("new operator: %v", err)
	}
	return operator, binaryPath
}

func TestLatestReportsUpdateAvailable(t *testing.T) {
	source := fakeSource{release: testRelease()}
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
	source := fakeSource{release: Release{Version: "0.1.0", Tag: "v0.1.0", AssetURL: archiveURL, ChecksumURL: checksumURL}}
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
	archive := releaseArchive(t, "0.2.0", binary, nil)
	source := fakeSource{release: testRelease()}
	downloader := fakeDownloader{assets: releaseAssets(archive)}
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
	// The release's own installer must have re-applied the packaging, and the
	// applied tree must be retained for a later rollback.
	if !result.PackagingSynced {
		t.Fatalf("expected the packaging to be synced, got %+v", result)
	}
	sync := packagingSync(runner)
	if sync == nil {
		t.Fatal("expected the release installer to be run with " + releaseInstallerFlag)
	}
	if sync.Name != "/bin/bash" || !strings.HasSuffix(sync.Args[0], filepath.FromSlash(releaseInstallerEntry)) {
		t.Fatalf("unexpected packaging sync command: %+v", sync)
	}
	// --no-start is not optional: without it the installer restarts nexa-agent
	// mid-call and kills the RPC that is waiting on it.
	if !containsAll(sync.Args, releaseInstallerFlag, releaseInstallerNoStart) {
		t.Fatalf("the installer must be run packaging-only and without a restart: %v", sync.Args)
	}
	if _, err := os.Stat(filepath.Join(operator.workRoot, currentPackagingDir, "packaging", "systemd", "nexa-agent.service")); err != nil {
		t.Fatalf("the applied packaging tree should be retained: %v", err)
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
	archive := releaseArchive(t, "0.2.0", []byte("new-nexa-binary-bytes"), nil)
	source := fakeSource{release: testRelease()}
	downloader := fakeDownloader{assets: map[string][]byte{
		archiveURL:  archive,
		checksumURL: checksumOf([]byte("a-different-archive")),
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
	archive := releaseArchive(t, "0.2.0", []byte("new-nexa-binary-bytes"), nil)
	source := fakeSource{release: testRelease()}
	downloader := fakeDownloader{assets: releaseAssets(archive)}
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
		"0.1.0": {Version: "0.1.0", Tag: "v0.1.0", AssetURL: archiveURL, ChecksumURL: checksumURL},
		"0.0.9": {Version: "0.0.9", Tag: "v0.0.9", AssetURL: archiveURL, ChecksumURL: checksumURL},
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

// TestApplyReportsAPackagingFailure covers the half-upgraded node: the binary
// swap succeeded but the release's packaging did not apply. That must be an
// error naming the recovery, not a success — and no restart may be armed onto a
// node whose units were never updated.
func TestApplyReportsAPackagingFailure(t *testing.T) {
	archive := releaseArchive(t, "0.2.0", []byte("new-nexa-binary-bytes"), nil)
	downloader := fakeDownloader{assets: releaseAssets(archive)}
	runner := &fakeRunner{
		versionOutput:   "0.2.0 (commit abc)",
		packagingErr:    errors.New("exit status 1"),
		packagingOutput: "error: tmpfiles rule rejected\n",
	}
	operator, binaryPath := newTestOperator(t, "0.1.0", fakeSource{release: testRelease()}, downloader, runner)

	result, err := operator.Apply(context.Background(), Change{})
	if err == nil {
		t.Fatal("expected a packaging failure to be reported")
	}
	if !strings.Contains(err.Error(), "tmpfiles rule rejected") {
		t.Fatalf("the installer's own message should reach the operator: %v", err)
	}
	if !result.Swapped || result.PackagingSynced || result.PackagingNote == "" {
		t.Fatalf("a half-applied update must be described honestly, got %+v", result)
	}
	if data, _ := os.ReadFile(binaryPath); string(data) != "new-nexa-binary-bytes" {
		t.Fatalf("the binary swap happened before packaging; got %q", data)
	}
	for _, command := range runner.commands {
		if command.Name == "systemd-run" {
			t.Fatal("no restart should be armed when the packaging did not apply")
		}
	}
	// The rollback target must still be there — it is the way out of this state.
	if data, _ := os.ReadFile(binaryPath + ".prev"); string(data) != "old-binary" {
		t.Fatalf("the previous binary must remain available; got %q", data)
	}
}

// TestApplyLocalBinarySaysPackagingWasNotTouched keeps the operator-push path
// honest: it installs a binary, and nothing else.
func TestApplyLocalBinarySaysPackagingWasNotTouched(t *testing.T) {
	runner := &fakeRunner{versionOutput: "0.1.0-dev (commit abc)"}
	operator, _ := newTestOperator(t, "0.1.0-dev", fakeSource{}, fakeDownloader{}, runner)

	staged := filepath.Join(t.TempDir(), "nexa-linux-amd64")
	if err := os.WriteFile(staged, []byte("freshly-built"), 0o755); err != nil {
		t.Fatalf("stage local binary: %v", err)
	}
	result, err := operator.Apply(context.Background(), Change{BinaryPath: staged})
	if err != nil {
		t.Fatalf("apply local binary: %v", err)
	}
	if result.PackagingSynced {
		t.Fatal("a local binary push applies no packaging")
	}
	if !strings.Contains(result.PackagingNote, releaseInstallerFlag) {
		t.Fatalf("the note should tell the operator how to apply packaging, got %q", result.PackagingNote)
	}
	if sync := packagingSync(runner); sync != nil {
		t.Fatalf("no installer should run for a local push, got %+v", sync)
	}
}
