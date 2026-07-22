package selfupdate

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Apply installs a new panel binary and arms a detached, delayed restart of both
// units, returning before that restart fires so the enclosing job can record
// success while the current process is still alive. It dispatches on the change:
// a BinaryPath installs a binary already staged on the host; otherwise the
// target release is downloaded from the trusted repository and verified first.
func (o *HostOperator) Apply(ctx context.Context, change Change) (Result, error) {
	o.applyMu.Lock()
	defer o.applyMu.Unlock()

	if strings.TrimSpace(change.BinaryPath) != "" {
		return o.applyLocalBinary(ctx, change.BinaryPath)
	}
	return o.applyRelease(ctx, change)
}

// applyRelease resolves, downloads, and verifies a release from the trusted
// repository, then installs it. Only strictly newer releases are accepted; the
// archive must match its published checksum, and the binary it carries must run
// and report the expected version.
//
// The order matters. The binary is swapped first and the packaging applied
// second, because the packaging describes the new binary: units, tmpfiles rules
// and prerequisites for a version that is not on disk yet would be the wrong
// half of the upgrade to land first. A packaging failure therefore leaves a
// node whose binary moved and whose units did not — that is reported as an
// error naming the rollback, not swallowed.
func (o *HostOperator) applyRelease(ctx context.Context, change Change) (Result, error) {
	release, err := o.resolve(ctx, change)
	if err != nil {
		return Result{}, err
	}
	if !isNewer(release.Version, o.installed) {
		if release.Version == o.installed {
			return Result{}, fmt.Errorf("this node already runs version %s", o.installed)
		}
		return Result{}, fmt.Errorf("version %s is older than the installed %s; downgrades are not supported", release.Version, o.installed)
	}

	expected, err := o.expectedChecksum(ctx, release)
	if err != nil {
		return Result{}, err
	}
	archive, err := o.downloader.Fetch(ctx, release.AssetURL, maxArchiveBytes)
	if err != nil {
		return Result{}, err
	}
	if err := verifyChecksum(archive, expected); err != nil {
		return Result{}, err
	}

	staging, err := o.stage(archive)
	if err != nil {
		return Result{}, err
	}
	// The staging tree is only removed on a failure; on success it is retained
	// as the node's "current" packaging so a later rollback has something to
	// restore.
	staged := false
	defer func() {
		if !staged {
			_ = os.RemoveAll(staging)
		}
	}()

	binary, err := readStagedBinary(filepath.Join(staging, filepath.FromSlash(releaseBinaryEntry)))
	if err != nil {
		return Result{}, err
	}
	if _, err := o.installStagedBinary(ctx, binary, release.Version); err != nil {
		return Result{}, err
	}
	if err := o.syncPackaging(ctx, staging); err != nil {
		return Result{
			PreviousVersion: o.installed,
			TargetVersion:   release.Version,
			Swapped:         true,
			PackagingNote:   "the binary is now " + release.Version + " but its packaging was not applied; run `nexa self-update rollback` or re-run the release installer by hand",
		}, err
	}
	if err := o.retainPackaging(staging); err != nil {
		return Result{}, err
	}
	staged = true

	result, err := o.finishSwap(ctx, release.Version)
	result.PackagingSynced = true
	return result, err
}

