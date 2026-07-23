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
	signatureErr    error
	packagingHook   func(Command) error
	commands        []Command
}

func (r *fakeRunner) Run(ctx context.Context, command Command) ([]byte, error) {
	r.commands = append(r.commands, command)
	// A real exec fails immediately on a dead context; the restore paths must
	// not depend on the caller's context still being alive.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(command.Args) == 1 && command.Args[0] == "version" {
		return []byte(r.versionOutput), r.versionErr
	}
	if len(command.Args) > 1 && command.Args[1] == releaseInstallerFlag {
		if r.packagingHook != nil {
			if err := r.packagingHook(command); err != nil {
				return []byte(r.packagingOutput), err
			}
		}
		return []byte(r.packagingOutput), r.packagingErr
	}
	if command.Name == "ssh-keygen" {
		return []byte("signature verification failed"), r.signatureErr
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
		"bin/nexa":           binary,
		"scripts/install.sh": []byte("#!/usr/bin/env bash\nexit 0\n"),
		// The fixture carries every executable the real bundle does. It used to
		// stop at the seed helper, so no test modelled a release faithfully and
		// the uninstaller's mode went unchecked until a live node refused it.
		"scripts/nexa-seed-admin.sh":           []byte("#!/usr/bin/env bash\nexit 0\n"),
		"scripts/nexa-release-helper.py":       []byte("#!/usr/bin/env python3\n"),
		"scripts/uninstall.sh":                 []byte("#!/usr/bin/env bash\nexit 0\n"),
		"packaging/systemd/nexa-agent.service": []byte("[Service]\n"),
		releaseManifestEntry:                   []byte("version=" + version + "\ncommit=abc123def456\narch=amd64\nbuilt_at=2026-07-22T09:08:07Z\n"),
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
		archiveURL:   archive,
		checksumURL:  checksumOf(archive),
		signatureURL: []byte("-----BEGIN SSH SIGNATURE-----\ntest\n-----END SSH SIGNATURE-----\n"),
	}
}

// archiveURL and checksumURL stand in for the asset API URLs a real release
// resolves to.
const (
	archiveURL   = "https://api.github.com/repos/o/r/releases/assets/1"
	checksumURL  = "https://api.github.com/repos/o/r/releases/assets/2"
	signatureURL = "https://api.github.com/repos/o/r/releases/assets/3"
	// amd64AssetName is the published bundle name the test operator's
	// architecture must resolve to.
	amd64AssetName = "nexa-panel-linux-amd64.tar.gz"
)

