package selfupdate

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
	_ "modernc.org/sqlite"
)

const (
	transactionFileName  = "transaction.json"
	transactionDirName   = "transactions"
	defaultLifecycleLock = "/run/lock/nexa-panel-lifecycle.lock"
	apiSocketPath        = "/run/nexa-panel/api.sock"
	// defaultPackageStateDir exists only on a dpkg-managed host; package-manager
	// recovery is skipped entirely when it is absent.
	defaultPackageStateDir = "/var/lib/dpkg"
)

// restoreTimeout bounds every restore and rollback. A restore is frequently
// triggered by the very cancellation (or apply deadline) that killed the
// update, so it never inherits the caller's cancellation — it would then run
// its own `nexa version` exec on a dead context and leave the new binary
// installed on a host that was supposed to roll back.
const restoreTimeout = 3 * time.Minute

// restoreContext detaches ctx from its caller's cancellation while keeping its
// values, and bounds the result so a detached restore can still not hang.
func restoreContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), restoreTimeout)
}

type transactionPhase string

const (
	phasePrepared   transactionPhase = "prepared"
	phaseActivating transactionPhase = "activating"
	phaseSucceeded  transactionPhase = "succeeded"
	phaseFailed     transactionPhase = "failed"
)

type updateTransaction struct {
	ID                        string                   `json:"id"`
	TargetVersion             string                   `json:"targetVersion"`
	PreviousVersion           string                   `json:"previousVersion"`
	Phase                     transactionPhase         `json:"phase"`
	SnapshotDir               string                   `json:"snapshotDir,omitempty"`
	PreservedBinary           string                   `json:"preservedBinary"`
	DatabaseSnapshot          controlDatabaseSnapshot  `json:"databaseSnapshot"`
	ActivationDatabaseRestore *controlDatabaseSnapshot `json:"activationDatabaseRestore,omitempty"`
	// PackagingRunning records that the release installer — and therefore
	// dpkg/apt — was in flight when the journal was last written. The installer
	// runs in its own process group and is SIGKILLed on cancellation, so its
	// EXIT trap never runs; recovery uses this to repair the package manager.
	PackagingRunning bool      `json:"packagingRunning,omitempty"`
	Failure          string    `json:"failure,omitempty"`
	FailureReported  bool      `json:"failureReported,omitempty"`
	Result           Result    `json:"result"`
	CreatedAt        time.Time `json:"createdAt"`
	UpdatedAt        time.Time `json:"updatedAt"`
}

// controlDatabaseSnapshot describes a transaction-owned, consistent SQLite
// snapshot. Captured distinguishes "the database did not exist" from a journal
// created by an older build that did not protect database rollback at all.
type controlDatabaseSnapshot struct {
	Path     string      `json:"path,omitempty"`
	Captured bool        `json:"captured"`
	Existed  bool        `json:"existed"`
	Mode     os.FileMode `json:"mode,omitempty"`
	UID      int         `json:"uid,omitempty"`
	GID      int         `json:"gid,omitempty"`
}

type snapshotEntry struct {
	Path       string      `json:"path"`
	BackupName string      `json:"backupName,omitempty"`
	Mode       os.FileMode `json:"mode,omitempty"`
	UID        int         `json:"uid,omitempty"`
	GID        int         `json:"gid,omitempty"`
	LinkTarget string      `json:"linkTarget,omitempty"`
	Existed    bool        `json:"existed"`
}

type hostSnapshot struct {
	Entries []snapshotEntry `json:"entries"`
}