// applyLocalBinary installs a binary an operator has already staged on the host
// (via scp/rsync, then `nexa self-update --binary PATH`). There is no download,
// no checksum, and no newer-than guard: pushing a build is an explicit act, so a
// same- or dev-version re-deploy is allowed. The path is validated only as an
// absolute, regular, size-bounded file, and the binary must run and report a
// version before it replaces the live one.
func (o *HostOperator) applyLocalBinary(ctx context.Context, path string) (Result, error) {
	if !filepath.IsAbs(path) {
		return Result{}, errors.New("the binary path must be absolute")
	}
	info, err := os.Stat(path)
	if err != nil {
		return Result{}, fmt.Errorf("the binary at %s could not be read: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return Result{}, fmt.Errorf("%s is not a regular file", path)
	}
	if info.Size() > maxBinaryBytes {
		return Result{}, fmt.Errorf("the binary at %s exceeds the %d byte limit", path, maxBinaryBytes)
	}
	binary, err := os.ReadFile(path)
	if err != nil {
		return Result{}, fmt.Errorf("the binary at %s could not be read: %w", path, err)
	}
	reported, err := o.installStagedBinary(ctx, binary, "")
	if err != nil {
		return Result{}, err
	}
	if reported == "" {
		reported = "the supplied binary"
	}
	result, err := o.finishSwap(ctx, reported)
	// A local push is a binary, not a release: there is no packaging tree to
	// apply. Say so rather than let the caller assume a full upgrade.
	result.PackagingNote = "only the binary was replaced; if this build changes systemd units, tmpfiles rules or the nginx template, run its scripts/install.sh " + releaseInstallerFlag
	return result, err
}

// Rollback reinstalls the binary preserved by the previous swap, and, when the
// release that binary came from is still retained, re-applies its packaging too.
// It validates the preserved binary runs before the atomic replacement — which
// itself re-preserves the now-current binary as the new .prev, and swaps the two
// retained packaging trees, so a rollback can be undone — then arms the same
// detached restart as a normal apply. It errors when no previous binary is
// available.
//
// Packaging is rolled back with the binary rather than left alone because the
// two are one artifact: reverting only /usr/bin/nexa onto units, tmpfiles rules
// and an nginx template from the newer release is the same half-upgrade this
// operator exists to prevent, just in the other direction. When there is no
// retained previous tree — the node's first self-update, where the packaging it
// came with was never captured — the binary is still restored, and the Result
// says plainly that the packaging was not.
func (o *HostOperator) Rollback(ctx context.Context) (Result, error) {
	o.applyMu.Lock()
	defer o.applyMu.Unlock()

	previous := o.previousBinaryPath()
	info, err := os.Stat(previous)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Result{}, errors.New("no previous binary is available to roll back to")
		}
		return Result{}, fmt.Errorf("the previous binary at %s could not be read: %w", previous, err)
	}
	if !info.Mode().IsRegular() {
		return Result{}, fmt.Errorf("%s is not a regular file", previous)
	}
	binary, err := os.ReadFile(previous)
	if err != nil {
		return Result{}, fmt.Errorf("the previous binary at %s could not be read: %w", previous, err)
	}
	reported, err := o.installStagedBinary(ctx, binary, "")
	if err != nil {
		return Result{}, err
	}
	if reported == "" {
		reported = "the previous binary"
	}
	synced, note, err := o.rollbackPackaging(ctx)
	if err != nil {
		return Result{
			PreviousVersion: o.installed,
			TargetVersion:   reported,
			Swapped:         true,
			PackagingNote:   "the previous binary is back in place but its packaging could not be re-applied; re-run the release installer by hand",
		}, err
	}
	result, err := o.finishSwap(ctx, reported)
	result.PackagingSynced = synced
	result.PackagingNote = note
	return result, err
}

// rollbackPackaging re-applies the retained previous release tree and swaps it
// with the current one so the rollback is itself undoable. It reports whether
// packaging moved, and the note the caller must show when it did not.
func (o *HostOperator) rollbackPackaging(ctx context.Context) (bool, string, error) {
	previous := filepath.Join(o.workRoot, previousPackagingDir)
	if _, err := os.Stat(filepath.Join(previous, filepath.FromSlash(releaseInstallerEntry))); err != nil {
		return false, "only the binary was rolled back: this node has no retained packaging for the previous version, so its systemd units, tmpfiles rules and nginx template are still the newer release's", nil
	}
	if err := o.syncPackaging(ctx, previous); err != nil {
		return false, "", err
	}
	if err := o.swapRetainedPackaging(); err != nil {
		return false, "", err
	}
	return true, "", nil
}

