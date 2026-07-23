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
	"strings"
)

// Apply installs a signed release as a crash-recoverable host transaction. It
// does not return success until the detached activation helper has restarted the
// new services and the API readiness check has authenticated the new agent.
func (o *HostOperator) Apply(ctx context.Context, change Change) (Result, error) {
	o.applyMu.Lock()
	unlocked := false
	defer func() {
		if !unlocked {
			o.applyMu.Unlock()
		}
	}()
	hostLock, hostUnlock, err := o.acquireHostLock(ctx)
	if err != nil {
		return Result{}, err
	}
	hostUnlocked := false
	defer func() {
		if !hostUnlocked {
			hostUnlock()
		}
	}()

	requested := normalizeVersion(change.Version)
	if requested != "" {
		if current, err := readTransaction(o.transactionPath()); err == nil && current.TargetVersion == requested {
			switch current.Phase {
			case phaseSucceeded:
				hostUnlock()
				hostUnlocked = true
				o.applyMu.Unlock()
				unlocked = true
				return current.Result, nil
			case phasePrepared:
				recoveryErr := o.restorePreparedTransaction(ctx, current)
				current.Phase = phaseFailed
				current.FailureReported = true
				current.Failure = "an interrupted update was restored before activation"
				if recoveryErr != nil {
					current.Failure += "; recovery failed: " + recoveryErr.Error()
				} else {
					current.Result.RolledBack = true
					current.Result.Swapped = false
				}
				_ = writeTransaction(o.transactionPath(), current)
				return current.Result, errors.New(current.Failure)
			case phaseActivating:
				hostUnlock()
				hostUnlocked = true
				o.applyMu.Unlock()
				unlocked = true
				return o.waitForActivation(ctx, o.transactionPath())
			case phaseFailed:
				if !current.FailureReported {
					current.FailureReported = true
					_ = writeTransaction(o.transactionPath(), current)
					hostUnlock()
					hostUnlocked = true
					o.applyMu.Unlock()
					unlocked = true
					return current.Result, errors.New(current.Failure)
				}
			}
		}
	}

	result, statePath, err := o.applyRelease(ctx, change, hostLock)
	if err != nil {
		return result, err
	}
	hostUnlock()
	hostUnlocked = true
	o.applyMu.Unlock()
	unlocked = true
	return o.waitForActivation(ctx, statePath)
}