var defaultManagedPackagingPaths = []string{
	"/usr/lib/systemd/system/nexa-agent.service",
	"/usr/lib/systemd/system/nexa-api.service",
	"/usr/lib/systemd/system/nexa-panel-system-backup.service",
	"/usr/lib/systemd/system/nexa-panel-system-backup.timer",
	"/usr/lib/systemd/system/nexa-update-recovery.service",
	"/etc/systemd/system/multi-user.target.wants/nexa-update-recovery.service",
	"/usr/lib/sysusers.d/nexa-panel.conf",
	"/usr/lib/tmpfiles.d/nexa-panel.conf",
	"/etc/systemd/system/nexa-api.service.d/10-nexa-panel.conf",
	"/etc/systemd/system/nexa-api.service.d/10-insecure-http.conf",
	"/etc/nexa-panel/release-signers",
	"/etc/nginx/sites-available/nexa-panel.conf",
	"/etc/nginx/sites-enabled/nexa-panel.conf",
	"/etc/nginx/snippets/nexa-panel-proxy.conf",
	"/usr/lib/nexa-panel/uninstall.sh",
	"/usr/sbin/nexa-uninstall",
}

func (o *HostOperator) transactionPath() string {
	return filepath.Join(o.workRoot, transactionFileName)
}

func newTransactionID() (string, error) {
	raw := make([]byte, 12)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("create update transaction id: %w", err)
	}
	return hex.EncodeToString(raw), nil
}