// finishSwap arms the restart and assembles the Result after a successful swap.
// The swap has already happened, so a restart-scheduling failure is reported but
// still returns Swapped: the operator can bounce the units by hand.
func (o *HostOperator) finishSwap(ctx context.Context, targetVersion string) (Result, error) {
	result := Result{PreviousVersion: o.installed, TargetVersion: targetVersion, Swapped: true}
	// Surface whether a rollback target exists so callers can offer it.
	if info, err := os.Stat(o.previousBinaryPath()); err == nil && info.Mode().IsRegular() {
		result.PreviousBinaryPath = o.previousBinaryPath()
	}
	if err := o.scheduleRestart(ctx); err != nil {
		return result, fmt.Errorf("binary was updated to %s but the automatic restart could not be scheduled: %w", targetVersion, err)
	}
	result.RestartScheduled = true
	result.RestartDelay = o.restartDelay.String()
	return result, nil
}

// resolve turns a change into a concrete release, gating an explicit version
// through the strict shape before it becomes a release tag.
func (o *HostOperator) resolve(ctx context.Context, change Change) (Release, error) {
	requested := strings.TrimSpace(change.Version)
	if requested == "" {
		return o.source.Latest(ctx, o.arch)
	}
	version := normalizeVersion(requested)
	if version == "" {
		return Release{}, fmt.Errorf("%q is not a valid version", requested)
	}
	return o.source.ByVersion(ctx, o.arch, version)
}

// expectedChecksum downloads and parses the checksum sidecar, returning the
// lowercase hex SHA-256 the binary must match.
func (o *HostOperator) expectedChecksum(ctx context.Context, release Release) (string, error) {
	raw, err := o.downloader.Fetch(ctx, release.ChecksumURL, maxChecksumBytes)
	if err != nil {
		return "", err
	}
	return parseChecksum(string(raw))
}

// parseChecksum reads the first whitespace-delimited token of a `sha256sum`
// style file and validates it as a 64-character hex digest.
func parseChecksum(contents string) (string, error) {
	fields := strings.Fields(contents)
	if len(fields) == 0 {
		return "", fmt.Errorf("release checksum file is empty")
	}
	digest := strings.ToLower(fields[0])
	if len(digest) != sha256.Size*2 {
		return "", fmt.Errorf("release checksum is not a SHA-256 digest")
	}
	if _, err := hex.DecodeString(digest); err != nil {
		return "", fmt.Errorf("release checksum is not valid hex")
	}
	return digest, nil
}

// verifyChecksum computes the SHA-256 of the downloaded binary and compares it
// to the expected digest in constant time.
func verifyChecksum(binary []byte, expected string) error {
	sum := sha256.Sum256(binary)
	actual := hex.EncodeToString(sum[:])
	if subtle.ConstantTimeCompare([]byte(actual), []byte(expected)) != 1 {
		return fmt.Errorf("downloaded binary failed checksum verification")
	}
	return nil
}

// installStagedBinary writes bytes to a sibling temp file, validates the binary
// runs (and, when expectVersion is non-empty, that it reports that version),
// then atomically renames it over the target. The rename is atomic because the
// temp file shares the target's directory, and thus its filesystem; any failure
// before the rename removes the temp file. It returns the version the staged
// binary reported, so a local push can name what it installed.
func (o *HostOperator) installStagedBinary(ctx context.Context, binary []byte, expectVersion string) (string, error) {
	directory := filepath.Dir(o.binaryPath)
	temp, err := os.CreateTemp(directory, ".nexa-update-*")
	if err != nil {
		return "", fmt.Errorf("stage the new binary: %w", err)
	}
	tempPath := temp.Name()
	cleanup := func() { _ = os.Remove(tempPath) }

	if _, err := temp.Write(binary); err != nil {
		_ = temp.Close()
		cleanup()
		return "", fmt.Errorf("write the new binary: %w", err)
	}
	if err := temp.Close(); err != nil {
		cleanup()
		return "", fmt.Errorf("flush the new binary: %w", err)
	}
	if err := os.Chmod(tempPath, 0o755); err != nil {
		cleanup()
		return "", fmt.Errorf("make the new binary executable: %w", err)
	}
	output, err := o.runner.Run(ctx, Command{Name: tempPath, Args: []string{"version"}})
	if err != nil {
		cleanup()
		return "", fmt.Errorf("the binary is not a runnable nexa binary: %w", err)
	}
	if expectVersion != "" && !strings.Contains(string(output), expectVersion) {
		cleanup()
		return "", fmt.Errorf("the binary reports an unexpected version")
	}
	// Preserve the binary being replaced before overwriting it, so a bad upgrade
	// can be rolled back. A failure here aborts the swap: an un-rollbackable
	// upgrade must never happen silently.
	if err := o.preservePreviousBinary(); err != nil {
		cleanup()
		return "", err
	}
	if err := os.Rename(tempPath, o.binaryPath); err != nil {
		cleanup()
		return "", fmt.Errorf("install the new binary: %w", err)
	}
	return firstField(string(output)), nil
}