// applyRelease resolves, downloads, and verifies a release from the trusted
// repository, then installs it. Only strictly newer releases are accepted; the
// archive must match its published checksum, and the binary it carries must run
// and report the expected version.
//
// Packaging is applied before the live binary moves, after an exact snapshot of
// every managed destination. Any failure restores that snapshot while the old
// services and binary are still running; there is no half-upgraded success path.
func (o *HostOperator) applyRelease(ctx context.Context, change Change, lifecycleLock *os.File) (Result, string, error) {
	release, err := o.resolve(ctx, change)
	if err != nil {
		return Result{}, "", err
	}
	if !isNewer(release.Version, o.installed) {
		if release.Version == o.installed {
			return Result{}, "", fmt.Errorf("this node already runs version %s", o.installed)
		}
		return Result{}, "", fmt.Errorf("version %s is older than the installed %s; downgrades are not supported", release.Version, o.installed)
	}

	expected, err := o.expectedChecksum(ctx, release)
	if err != nil {
		return Result{}, "", err
	}
	archive, err := o.downloader.Fetch(ctx, release.AssetURL, maxArchiveBytes)
	if err != nil {
		return Result{}, "", err
	}
	if err := verifyChecksum(archive, expected); err != nil {
		return Result{}, "", err
	}
	signature, err := o.downloader.Fetch(ctx, release.SignatureURL, maxSignatureBytes)
	if err != nil {
		return Result{}, "", fmt.Errorf("download release signature: %w", err)
	}
	if err := o.verifyReleaseSignature(ctx, archive, signature); err != nil {
		return Result{}, "", err
	}

	staging, err := o.stage(archive)
	if err != nil {
		return Result{}, "", err
	}
	defer os.RemoveAll(staging)

	binary, err := readStagedBinary(filepath.Join(staging, filepath.FromSlash(releaseBinaryEntry)))
	if err != nil {
		return Result{}, "", err
	}
	reported, err := o.validateBinary(ctx, binary, release.Version)
	if err != nil {
		return Result{}, "", err
	}
	// Every statement of what this bundle is has to agree before its install.sh
	// runs as root. Signature and checksum prove the bytes are a genuine,
	// unmodified release; only this proves they are the release that was asked
	// for, built for this architecture.
	manifest, err := readReleaseManifest(filepath.Join(staging, filepath.FromSlash(releaseManifestEntry)))
	if err != nil {
		return Result{}, "", err
	}
	if err := verifyReleaseAgreement(release, manifest, o.arch, reported, expected, archive); err != nil {
		return Result{}, "", err
	}
	transactionID, err := newTransactionID()
	if err != nil {
		return Result{}, "", err
	}
	snapshotDir, err := o.createHostSnapshot(transactionID)
	if err != nil {
		return Result{}, "", err
	}
	preservedBinary, err := o.preserveTransactionBinary(transactionID)
	if err != nil {
		return Result{}, "", err
	}
	databaseSnapshot, err := o.createControlDatabaseSnapshot(ctx, transactionID)
	if err != nil {
		return Result{}, "", err
	}
	result := Result{
		PreviousVersion: o.installed,
		TargetVersion:   release.Version,
		PackagingSynced: false,
	}
	state := updateTransaction{
		ID:               transactionID,
		TargetVersion:    release.Version,
		PreviousVersion:  o.installed,
		Phase:            phasePrepared,
		SnapshotDir:      snapshotDir,
		PreservedBinary:  preservedBinary,
		DatabaseSnapshot: databaseSnapshot,
		Result:           result,
		CreatedAt:        o.now().UTC(),
	}
	statePath := o.transactionPath()
	// The journal records that the installer — and with it dpkg/apt — is about
	// to run, so a reboot or a kill during it is repairable at recovery.
	state.PackagingRunning = true
	if err := writeTransaction(statePath, state); err != nil {
		return Result{}, "", err
	}
	syncErr := o.syncPackaging(ctx, staging, lifecycleLock)
	state.PackagingRunning = false
	if syncErr != nil {
		err := syncErr
		// A cancelled context SIGKILLs the installer's whole process group, so
		// its EXIT trap never runs and dpkg can be left half-configured.
		if ctx.Err() != nil {
			if recoveryErr := o.recoverPackageManager(ctx); recoveryErr != nil {
				err = fmt.Errorf("%w; repair the interrupted package manager: %v", err, recoveryErr)
			}
		}
		restoreErr := restoreHostSnapshot(snapshotDir)
		state.Phase = phaseFailed
		state.Failure = err.Error()
		state.FailureReported = true
		state.Result.RolledBack = restoreErr == nil
		_ = writeTransaction(statePath, state)
		if restoreErr != nil {
			return Result{}, "", fmt.Errorf("%w; restore previous packaging: %v", err, restoreErr)
		}
		return Result{}, "", err
	}
	if _, err := o.installStagedBinary(ctx, binary, release.Version); err != nil {
		restoreErr := o.restorePreparedTransaction(ctx, state)
		state.Phase = phaseFailed
		state.Failure = err.Error()
		state.FailureReported = true
		state.Result.RolledBack = restoreErr == nil
		_ = writeTransaction(statePath, state)
		if restoreErr != nil {
			return Result{}, "", fmt.Errorf("%w; restore previous packaging: %v", err, restoreErr)
		}
		return Result{}, "", err
	}
	result.Swapped = true
	result.PackagingSynced = true
	state.Phase = phaseActivating
	state.Result = result
	if err := writeTransaction(statePath, state); err != nil {
		_ = o.restorePreparedTransaction(ctx, state)
		return Result{}, "", err
	}
	if err := o.launchActivation(ctx, statePath, transactionID); err != nil {
		restoreErr := o.restorePreparedTransaction(ctx, state)
		state.Phase = phaseFailed
		state.Failure = err.Error()
		state.FailureReported = true
		state.Result.RolledBack = restoreErr == nil
		if restoreErr != nil {
			state.Failure += "; restore previous state: " + restoreErr.Error()
		}
		_ = writeTransaction(statePath, state)
		return Result{}, "", err
	}
	return result, statePath, nil
}