func (o *HostOperator) acquireHostLock(ctx context.Context) (*os.File, func(), error) {
	if err := os.MkdirAll(o.workRoot, 0o700); err != nil {
		return nil, nil, fmt.Errorf("create the self-update work directory: %w", err)
	}
	if err := os.Chmod(o.workRoot, 0o700); err != nil {
		return nil, nil, fmt.Errorf("secure the self-update work directory: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(o.lockPath), 0o755); err != nil {
		return nil, nil, fmt.Errorf("create lifecycle lock directory: %w", err)
	}
	fd, err := unix.Open(o.lockPath, unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, nil, fmt.Errorf("open the self-update lock: %w", err)
	}
	lock := os.NewFile(uintptr(fd), o.lockPath)
	if lock == nil {
		_ = unix.Close(fd)
		return nil, nil, errors.New("open the self-update lock: invalid file descriptor")
	}
	if err := unix.Fchmod(fd, 0o600); err != nil {
		_ = lock.Close()
		return nil, nil, fmt.Errorf("secure the self-update lock: %w", err)
	}
	info, err := lock.Stat()
	if err != nil {
		_ = lock.Close()
		return nil, nil, fmt.Errorf("inspect the self-update lock: %w", err)
	}
	if !info.Mode().IsRegular() {
		_ = lock.Close()
		return nil, nil, errors.New("self-update lifecycle lock is not a regular file")
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok && os.Geteuid() == 0 && stat.Uid != 0 {
		_ = lock.Close()
		return nil, nil, errors.New("self-update lifecycle lock is not owned by root")
	}
	for {
		err = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return lock, func() {
				_ = unix.Flock(int(lock.Fd()), unix.LOCK_UN)
				_ = lock.Close()
			}, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			_ = lock.Close()
			return nil, nil, fmt.Errorf("lock self-update state: %w", err)
		}
		select {
		case <-ctx.Done():
			_ = lock.Close()
			return nil, nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func readTransaction(path string) (updateTransaction, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return updateTransaction{}, err
	}
	var transaction updateTransaction
	if err := json.Unmarshal(data, &transaction); err != nil {
		return updateTransaction{}, fmt.Errorf("decode update transaction: %w", err)
	}
	if transaction.ID == "" || transaction.TargetVersion == "" {
		return updateTransaction{}, errors.New("update transaction is incomplete")
	}
	return transaction, nil
}

func writeTransaction(path string, transaction updateTransaction) error {
	transaction.UpdatedAt = time.Now().UTC()
	encoded, err := json.MarshalIndent(transaction, "", "  ")
	if err != nil {
		return fmt.Errorf("encode update transaction: %w", err)
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create update transaction directory: %w", err)
	}
	temp, err := os.CreateTemp(directory, ".transaction-*")
	if err != nil {
		return fmt.Errorf("stage update transaction: %w", err)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(encoded); err != nil {
		_ = temp.Close()
		return fmt.Errorf("write update transaction: %w", err)
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return fmt.Errorf("flush update transaction: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close update transaction: %w", err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("publish update transaction: %w", err)
	}
	dir, err := os.Open(directory)
	if err == nil {
		err = dir.Sync()
		_ = dir.Close()
	}
	if err != nil {
		return fmt.Errorf("flush update transaction directory: %w", err)
	}
	return nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func (o *HostOperator) createHostSnapshot(transactionID string) (string, error) {
	directory := filepath.Join(o.workRoot, transactionDirName, transactionID, "snapshot")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", fmt.Errorf("create packaging snapshot: %w", err)
	}
	snapshot := hostSnapshot{Entries: make([]snapshotEntry, 0, len(o.managedPaths))}
	for index, path := range o.managedPaths {
		entry := snapshotEntry{Path: path}
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			snapshot.Entries = append(snapshot.Entries, entry)
			continue
		}
		if err != nil {
			return "", fmt.Errorf("snapshot %s: %w", path, err)
		}
		entry.Existed = true
		entry.Mode = info.Mode()
		if stat, ok := info.Sys().(*syscall.Stat_t); ok {
			entry.UID, entry.GID = int(stat.Uid), int(stat.Gid)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			entry.LinkTarget, err = os.Readlink(path)
			if err != nil {
				return "", fmt.Errorf("snapshot symlink %s: %w", path, err)
			}
		} else if info.Mode().IsRegular() {
			entry.BackupName = strconv.Itoa(index)
			if err := copyRegularFile(path, filepath.Join(directory, entry.BackupName), info.Mode().Perm()); err != nil {
				return "", fmt.Errorf("snapshot %s: %w", path, err)
			}
		} else {
			return "", fmt.Errorf("refuse to snapshot non-regular managed path %s", path)
		}
		snapshot.Entries = append(snapshot.Entries, entry)
	}
	manifest, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return "", err
	}
	manifestPath := filepath.Join(directory, "manifest.json")
	file, err := os.OpenFile(manifestPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return "", fmt.Errorf("write packaging snapshot manifest: %w", err)
	}
	if _, err := file.Write(manifest); err != nil {
		_ = file.Close()
		return "", fmt.Errorf("write packaging snapshot manifest: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return "", fmt.Errorf("flush packaging snapshot manifest: %w", err)
	}
	if err := file.Close(); err != nil {
		return "", fmt.Errorf("close packaging snapshot manifest: %w", err)
	}
	if err := syncDirectory(directory); err != nil {
		return "", fmt.Errorf("flush packaging snapshot directory: %w", err)
	}
	return directory, nil
}

func (o *HostOperator) preserveTransactionBinary(transactionID string) (string, error) {
	directory := filepath.Join(o.workRoot, transactionDirName, transactionID)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", fmt.Errorf("create binary snapshot directory: %w", err)
	}
	destination := filepath.Join(directory, "previous-nexa")
	info, err := os.Stat(o.binaryPath)
	if err != nil {
		return "", fmt.Errorf("inspect current binary: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("current nexa binary is not a regular file")
	}
	if err := copyRegularFile(o.binaryPath, destination, 0o700); err != nil {
		return "", fmt.Errorf("preserve current binary in transaction: %w", err)
	}
	return destination, nil
}

// createControlDatabaseSnapshot uses SQLite's VACUUM INTO rather than copying
// the three live database files. The API keeps control.db in WAL mode, so a raw
// control.db copy can omit committed pages that still live in -wal. VACUUM
// reads one consistent transaction and produces a self-contained database that
// can later replace control.db after the API is stopped.
func (o *HostOperator) createControlDatabaseSnapshot(ctx context.Context, transactionID string) (controlDatabaseSnapshot, error) {
	snapshot := controlDatabaseSnapshot{Captured: true}
	info, err := os.Lstat(o.databasePath)
	if errors.Is(err, os.ErrNotExist) {
		return snapshot, nil
	}
	if err != nil {
		return controlDatabaseSnapshot{}, fmt.Errorf("inspect control database: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return controlDatabaseSnapshot{}, errors.New("control database is not a regular file")
	}
	snapshot.Existed = true
	snapshot.Mode = info.Mode().Perm()
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		snapshot.UID, snapshot.GID = int(stat.Uid), int(stat.Gid)
	}

	directory := filepath.Join(o.workRoot, transactionDirName, transactionID)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return controlDatabaseSnapshot{}, fmt.Errorf("create database snapshot directory: %w", err)
	}
	destination := filepath.Join(directory, "previous-control.db")
	if _, err := os.Lstat(destination); err == nil {
		return controlDatabaseSnapshot{}, errors.New("control database snapshot already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return controlDatabaseSnapshot{}, fmt.Errorf("inspect control database snapshot: %w", err)
	}
	dsn := (&url.URL{Scheme: "file", Path: o.databasePath}).String() +
		"?_pragma=busy_timeout(5000)&mode=ro"
	database, err := sql.Open("sqlite", dsn)
	if err != nil {
		return controlDatabaseSnapshot{}, fmt.Errorf("open control database for snapshot: %w", err)
	}
	database.SetMaxOpenConns(1)
	quoted := "'" + strings.ReplaceAll(destination, "'", "''") + "'"
	if _, err := database.ExecContext(ctx, "VACUUM INTO "+quoted); err != nil {
		_ = database.Close()
		_ = os.Remove(destination)
		return controlDatabaseSnapshot{}, fmt.Errorf("snapshot control database: %w", err)
	}
	if err := database.Close(); err != nil {
		_ = os.Remove(destination)
		return controlDatabaseSnapshot{}, fmt.Errorf("close control database snapshot: %w", err)
	}
	if err := os.Chmod(destination, 0o600); err != nil {
		_ = os.Remove(destination)
		return controlDatabaseSnapshot{}, fmt.Errorf("secure control database snapshot: %w", err)
	}
	file, err := os.Open(destination)
	if err != nil {
		return controlDatabaseSnapshot{}, fmt.Errorf("open control database snapshot: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return controlDatabaseSnapshot{}, fmt.Errorf("flush control database snapshot: %w", err)
	}
	if err := file.Close(); err != nil {
		return controlDatabaseSnapshot{}, fmt.Errorf("close control database snapshot: %w", err)
	}
	if err := syncDirectory(directory); err != nil {
		return controlDatabaseSnapshot{}, fmt.Errorf("flush control database snapshot directory: %w", err)
	}
	snapshot.Path = destination
	return snapshot, nil
}

// restoreControlDatabase publishes a transaction's consistent snapshot and
// removes WAL/SHM sidecars from the stopped API. Old sidecars contain page
// numbers and salts for the migrated database and must never be replayed into
// the restored pre-migration file.
func (o *HostOperator) restoreControlDatabase(snapshot controlDatabaseSnapshot) error {
	if !snapshot.Captured {
		return errors.New("the update transaction has no control database snapshot")
	}
	for _, path := range []string{o.databasePath + "-wal", o.databasePath + "-shm"} {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove stale SQLite sidecar %s: %w", path, err)
		}
	}
	if !snapshot.Existed {
		if err := os.Remove(o.databasePath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove newly created control database: %w", err)
		}
		return syncDirectory(filepath.Dir(o.databasePath))
	}
	transactionRoot := filepath.Join(o.workRoot, transactionDirName)
	relative, err := filepath.Rel(transactionRoot, snapshot.Path)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("control database snapshot is outside the managed transaction directory")
	}
	info, err := os.Lstat(snapshot.Path)
	if err != nil {
		return fmt.Errorf("inspect control database snapshot: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return errors.New("control database snapshot is not a regular file")
	}
	mode := snapshot.Mode.Perm()
	if mode == 0 {
		mode = 0o600
	}
	if err := copyRegularFile(snapshot.Path, o.databasePath, mode); err != nil {
		return fmt.Errorf("restore control database: %w", err)
	}
	if err := os.Chown(o.databasePath, snapshot.UID, snapshot.GID); err != nil && os.Geteuid() == 0 {
		return fmt.Errorf("restore control database ownership: %w", err)
	}
	return nil
}

func restoreHostSnapshot(directory string) error {
	if directory == "" {
		return nil
	}
	manifest, err := os.ReadFile(filepath.Join(directory, "manifest.json"))
	if err != nil {
		return fmt.Errorf("read packaging snapshot: %w", err)
	}
	var snapshot hostSnapshot
	if err := json.Unmarshal(manifest, &snapshot); err != nil {
		return fmt.Errorf("decode packaging snapshot: %w", err)
	}
	for _, entry := range snapshot.Entries {
		if !filepath.IsAbs(entry.Path) {
			return fmt.Errorf("snapshot contains non-absolute path %q", entry.Path)
		}
		if err := os.Remove(entry.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("clear managed path %s: %w", entry.Path, err)
		}
		if !entry.Existed {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(entry.Path), 0o755); err != nil {
			return fmt.Errorf("restore parent of %s: %w", entry.Path, err)
		}
		if entry.LinkTarget != "" {
			if err := os.Symlink(entry.LinkTarget, entry.Path); err != nil {
				return fmt.Errorf("restore symlink %s: %w", entry.Path, err)
			}
			if err := os.Lchown(entry.Path, entry.UID, entry.GID); err != nil && os.Geteuid() == 0 {
				return fmt.Errorf("restore ownership of %s: %w", entry.Path, err)
			}
			continue
		}
		if err := copyRegularFile(filepath.Join(directory, entry.BackupName), entry.Path, entry.Mode.Perm()); err != nil {
			return fmt.Errorf("restore %s: %w", entry.Path, err)
		}
		if err := os.Chown(entry.Path, entry.UID, entry.GID); err != nil && os.Geteuid() == 0 {
			return fmt.Errorf("restore ownership of %s: %w", entry.Path, err)
		}
	}
	return nil
}

func copyRegularFile(source, destination string, mode os.FileMode) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(destination), ".copy-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if _, err := io.Copy(temp, input); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Chmod(mode); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempPath, destination); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(destination))
}

func (o *HostOperator) launchActivation(ctx context.Context, statePath, id string) error {
	if o.activate != nil {
		return o.activate(ctx, statePath)
	}
	unit := "nexa-panel-update-" + id
	output, err := o.runner.Run(ctx, Command{
		Name: "systemd-run",
		Args: []string{
			"--unit=" + unit,
			"--collect",
			"--property=Type=exec",
			o.binaryPath,
			"self-update", "activate", "--transaction", statePath,
		},
	})
	if err != nil {
		return fmt.Errorf("launch activation helper: %s: %w", lastLine(string(output)), err)
	}
	return nil
}

func (o *HostOperator) waitForActivation(ctx context.Context, statePath string) (Result, error) {
	ticker := time.NewTicker(o.activationPollInterval)
	defer ticker.Stop()
	for {
		state, err := readTransaction(statePath)
		if err != nil {
			return Result{}, err
		}
		switch state.Phase {
		case phaseSucceeded:
			return state.Result, nil
		case phaseFailed:
			if !state.FailureReported {
				state.FailureReported = true
				_ = writeTransaction(statePath, state)
			}
			return state.Result, errors.New(state.Failure)
		}
		select {
		case <-ctx.Done():
			// The activation helper is the newly installed binary. A candidate
			// that runs but is not really nexa never advances this journal, so
			// waiting simply expires — and returning here used to leave the bad
			// binary live with the preserved one untouched, which bricks the
			// node at its next restart. Recovery must never depend on the
			// artifact under test, so restore from the journal ourselves.
			return Result{}, o.recoverStalledActivation(statePath, ctx.Err())
		case <-ticker.C:
		}
	}
}

// recoverStalledActivation restores the preserved binary and managed files when
// the activation helper never reported an outcome. It runs on a context that
// outlives the cancelled one, because the usual cause is that deadline expiring.
func (o *HostOperator) recoverStalledActivation(statePath string, cause error) error {
	state, err := readTransaction(statePath)
	if err != nil {
		return errors.Join(cause, fmt.Errorf("read update journal for recovery: %w", err))
	}
	if state.Phase == phaseSucceeded || state.Phase == phaseFailed {
		return cause
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(context.Background()), restoreTimeout)
	defer cancel()
	restoreErr := o.restoreTransaction(ctx, state)
	state.Phase = phaseFailed
	state.Failure = cause.Error()
	state.FailureReported = true
	state.Result.RolledBack = restoreErr == nil
	if restoreErr != nil {
		state.Failure += "; restore previous state: " + restoreErr.Error()
	}
	_ = writeTransaction(statePath, state)
	if restoreErr != nil {
		return errors.Join(cause, restoreErr)
	}
	return fmt.Errorf("%w; the previous version was restored", cause)
}

// ActivateTransaction is the root-only detached helper entrypoint. It applies
// the new unit graph, starts the new agent and API, and proves readiness before
// publishing a successful transaction. On any failure it restores both the
// binary and every packaging file captured before the update.
func (o *HostOperator) ActivateTransaction(ctx context.Context, statePath string) error {
	if filepath.Clean(statePath) != filepath.Clean(o.transactionPath()) {
		return errors.New("activation transaction path is not the managed self-update journal")
	}
	_, unlock, err := o.acquireHostLock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	state, err := readTransaction(statePath)
	if err != nil {
		return err
	}
	if state.Phase == phaseSucceeded {
		return nil
	}
	if state.Phase != phaseActivating && state.Phase != phasePrepared {
		return fmt.Errorf("update transaction is in phase %s", state.Phase)
	}

	activateErr := o.prepareActivationDatabase(ctx, state)
	if activateErr == nil {
		activateErr = o.activateServices(ctx, state.TargetVersion)
	}
	if activateErr == nil {
		state.Phase = phaseSucceeded
		state.Result.Activated = true
		if err := writeTransaction(statePath, state); err != nil {
			return err
		}
		o.removeObsoleteTransactions(state.ID)
		return nil
	}

	rollbackErr := o.restoreTransaction(ctx, state)
	state.Phase = phaseFailed
	state.Failure = "activate updated services: " + activateErr.Error()
	if rollbackErr != nil {
		state.Failure += "; automatic rollback also failed: " + rollbackErr.Error()
	} else {
		state.Failure += "; the previous binary and packaging were restored"
		state.Result.RolledBack = true
		state.Result.Swapped = false
	}
	if err := writeTransaction(statePath, state); err != nil {
		return fmt.Errorf("%s; persist failure: %w", state.Failure, err)
	}
	return errors.New(state.Failure)
}

// prepareActivationDatabase is used by an explicit rollback transaction. The
// old schema snapshot is restored only after the currently running API has
// closed SQLite; normal forward updates leave the database live and let the new
// API migrate it during startup.
func (o *HostOperator) prepareActivationDatabase(ctx context.Context, state updateTransaction) error {
	if state.ActivationDatabaseRestore == nil {
		return nil
	}
	if err := o.stopAPI(ctx); err != nil {
		return err
	}
	if err := o.restoreControlDatabase(*state.ActivationDatabaseRestore); err != nil {
		return fmt.Errorf("restore rollback target database: %w", err)
	}
	return nil
}

func (o *HostOperator) removeObsoleteTransactions(currentID string) {
	root := filepath.Join(o.workRoot, transactionDirName)
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() && entry.Name() != currentID {
			_ = os.RemoveAll(filepath.Join(root, entry.Name()))
		}
	}
}

// RecoverTransaction reconciles an update interrupted by a host reboot. A
// prepared transaction never reached activation and is restored. An activating
// transaction resumes the same readiness-gated activation. Terminal journals
// are intentionally left in place as the rollback source.
func (o *HostOperator) RecoverTransaction(ctx context.Context, statePath string) error {
	if filepath.Clean(statePath) != filepath.Clean(o.transactionPath()) {
		return errors.New("recovery transaction path is not the managed self-update journal")
	}
	state, err := readTransaction(statePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	switch state.Phase {
	case phaseSucceeded, phaseFailed:
		return nil
	case phaseActivating:
		return o.ActivateTransaction(ctx, statePath)
	case phasePrepared:
		_, unlock, err := o.acquireHostLock(ctx)
		if err != nil {
			return err
		}
		defer unlock()
		// The installer was killed with its package manager mid-transaction, so
		// dpkg is repaired before anything else touches the host.
		packaging := ""
		if state.PackagingRunning {
			if err := o.recoverPackageManager(ctx); err != nil {
				packaging = "; the interrupted package manager could not be repaired: " + err.Error()
			}
		}
		if err := o.restorePreparedTransaction(ctx, state); err != nil {
			return err
		}
		state.PackagingRunning = false
		state.Phase = phaseFailed
		state.Failure = "host restarted before update activation; the previous binary and packaging were restored" + packaging
		state.FailureReported = false
		state.Result.RolledBack = true
		state.Result.Swapped = false
		return writeTransaction(statePath, state)
	default:
		return fmt.Errorf("unknown update transaction phase %q", state.Phase)
	}
}

func (o *HostOperator) activateServices(ctx context.Context, expectVersion string) error {
	commands := []Command{
		{Name: "systemctl", Args: []string{"daemon-reload"}},
		{Name: "nginx", Args: []string{"-t"}},
		{Name: "systemctl", Args: []string{"restart", "nexa-agent.service"}},
		{Name: "systemctl", Args: []string{"is-active", "--quiet", "nexa-agent.service"}},
		{Name: "systemctl", Args: []string{"restart", "nexa-api.service"}},
	}
	for _, command := range commands {
		if output, err := o.runner.Run(ctx, command); err != nil {
			return fmt.Errorf("%s %s: %s: %w", command.Name, strings.Join(command.Args, " "), lastLine(string(output)), err)
		}
	}
	if err := o.readiness(ctx, expectVersion); err != nil {
		return err
	}
	if output, err := o.runner.Run(ctx, Command{Name: "systemctl", Args: []string{"reload", "nginx.service"}}); err != nil {
		if fallback, restartErr := o.runner.Run(ctx, Command{Name: "systemctl", Args: []string{"restart", "nginx.service"}}); restartErr != nil {
			return fmt.Errorf("reload nginx: %s; restart nginx: %s: %w", lastLine(string(output)), lastLine(string(fallback)), restartErr)
		}
	}
	if output, err := o.runner.Run(ctx, Command{Name: "systemctl", Args: []string{"is-active", "--quiet", "nginx.service"}}); err != nil {
		return fmt.Errorf("verify nginx is active: %s: %w", lastLine(string(output)), err)
	}
	return nil
}

// recoverPackageManager repairs a dpkg/apt run the update interrupted. The
// release installer is executed in its own process group and killed with
// SIGKILL when the context dies, so neither its EXIT trap nor dpkg's own
// cleanup ever runs and the host can be left with half-configured packages that
// block every later install.
//
// It is deliberately narrow. It runs only when the journal recorded that the
// installer was actually in flight, only on a dpkg host, and always on a
// detached context — the interruption is usually the dead context itself.
// Failure is reported to the caller rather than aborting a restore: an
// unrepairable package manager is a host someone has to look at, but the
// previous binary and packaging still have to go back.
func (o *HostOperator) recoverPackageManager(caller context.Context) error {
	if info, err := os.Stat(o.packageStateDir); err != nil || !info.IsDir() {
		return nil
	}
	ctx, cancel := restoreContext(caller)
	defer cancel()
	for _, command := range []Command{
		{Name: "dpkg", Args: []string{"--configure", "-a"}, Env: []string{"DEBIAN_FRONTEND=noninteractive"}},
		{Name: "apt-get", Args: []string{"-y", "-f", "install"}, Env: []string{"DEBIAN_FRONTEND=noninteractive"}},
	} {
		if output, err := o.runner.Run(ctx, command); err != nil {
			return fmt.Errorf("%s %s: %s: %w", command.Name, strings.Join(command.Args, " "), lastLine(string(output)), err)
		}
	}
	return nil
}

func (o *HostOperator) stopAPI(ctx context.Context) error {
	output, err := o.runner.Run(ctx, Command{Name: "systemctl", Args: []string{"stop", "nexa-api.service"}})
	if err != nil {
		return fmt.Errorf("stop control plane before database restore: %s: %w", lastLine(string(output)), err)
	}
	return nil
}

// waitAPIReady polls the control plane's readiness endpoint over its unix
// socket until it reports ready. A 200 alone is not enough: the endpoint also
// names the version that answered it, and an activation that restarted the
// services without the new binary actually taking over would otherwise be
// reported successful. When expectVersion parses as a release version the
// reported version must equal it; a local push, whose "version" is a build
// string, asserts health only.
func waitAPIReady(ctx context.Context, socket, expectVersion string, timeout time.Duration) error {
	readyCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	expected := normalizeVersion(expectVersion)
	client := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}, Timeout: 3 * time.Second}
	mismatch := ""
	for {
		request, err := http.NewRequestWithContext(readyCtx, http.MethodGet, "http://unix/api/v1/health/ready", nil)
		if err == nil {
			response, requestErr := client.Do(request)
			if requestErr == nil {
				var report struct {
					Status  string `json:"status"`
					Version string `json:"version"`
				}
				body, _ := io.ReadAll(io.LimitReader(response.Body, 16*1024))
				_ = response.Body.Close()
				if response.StatusCode == http.StatusOK {
					_ = json.Unmarshal(body, &report)
					reported := normalizeVersion(report.Version)
					if expected == "" || reported == expected {
						return nil
					}
					// Keep polling: the old API may still be answering while
					// systemd brings the new one up, so only the deadline is
					// terminal. The last mismatch explains the failure.
					mismatch = fmt.Sprintf("the ready control plane reports version %q, not the expected %q", report.Version, expected)
				}
			}
		}
		select {
		case <-readyCtx.Done():
			if mismatch != "" {
				return errors.New(mismatch)
			}
			return fmt.Errorf("new control plane did not become ready: %w", readyCtx.Err())
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// restoreTransaction undoes an activation that failed its health gate. It runs
// on a detached context: the failure is often the caller's own cancellation or
// apply deadline, and a restore that inherited it could not even exec the
// binary it is restoring.
func (o *HostOperator) restoreTransaction(caller context.Context, state updateTransaction) error {
	ctx, cancel := restoreContext(caller)
	defer cancel()
	if err := o.stopAPI(ctx); err != nil {
		return err
	}
	previous := state.PreservedBinary
	info, err := os.Stat(previous)
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("no transaction-preserved previous binary is available")
	}
	binary, err := os.ReadFile(previous)
	if err != nil {
		return err
	}
	if _, err := o.installStagedBinary(ctx, binary, ""); err != nil {
		return err
	}
	if err := restoreHostSnapshot(state.SnapshotDir); err != nil {
		return err
	}
	if err := o.restoreControlDatabase(state.DatabaseSnapshot); err != nil {
		return err
	}
	for _, command := range []Command{
		{Name: "systemctl", Args: []string{"daemon-reload"}},
		{Name: "nginx", Args: []string{"-t"}},
		{Name: "systemctl", Args: []string{"restart", "nexa-agent.service", "nexa-api.service"}},
	} {
		if output, err := o.runner.Run(ctx, command); err != nil {
			return fmt.Errorf("restore services with %s: %s: %w", command.Name, lastLine(string(output)), err)
		}
	}
	// The restored control plane must report the version this transaction came
	// from; anything else means the old binary is not the one now serving.
	if err := o.readiness(ctx, state.PreviousVersion); err != nil {
		return err
	}
	return nil
}