// previousBinaryPath is where the binary being replaced is preserved on each
// swap, enabling `nexa self-update rollback`.
func (o *HostOperator) previousBinaryPath() string {
	return o.binaryPath + ".prev"
}

// preservePreviousBinary copies the binary currently at o.binaryPath aside to
// o.previousBinaryPath() before it is overwritten, retaining a rollback target.
// It is a no-op when no binary is installed yet (a first install). The copy is
// staged and atomically renamed so a crash mid-copy never leaves a truncated
// .prev in place.
func (o *HostOperator) preservePreviousBinary() error {
	current, err := os.ReadFile(o.binaryPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read the current binary to preserve it: %w", err)
	}
	directory := filepath.Dir(o.binaryPath)
	temp, err := os.CreateTemp(directory, ".nexa-prev-*")
	if err != nil {
		return fmt.Errorf("stage the previous binary: %w", err)
	}
	tempPath := temp.Name()
	cleanup := func() { _ = os.Remove(tempPath) }
	if _, err := temp.Write(current); err != nil {
		_ = temp.Close()
		cleanup()
		return fmt.Errorf("write the previous binary: %w", err)
	}
	if err := temp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("flush the previous binary: %w", err)
	}
	if err := os.Chmod(tempPath, 0o755); err != nil {
		cleanup()
		return fmt.Errorf("make the previous binary executable: %w", err)
	}
	if err := os.Rename(tempPath, o.previousBinaryPath()); err != nil {
		cleanup()
		return fmt.Errorf("preserve the previous binary: %w", err)
	}
	return nil
}

// stage extracts a verified release archive into a fresh directory under the
// work root and returns that directory. The work root is created 0700 and
// root-owned: the tree holds a scripts/install.sh this operator later executes
// as root, so no other account may be able to reach or rewrite it.
func (o *HostOperator) stage(archive []byte) (string, error) {
	if err := os.MkdirAll(o.workRoot, 0o700); err != nil {
		return "", fmt.Errorf("create the self-update work directory: %w", err)
	}
	// MkdirAll honours the umask, and the agent runs with UMask=0177; the mode
	// is therefore set explicitly rather than assumed.
	if err := os.Chmod(o.workRoot, 0o700); err != nil {
		return "", fmt.Errorf("secure the self-update work directory: %w", err)
	}
	staging, err := os.MkdirTemp(o.workRoot, "staging-")
	if err != nil {
		return "", fmt.Errorf("create the release staging directory: %w", err)
	}
	if err := extractRelease(archive, staging); err != nil {
		_ = os.RemoveAll(staging)
		return "", err
	}
	return staging, nil
}

// readStagedBinary reads the nexa binary out of an extracted release tree,
// applying the same size ceiling as a download so a wildly oversized member
// cannot be loaded whole.
func readStagedBinary(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("the release archive's %s could not be read: %w", releaseBinaryEntry, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("the release archive's %s is not a regular file", releaseBinaryEntry)
	}
	if info.Size() > maxBinaryBytes {
		return nil, fmt.Errorf("the release archive's %s exceeds the %d byte limit", releaseBinaryEntry, maxBinaryBytes)
	}
	binary, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("the release archive's %s could not be read: %w", releaseBinaryEntry, err)
	}
	return binary, nil
}

