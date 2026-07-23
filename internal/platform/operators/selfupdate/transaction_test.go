package selfupdate

import (
	"context"
	"database/sql"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func preparedTransactionFixture(t *testing.T) (*HostOperator, *fakeRunner, string, string, updateTransaction) {
	t.Helper()
	runner := &fakeRunner{versionOutput: "0.1.0 (commit old)"}
	operator, binaryPath := newTestOperator(t, "0.1.0", fakeSource{}, fakeDownloader{}, runner)
	managedPath := filepath.Join(t.TempDir(), "nexa-api.service")
	if err := os.WriteFile(managedPath, []byte("old-packaging"), 0o644); err != nil {
		t.Fatal(err)
	}
	operator.managedPaths = []string{managedPath}
	id, err := newTransactionID()
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := operator.createHostSnapshot(id)
	if err != nil {
		t.Fatal(err)
	}
	preserved, err := operator.preserveTransactionBinary(id)
	if err != nil {
		t.Fatal(err)
	}
	state := updateTransaction{
		ID: id, TargetVersion: "0.2.0", PreviousVersion: "0.1.0",
		Phase: phaseActivating, SnapshotDir: snapshot, PreservedBinary: preserved,
		DatabaseSnapshot: controlDatabaseSnapshot{Captured: true},
		Result:           Result{PreviousVersion: "0.1.0", TargetVersion: "0.2.0", Swapped: true},
	}
	if err := writeTransaction(operator.transactionPath(), state); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binaryPath, []byte("new-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(managedPath, []byte("new-packaging"), 0o644); err != nil {
		t.Fatal(err)
	}
	return operator, runner, binaryPath, managedPath, state
}

func TestActivationPublishesSuccessOnlyAfterReadiness(t *testing.T) {
	operator, _, _, _, _ := preparedTransactionFixture(t)
	ready := false
	expected := ""
	operator.readiness = func(_ context.Context, expectVersion string) error {
		ready = true
		expected = expectVersion
		return nil
	}

	if err := operator.ActivateTransaction(context.Background(), operator.transactionPath()); err != nil {
		t.Fatalf("activate: %v", err)
	}
	// The health gate is only meaningful if it asserts the version the update
	// was for; a restart that left the old binary serving must not pass.
	if expected != "0.2.0" {
		t.Fatalf("readiness was asked for version %q, not the transaction target", expected)
	}
	state, err := readTransaction(operator.transactionPath())
	if err != nil {
		t.Fatal(err)
	}
	if !ready || state.Phase != phaseSucceeded || !state.Result.Activated {
		t.Fatalf("activation was published before readiness: ready=%v state=%+v", ready, state)
	}
}

func TestActivationFailureRestoresBinaryAndPackaging(t *testing.T) {
	operator, _, binaryPath, managedPath, _ := preparedTransactionFixture(t)
	databasePath := filepath.Join(t.TempDir(), "control.db")
	databaseSnapshotPath := filepath.Join(operator.workRoot, transactionDirName, "database-snapshot")
	if err := os.MkdirAll(filepath.Dir(databaseSnapshotPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(databaseSnapshotPath, []byte("old-database"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(databasePath, []byte("migrated-database"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if err := os.WriteFile(databasePath+suffix, []byte("stale"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	operator.databasePath = databasePath
	state, err := readTransaction(operator.transactionPath())
	if err != nil {
		t.Fatal(err)
	}
	state.DatabaseSnapshot = controlDatabaseSnapshot{Path: databaseSnapshotPath, Captured: true, Existed: true, Mode: 0o600, UID: os.Getuid(), GID: os.Getgid()}
	if err := writeTransaction(operator.transactionPath(), state); err != nil {
		t.Fatal(err)
	}
	checks := 0
	restoreExpected := ""
	operator.readiness = func(_ context.Context, expectVersion string) error {
		checks++
		if checks == 1 {
			return errors.New("new API never became ready")
		}
		restoreExpected = expectVersion
		return nil
	}

	if err := operator.ActivateTransaction(context.Background(), operator.transactionPath()); err == nil {
		t.Fatal("expected failed activation")
	}
	if data, _ := os.ReadFile(binaryPath); string(data) != "old-binary" {
		t.Fatalf("binary after rollback = %q", data)
	}
	if data, _ := os.ReadFile(managedPath); string(data) != "old-packaging" {
		t.Fatalf("packaging after rollback = %q", data)
	}
	if data, _ := os.ReadFile(databasePath); string(data) != "old-database" {
		t.Fatalf("database after rollback = %q", data)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(databasePath + suffix); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("stale SQLite sidecar %s survived rollback: %v", suffix, err)
		}
	}
	state, err = readTransaction(operator.transactionPath())
	if err != nil {
		t.Fatal(err)
	}
	if state.Phase != phaseFailed || state.Failure == "" || state.Result.Activated || !state.Result.RolledBack || state.Result.Swapped {
		t.Fatalf("failed activation journal = %+v", state)
	}
	// The restored control plane is health-checked against the version it was
	// rolled back to, not the target that failed.
	if restoreExpected != "0.1.0" {
		t.Fatalf("rollback readiness was asked for version %q", restoreExpected)
	}
}

// TestActivationRollbackSurvivesACancelledCaller proves the rollback does not
// inherit the cancellation that caused it: the activation helper's context is
// frequently already dead by the time the health gate fails.
func TestActivationRollbackSurvivesACancelledCaller(t *testing.T) {
	operator, _, binaryPath, managedPath, _ := preparedTransactionFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	checks := 0
	operator.readiness = func(ctx context.Context, _ string) error {
		checks++
		if checks == 1 {
			// The health gate fails because the caller went away mid-activation.
			cancel()
			return context.Canceled
		}
		return ctx.Err()
	}

	if err := operator.ActivateTransaction(ctx, operator.transactionPath()); err == nil {
		t.Fatal("expected the cancelled activation to fail")
	}
	if data, _ := os.ReadFile(binaryPath); string(data) != "old-binary" {
		t.Fatalf("binary after cancelled activation = %q", data)
	}
	if data, _ := os.ReadFile(managedPath); string(data) != "old-packaging" {
		t.Fatalf("packaging after cancelled activation = %q", data)
	}
	state, err := readTransaction(operator.transactionPath())
	if err != nil {
		t.Fatal(err)
	}
	if !state.Result.RolledBack || state.Result.Swapped {
		t.Fatalf("cancelled activation journal = %+v", state)
	}
}

// TestWaitAPIReadyRequiresTheExpectedVersion closes the health-gate gap: a 200
// from a readiness endpoint the *old* binary is still serving must not count as
// a successful activation.
func TestWaitAPIReadyRequiresTheExpectedVersion(t *testing.T) {
	directory, err := os.MkdirTemp("", "nexa-ready")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(directory)
	socket := filepath.Join(directory, "api.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ready","version":"0.1.0"}`))
	})}
	go func() { _ = server.Serve(listener) }()
	defer server.Close()

	if err := waitAPIReady(context.Background(), socket, "0.2.0", 750*time.Millisecond); err == nil || !strings.Contains(err.Error(), "0.1.0") {
		t.Fatalf("a ready old control plane must fail the new version's gate, got %v", err)
	}
	if err := waitAPIReady(context.Background(), socket, "0.1.0", 5*time.Second); err != nil {
		t.Fatalf("the expected version must pass: %v", err)
	}
	// A local binary push has no release version to assert; health alone gates it.
	if err := waitAPIReady(context.Background(), socket, "the supplied binary", 5*time.Second); err != nil {
		t.Fatalf("a non-release version must assert health only: %v", err)
	}
}

// TestBootRecoveryRepairsAnInterruptedPackageManager covers the installer being
// SIGKILLed with its process group: dpkg is left half-configured, and nothing
// else on the host can install anything until it is repaired.
func TestBootRecoveryRepairsAnInterruptedPackageManager(t *testing.T) {
	operator, runner, _, _, state := preparedTransactionFixture(t)
	operator.packageStateDir = t.TempDir()
	state.Phase = phasePrepared
	state.PackagingRunning = true
	if err := writeTransaction(operator.transactionPath(), state); err != nil {
		t.Fatal(err)
	}

	if err := operator.RecoverTransaction(context.Background(), operator.transactionPath()); err != nil {
		t.Fatalf("recover: %v", err)
	}
	if !ranCommand(runner, "dpkg", "--configure", "-a") || !ranCommand(runner, "apt-get", "-y", "-f", "install") {
		t.Fatalf("interrupted package manager was not repaired: %+v", runner.commands)
	}
	recovered, err := readTransaction(operator.transactionPath())
	if err != nil {
		t.Fatal(err)
	}
	if recovered.PackagingRunning {
		t.Fatalf("recovered journal still claims packaging is running: %+v", recovered)
	}
}

// TestRecoveryLeavesANonDpkgHostAlone keeps the repair narrow: a host without a
// dpkg state directory must never see apt commands.
func TestRecoveryLeavesANonDpkgHostAlone(t *testing.T) {
	operator, runner, _, _, state := preparedTransactionFixture(t)
	operator.packageStateDir = filepath.Join(t.TempDir(), "absent")
	state.Phase = phasePrepared
	state.PackagingRunning = true
	if err := writeTransaction(operator.transactionPath(), state); err != nil {
		t.Fatal(err)
	}

	if err := operator.RecoverTransaction(context.Background(), operator.transactionPath()); err != nil {
		t.Fatalf("recover: %v", err)
	}
	if ranCommand(runner, "dpkg", "--configure", "-a") {
		t.Fatalf("package-manager repair ran on a host without dpkg: %+v", runner.commands)
	}
}

func ranCommand(runner *fakeRunner, name string, args ...string) bool {
	for _, command := range runner.commands {
		if command.Name == name && strings.Join(command.Args, " ") == strings.Join(args, " ") {
			return true
		}
	}
	return false
}

func TestControlDatabaseSnapshotCapturesCommittedWALState(t *testing.T) {
	operator, _, _, _, state := preparedTransactionFixture(t)
	databasePath := filepath.Join(t.TempDir(), "control.db")
	database, err := sql.Open("sqlite", "file:"+databasePath+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err := database.Exec("CREATE TABLE state (value TEXT); INSERT INTO state(value) VALUES ('before-update')"); err != nil {
		t.Fatal(err)
	}
	operator.databasePath = databasePath
	snapshot, err := operator.createControlDatabaseSnapshot(context.Background(), state.ID)
	if err != nil {
		t.Fatalf("snapshot control database: %v", err)
	}

	copied, err := sql.Open("sqlite", "file:"+snapshot.Path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer copied.Close()
	var value string
	if err := copied.QueryRow("SELECT value FROM state").Scan(&value); err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	if value != "before-update" {
		t.Fatalf("snapshot value = %q", value)
	}
}

func TestBootRecoveryRestoresPreparedTransaction(t *testing.T) {
	operator, _, binaryPath, managedPath, state := preparedTransactionFixture(t)
	state.Phase = phasePrepared
	if err := writeTransaction(operator.transactionPath(), state); err != nil {
		t.Fatal(err)
	}

	if err := operator.RecoverTransaction(context.Background(), operator.transactionPath()); err != nil {
		t.Fatalf("recover: %v", err)
	}
	if data, _ := os.ReadFile(binaryPath); string(data) != "old-binary" {
		t.Fatalf("binary after recovery = %q", data)
	}
	if data, _ := os.ReadFile(managedPath); string(data) != "old-packaging" {
		t.Fatalf("packaging after recovery = %q", data)
	}
	recovered, err := readTransaction(operator.transactionPath())
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Phase != phaseFailed || recovered.FailureReported {
		t.Fatalf("prepared recovery journal = %+v", recovered)
	}
}

func TestManagedSnapshotCoversReleaseAndLifecycleArtifacts(t *testing.T) {
	paths := make(map[string]bool, len(defaultManagedPackagingPaths))
	for _, path := range defaultManagedPackagingPaths {
		paths[path] = true
	}
	for _, required := range []string{
		"/etc/nexa-panel/release-signers",
		"/etc/nginx/snippets/nexa-panel-proxy.conf",
		"/usr/lib/nexa-panel/uninstall.sh",
		"/usr/sbin/nexa-uninstall",
		"/usr/lib/systemd/system/nexa-update-recovery.service",
		"/etc/systemd/system/multi-user.target.wants/nexa-update-recovery.service",
	} {
		if !paths[required] {
			t.Errorf("managed rollback snapshot is missing %s", required)
		}
	}
}