// ApplyLocalBinary installs a binary an operator has already staged on the host
// (via scp/rsync, then `nexa self-update --binary PATH`). There is no download,
// no checksum, and no newer-than guard: pushing a build is an explicit act, so a
// same- or dev-version re-deploy is allowed. The path is validated only as an
// absolute, regular, size-bounded file, and the binary must run and report a
// version before it replaces the live one.
func (o *HostOperator) ApplyLocalBinary(ctx context.Context, path string) (Result, error) {
	o.applyMu.Lock()
	unlocked := false
	defer func() {
		if !unlocked {
			o.applyMu.Unlock()
		}
	}()
	_, hostUnlock, err := o.acquireHostLock(ctx)
	if err != nil {
		return Result{}, err
	}
	hostUnlocked := false
	defer func() {
		if !hostUnlocked {
			hostUnlock()
		}
	}()
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
	transactionID, err := newTransactionID()
	if err != nil {
		return Result{}, err
	}
	snapshotDir, err := o.createHostSnapshot(transactionID)
	if err != nil {
		return Result{}, err
	}
	preservedBinary, err := o.preserveTransactionBinary(transactionID)
	if err != nil {
		return Result{}, err
	}
	databaseSnapshot, err := o.createControlDatabaseSnapshot(ctx, transactionID)
	if err != nil {
		return Result{}, err
	}
	reported, err := o.validateBinary(ctx, binary, "")
	if err != nil {
		return Result{}, err
	}
	if reported == "" {
		reported = "the supplied binary"
	}
	result := Result{PreviousVersion: o.installed, TargetVersion: reported}
	result.PackagingNote = "only the binary was replaced; if this build changes systemd units, tmpfiles rules or the nginx template, install the complete signed release instead"
	state := updateTransaction{ID: transactionID, TargetVersion: reported, PreviousVersion: o.installed, Phase: phasePrepared, SnapshotDir: snapshotDir, PreservedBinary: preservedBinary, DatabaseSnapshot: databaseSnapshot, Result: result, CreatedAt: o.now().UTC()}
	statePath := o.transactionPath()
	if err := writeTransaction(statePath, state); err != nil {
		return Result{}, err
	}
	if _, err := o.installStagedBinary(ctx, binary, ""); err != nil {
		restoreErr := o.restorePreparedTransaction(ctx, state)
		state.Phase = phaseFailed
		state.Failure = err.Error()
		state.FailureReported = true
		state.Result.RolledBack = restoreErr == nil
		if restoreErr != nil {
			state.Failure += "; restore previous state: " + restoreErr.Error()
		}
		_ = writeTransaction(statePath, state)
		return Result{}, err
	}
	result.Swapped = true
	state.Phase = phaseActivating
	state.Result = result
	if err := writeTransaction(statePath, state); err != nil {
		_ = o.restorePreparedTransaction(ctx, state)
		return Result{}, err
	}
	if err := o.launchActivation(ctx, statePath, transactionID); err != nil {
		restoreErr := o.restorePreparedTransaction(ctx, state)
		state.Phase = phaseFailed
		state.Failure = err.Error()
		state.FailureReported = true
		state.Result.RolledBack = restoreErr == nil
		if restoreErr != nil {
			state.Failure += "; restore previous state: " + restoreErr.Error()
		}
		_ = writeTransaction(statePath, state)
		return Result{}, err
	}
	hostUnlock()
	hostUnlocked = true
	o.applyMu.Unlock()
	unlocked = true
	return o.waitForActivation(ctx, statePath)
}