// testRelease is the canned 0.2.0 release the apply tests resolve.
func testRelease() Release {
	return Release{Version: "0.2.0", Tag: "v0.2.0", AssetName: amd64AssetName, AssetURL: archiveURL, ChecksumURL: checksumURL, SignatureURL: signatureURL}
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
	allowedSigners := filepath.Join(t.TempDir(), "release-signers")
	if err := os.WriteFile(allowedSigners, []byte(releaseSignerIdentity+" ssh-ed25519 AAAATEST\n"), 0o600); err != nil {
		t.Fatalf("seed allowed signers: %v", err)
	}
	operator, err := NewHostOperator(HostConfig{
		InstalledVersion:       installed,
		Source:                 source,
		Downloader:             downloader,
		Runner:                 runner,
		BinaryPath:             binaryPath,
		ControlDatabasePath:    filepath.Join(t.TempDir(), "control.db"),
		WorkRoot:               filepath.Join(t.TempDir(), "work"),
		LockPath:               filepath.Join(t.TempDir(), "lifecycle.lock"),
		AllowedSignersPath:     allowedSigners,
		Arch:                   "amd64",
		activationPollInterval: time.Millisecond,
		managedPaths:           []string{},
		activate: func(_ context.Context, statePath string) error {
			state, err := readTransaction(statePath)
			if err != nil {
				return err
			}
			state.Phase = phaseSucceeded
			state.Result.Activated = true
			return writeTransaction(statePath, state)
		},
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

func TestLifecycleLockSerializesInstallUpdateAndUninstall(t *testing.T) {
	first, _ := newTestOperator(t, "0.1.0", fakeSource{}, fakeDownloader{}, &fakeRunner{})
	second, _ := newTestOperator(t, "0.1.0", fakeSource{}, fakeDownloader{}, &fakeRunner{})
	second.lockPath = first.lockPath

	_, unlock, err := first.acquireHostLock(context.Background())
	if err != nil {
		t.Fatalf("acquire first lock: %v", err)
	}
	defer unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	if _, _, err := second.acquireHostLock(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second lifecycle operation acquired the shared lock: %v", err)
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
	if !result.Swapped || !result.Activated {
		t.Fatalf("expected a completed, health-verified activation, got %+v", result)
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
	// The release's own installer must have re-applied the packaging.
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
	if len(sync.ExtraFiles) != 1 || sync.ExtraFiles[0] == nil {
		t.Fatalf("packaging installer must inherit the held lifecycle lock as fd 3: %+v", sync.ExtraFiles)
	}
	if !containsAll(sync.Env, "NEXA_LIFECYCLE_LOCK_FD=3") {
		t.Fatalf("packaging installer was not told which inherited fd proves lock ownership: %v", sync.Env)
	}
	state, err := readTransaction(operator.transactionPath())
	if err != nil || state.Phase != phaseSucceeded || state.SnapshotDir == "" {
		t.Fatalf("successful apply must retain a rollback journal, state=%+v err=%v", state, err)
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

func TestApplyRejectsAnUntrustedReleaseSignature(t *testing.T) {
	archive := releaseArchive(t, "0.2.0", []byte("new-nexa-binary-bytes"), nil)
	runner := &fakeRunner{versionOutput: "0.2.0", signatureErr: errors.New("incorrect signature")}
	operator, binaryPath := newTestOperator(t, "0.1.0", fakeSource{release: testRelease()}, fakeDownloader{assets: releaseAssets(archive)}, runner)

	if _, err := operator.Apply(context.Background(), Change{}); err == nil || !strings.Contains(err.Error(), "signature") {
		t.Fatalf("expected authenticity failure, got %v", err)
	}
	if data, _ := os.ReadFile(binaryPath); string(data) != "old-binary" {
		t.Fatalf("an untrusted release must not change the live binary; got %q", data)
	}
	for _, command := range runner.commands {
		if command.Name == "/bin/bash" || command.Name == "systemd-run" {
			t.Fatalf("untrusted bytes reached a mutating command: %+v", command)
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

	result, err := operator.ApplyLocalBinary(context.Background(), staged)
	if err != nil {
		t.Fatalf("apply local binary: %v", err)
	}
	if !result.Swapped || !result.Activated {
		t.Fatalf("expected completed local activation, got %+v", result)
	}
	if result.PreviousVersion != "0.1.0-dev" || result.TargetVersion != "0.1.0-dev" {
		t.Fatalf("unexpected result versions: %+v", result)
	}
	if data, _ := os.ReadFile(binaryPath); string(data) != string(newBytes) {
		t.Fatalf("local binary was not swapped in; got %q", data)
	}
}

func TestApplyLocalBinaryRejectsBadPaths(t *testing.T) {
	operator, binaryPath := newTestOperator(t, "0.1.0", fakeSource{}, fakeDownloader{}, &fakeRunner{versionOutput: "0.1.0"})

	if _, err := operator.ApplyLocalBinary(context.Background(), "relative/path"); err == nil {
		t.Fatal("expected a relative binary path to be rejected")
	}
	if _, err := operator.ApplyLocalBinary(context.Background(), "/no/such/file/nexa"); err == nil {
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
	if _, err := operator.ApplyLocalBinary(context.Background(), staged); err == nil {
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

// TestApplyPackagingFailureLeavesTheLiveBinaryUntouched proves packaging is
// transactional: the old binary remains live and no activation is launched.
func TestApplyPackagingFailureLeavesTheLiveBinaryUntouched(t *testing.T) {
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
	if result.Swapped || result.PackagingSynced || result.Activated {
		t.Fatalf("a packaging failure must not move the live binary, got %+v", result)
	}
	if data, _ := os.ReadFile(binaryPath); string(data) != "old-binary" {
		t.Fatalf("the original binary must remain live; got %q", data)
	}
	for _, command := range runner.commands {
		if command.Name == "systemd-run" {
			t.Fatal("no restart should be armed when the packaging did not apply")
		}
	}
	state, stateErr := readTransaction(operator.transactionPath())
	if stateErr != nil || state.Phase != phaseFailed {
		t.Fatalf("failed packaging must leave a recoverable terminal journal: state=%+v err=%v", state, stateErr)
	}
}

// TestApplyRestoresTheOldBinaryWhenTheCallerCancels is the regression test for
// a restore that inherited the caller's context: the failure it undoes is often
// the cancellation (or apply deadline) itself, and a restore that could not
// exec the binary it reinstalls left the new binary live on a host that was
// supposed to roll back.
func TestApplyRestoresTheOldBinaryWhenTheCallerCancels(t *testing.T) {
	archive := releaseArchive(t, "0.2.0", []byte("new-nexa-binary-bytes"), nil)
	runner := &fakeRunner{versionOutput: "0.2.0 (commit abc)"}
	operator, binaryPath := newTestOperator(t, "0.1.0", fakeSource{release: testRelease()}, fakeDownloader{assets: releaseAssets(archive)}, runner)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// The update dies the way a disconnected client or the apply timeout kills
	// it: after the new binary is live, before activation completes.
	operator.activate = func(context.Context, string) error {
		cancel()
		return context.Canceled
	}

	if _, err := operator.Apply(ctx, Change{}); err == nil {
		t.Fatal("expected the cancelled activation to fail the apply")
	}
	if data, _ := os.ReadFile(binaryPath); string(data) != "old-binary" {
		t.Fatalf("the previous binary must be live after a cancelled apply; got %q", data)
	}
	state, err := readTransaction(operator.transactionPath())
	if err != nil {
		t.Fatal(err)
	}
	if state.Phase != phaseFailed || !state.Result.RolledBack {
		t.Fatalf("a cancelled apply must journal a completed rollback: %+v", state)
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
	result, err := operator.ApplyLocalBinary(context.Background(), staged)
	if err != nil {
		t.Fatalf("apply local binary: %v", err)
	}
	if result.PackagingSynced {
		t.Fatal("a local binary push applies no packaging")
	}
	if !strings.Contains(result.PackagingNote, "complete signed release") {
		t.Fatalf("the note should direct the operator to a complete release, got %q", result.PackagingNote)
	}
	if sync := packagingSync(runner); sync != nil {
		t.Fatalf("no installer should run for a local push, got %+v", sync)
	}
}

// A shell script that exits zero is not a nexa binary. Installing one is fatal
// in a way no other bad candidate is: the activation helper is the newly
// installed binary, so it can neither activate nor roll itself back, and the
// node is left with the preserved binary unused and no working panel.
func TestLocalBinaryRefusesACandidateThatIsNotNexa(t *testing.T) {
	// A shell script that exits zero is not a nexa binary. Installing one is
	// fatal in a way no other bad candidate is: the activation helper is the
	// newly installed binary, so it can neither activate nor roll itself back,
	// and the node keeps a preserved binary it never uses.
	runner := &fakeRunner{versionOutput: "broken"}
	operator, binaryPath := newTestOperator(t, "0.1.0-dev", fakeSource{}, fakeDownloader{}, runner)
	live, err := os.ReadFile(binaryPath)
	if err != nil {
		t.Fatal(err)
	}

	staged := filepath.Join(t.TempDir(), "not-nexa")
	if err := os.WriteFile(staged, []byte("#!/bin/sh\necho broken\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	if _, err := operator.ApplyLocalBinary(context.Background(), staged); err == nil {
		t.Fatal("a candidate that is not a nexa binary must be refused")
	} else if !strings.Contains(err.Error(), "not a nexa binary") {
		t.Fatalf("error = %v, want it to name the cause", err)
	}
	if got, _ := os.ReadFile(binaryPath); string(got) != string(live) {
		t.Fatalf("the live binary must be untouched, got %q", got)
	}
}