// syncPackaging runs the release's own installer in its packaging-only mode,
// which re-applies systemd units, tmpfiles and sysusers rules, the nginx
// template and any newly required host prerequisites. The binary is already in
// place by then, so the installer is not asked to touch it, and --no-start
// keeps it from restarting the agent out from under this very call.
//
// It is invoked through bash by path rather than executed directly: the work
// root lives under /var, which a hardened node may well mount noexec.
func (o *HostOperator) syncPackaging(ctx context.Context, tree string) error {
	script := filepath.Join(tree, filepath.FromSlash(releaseInstallerEntry))
	output, err := o.runner.Run(ctx, Command{Name: "/bin/bash", Args: []string{script, releaseInstallerFlag, releaseInstallerNoStart}})
	if err != nil {
		return fmt.Errorf("apply the release packaging: %s: %w", lastLine(string(output)), err)
	}
	return nil
}

// retainPackaging promotes a freshly applied staging tree to "current",
// demoting whatever was current to "previous" — the tree a rollback re-applies.
// Only those two generations are kept; anything older is removed.
func (o *HostOperator) retainPackaging(staging string) error {
	current := filepath.Join(o.workRoot, currentPackagingDir)
	previous := filepath.Join(o.workRoot, previousPackagingDir)
	if err := os.RemoveAll(previous); err != nil {
		return fmt.Errorf("clear the retained previous packaging: %w", err)
	}
	if _, err := os.Stat(current); err == nil {
		if err := os.Rename(current, previous); err != nil {
			return fmt.Errorf("retain the displaced packaging: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect the retained packaging: %w", err)
	}
	if err := os.Rename(staging, current); err != nil {
		return fmt.Errorf("retain the applied packaging: %w", err)
	}
	return nil
}

// swapRetainedPackaging exchanges the current and previous trees after a
// rollback has re-applied the previous one, mirroring how the swap re-preserves
// the displaced binary as the new .prev: whatever was rolled back to is now
// current, and what was rolled away from is what a second rollback would
// restore.
func (o *HostOperator) swapRetainedPackaging() error {
	current := filepath.Join(o.workRoot, currentPackagingDir)
	previous := filepath.Join(o.workRoot, previousPackagingDir)
	interim := filepath.Join(o.workRoot, ".swapping")
	if err := os.RemoveAll(interim); err != nil {
		return fmt.Errorf("clear the packaging swap directory: %w", err)
	}
	if err := os.Rename(previous, interim); err != nil {
		return fmt.Errorf("swap the retained packaging: %w", err)
	}
	if err := os.Rename(current, previous); err != nil {
		return fmt.Errorf("swap the retained packaging: %w", err)
	}
	if err := os.Rename(interim, current); err != nil {
		return fmt.Errorf("swap the retained packaging: %w", err)
	}
	return nil
}

// lastLine returns the final non-empty line of command output, which for a
// failing shell installer is the message that explains the failure. Full output
// is not surfaced: it is long, and it is not the operator's error to read.
func lastLine(output string) string {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	for index := len(lines) - 1; index >= 0; index-- {
		if line := strings.TrimSpace(lines[index]); line != "" {
			return line
		}
	}
	return "no output"
}

// firstField returns the first whitespace-delimited token of s, which for the
// `nexa version` output ("X.Y.Z (commit …, built …)") is the version.
func firstField(s string) string {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

// scheduleRestart arms a transient, detached systemd timer that restarts both
// units after restartDelay. Running it detached is essential: a synchronous
// `systemctl restart nexa-agent` would kill this very process mid-RPC, before
// the enclosing job could record the result.
func (o *HostOperator) scheduleRestart(ctx context.Context) error {
	seconds := int(o.restartDelay.Seconds())
	if seconds < 1 {
		seconds = 1
	}
	args := []string{
		"--collect",
		"--on-active=" + strconv.Itoa(seconds),
		"systemctl", "restart",
	}
	args = append(args, managedUnits...)
	if output, err := o.runner.Run(ctx, Command{Name: "systemd-run", Args: args}); err != nil {
		return fmt.Errorf("schedule restart: %s: %w", strings.TrimSpace(string(output)), err)
	}
	return nil
}