// Rollback restores the exact binary and managed host files captured by the
// successful transaction. Before doing so it snapshots the current state, so
// the rollback is itself undoable. The same detached activation and readiness
// gate applies in both directions.
func (o *HostOperator) Rollback(ctx context.Context) (Result, error) {
	o.applyMu.Lock()
	unlocked := false
	defer func() {
		if !unlocked {
			o.applyMu.Unlock()
		}
	}()
	_, hostUnlock, err := o.acquireHostLock(ctx)
	if err != nil {
		return Result{}, err
	}
	hostUnlocked := false
	defer func() {
		if !hostUnlocked {
			hostUnlock()
		}
	}()
	current, err := readTransaction(o.transactionPath())
	if err != nil || current.Phase != phaseSucceeded {
		return Result{}, errors.New("no completed update transaction is available to roll back")
	}
	if !current.DatabaseSnapshot.Captured {
		return Result{}, errors.New("this update predates rollback-safe control database snapshots and cannot be rolled back automatically")
	}

	previous := current.PreservedBinary
	info, err := os.Stat(previous)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Result{}, errors.New("no transaction-preserved previous binary is available to roll back to")
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
	transactionID, err := newTransactionID()
	if err != nil {
		return Result{}, err
	}
	undoSnapshot, err := o.createHostSnapshot(transactionID)
	if err != nil {
		return Result{}, err
	}
	preservedCurrentBinary, err := o.preserveTransactionBinary(transactionID)
	if err != nil {
		return Result{}, err
	}
	databaseSnapshot, err := o.createControlDatabaseSnapshot(ctx, transactionID)
	if err != nil {
		return Result{}, err
	}
	targetVersion := current.PreviousVersion
	if targetVersion == "" {
		targetVersion = "the previous binary"
	}
	result := Result{PreviousVersion: current.TargetVersion, TargetVersion: targetVersion, PackagingSynced: true}
	targetDatabase := current.DatabaseSnapshot
	state := updateTransaction{ID: transactionID, TargetVersion: targetVersion, PreviousVersion: current.TargetVersion, Phase: phasePrepared, SnapshotDir: undoSnapshot, PreservedBinary: preservedCurrentBinary, DatabaseSnapshot: databaseSnapshot, ActivationDatabaseRestore: &targetDatabase, Result: result, CreatedAt: o.now().UTC()}
	statePath := o.transactionPath()
	if err := writeTransaction(statePath, state); err != nil {
		return Result{}, err
	}
	reported, err := o.installStagedBinary(ctx, binary, "")
	if err != nil {
		restoreErr := o.restorePreparedTransaction(ctx, state)
		state.Phase = phaseFailed
		state.Failure = err.Error()
		state.FailureReported = true
		state.Result.RolledBack = restoreErr == nil
		_ = writeTransaction(statePath, state)
		if restoreErr != nil {
			return Result{}, fmt.Errorf("%w; restore current state: %v", err, restoreErr)
		}
		return Result{}, err
	}
	if reported == "" {
		reported = "the previous binary"
	}
	if err := restoreHostSnapshot(current.SnapshotDir); err != nil {
		restoreErr := o.restorePreparedTransaction(ctx, state)
		state.Phase = phaseFailed
		state.Failure = err.Error()
		state.FailureReported = true
		state.Result.RolledBack = restoreErr == nil
		_ = writeTransaction(statePath, state)
		if restoreErr != nil {
			return Result{}, fmt.Errorf("restore previous packaging: %w; restore current state: %v", err, restoreErr)
		}
		return Result{}, fmt.Errorf("restore previous packaging: %w", err)
	}
	if current.PreviousVersion != "" {
		reported = current.PreviousVersion
	}
	result.TargetVersion = reported
	result.Swapped = true
	state.TargetVersion = reported
	state.Phase = phaseActivating
	state.Result = result
	if err := writeTransaction(statePath, state); err != nil {
		_ = o.restorePreparedTransaction(ctx, state)
		return Result{}, err
	}
	if err := o.launchActivation(ctx, statePath, transactionID); err != nil {
		restoreErr := o.restorePreparedTransaction(ctx, state)
		state.Phase = phaseFailed
		state.Failure = err.Error()
		state.FailureReported = true
		state.Result.RolledBack = restoreErr == nil
		_ = writeTransaction(statePath, state)
		return Result{}, err
	}
	hostUnlock()
	hostUnlocked = true
	o.applyMu.Unlock()
	unlocked = true
	return o.waitForActivation(ctx, statePath)
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

// validateBinary executes a staged copy without changing the live path. Release
// packaging is allowed to land only after this check has proved the artifact is
// executable and reports the version its signed metadata promised.
func (o *HostOperator) validateBinary(ctx context.Context, binary []byte, expectVersion string) (string, error) {
	directory := filepath.Dir(o.binaryPath)
	temp, err := os.CreateTemp(directory, ".nexa-validate-*")
	if err != nil {
		return "", fmt.Errorf("stage the new binary for validation: %w", err)
	}
	path := temp.Name()
	defer os.Remove(path)
	if _, err := temp.Write(binary); err != nil {
		_ = temp.Close()
		return "", fmt.Errorf("write the validation binary: %w", err)
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return "", fmt.Errorf("flush the validation binary: %w", err)
	}
	if err := temp.Close(); err != nil {
		return "", fmt.Errorf("close the validation binary: %w", err)
	}
	if err := os.Chmod(path, 0o755); err != nil {
		return "", fmt.Errorf("make the validation binary executable: %w", err)
	}
	output, err := o.runner.Run(ctx, Command{Name: path, Args: []string{"version"}})
	if err != nil {
		return "", fmt.Errorf("the binary is not a runnable nexa binary: %w", err)
	}
	if expectVersion != "" && !strings.Contains(string(output), expectVersion) {
		return "", errors.New("the binary reports an unexpected version")
	}
	// Exiting zero is not proof this is nexa: any script does that, and one that
	// does gets installed and then cannot activate or roll itself back, because
	// the activation helper is this very binary. Require the version line to
	// carry a version we recognise before it is allowed near /usr/bin/nexa.
	reported := firstField(string(output))
	if !isPlausibleVersion(reported) {
		return "", fmt.Errorf("the binary is not a nexa binary: %q is not a version", strings.TrimSpace(firstLine(string(output))))
	}
	return reported, nil
}

// restorePreparedTransaction is used before activation starts. No service has
// consumed the new unit graph yet, so restoring the binary and packaging files
// is sufficient and avoids bouncing a healthy old control plane.
//
// It runs on a context detached from the caller's: the failure it is undoing is
// frequently the caller's own cancellation or the apply deadline, and a restore
// that inherited that context could not exec the binary it is reinstalling —
// leaving the new binary live on a host that was supposed to roll back.
func (o *HostOperator) restorePreparedTransaction(caller context.Context, state updateTransaction) error {
	ctx, cancel := restoreContext(caller)
	defer cancel()
	previous, err := os.ReadFile(state.PreservedBinary)
	if err != nil {
		return fmt.Errorf("read the preserved binary: %w", err)
	}
	if _, err := o.installStagedBinary(ctx, previous, ""); err != nil {
		return fmt.Errorf("restore the preserved binary: %w", err)
	}
	if err := restoreHostSnapshot(state.SnapshotDir); err != nil {
		return fmt.Errorf("restore previous packaging: %w", err)
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
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		cleanup()
		return "", fmt.Errorf("flush the new binary: %w", err)
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
	if err := os.Rename(tempPath, o.binaryPath); err != nil {
		cleanup()
		return "", fmt.Errorf("install the new binary: %w", err)
	}
	if err := syncDirectory(directory); err != nil {
		return "", fmt.Errorf("flush the installed binary directory: %w", err)
	}
	return firstField(string(output)), nil
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
func (o *HostOperator) syncPackaging(ctx context.Context, tree string, lifecycleLock *os.File) error {
	if lifecycleLock == nil {
		return errors.New("apply release packaging without the lifecycle lock")
	}
	script := filepath.Join(tree, filepath.FromSlash(releaseInstallerEntry))
	output, err := o.runner.Run(ctx, Command{
		Name:       "/bin/bash",
		Args:       []string{script, releaseInstallerFlag, releaseInstallerNoStart},
		Env:        []string{"NEXA_LIFECYCLE_LOCK_FD=3"},
		ExtraFiles: []*os.File{lifecycleLock},
	})
	if err != nil {
		return fmt.Errorf("apply the release packaging: %s: %w", lastLine(string(output)), err)
	}
	output, err = o.runner.Run(ctx, Command{Name: "systemctl", Args: []string{"enable", "nexa-update-recovery.service"}})
	if err != nil {
		return fmt.Errorf("enable interrupted-update recovery: %s: %w", lastLine(string(output)), err)
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

// firstLine returns the first line of command output, for error messages that
// quote what a candidate binary actually printed.
func firstLine(output string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(output), "\n")
	return line
}
