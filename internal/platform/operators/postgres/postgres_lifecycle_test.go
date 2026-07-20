package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type recordingRunner struct {
	discovery       string
	commands        []Command
	missingOriginal bool
	databaseErrors  map[string]error
	existing        map[string]bool
}

func (r *recordingRunner) Run(_ context.Context, command Command) ([]byte, error) {
	r.commands = append(r.commands, command)
	if command.Name == "pg_lsclusters" {
		return []byte(r.discovery), nil
	}
	joined := strings.Join(command.Args, " ")
	if strings.Contains(joined, "SELECT 1 FROM pg_database WHERE datname = ") {
		for database, err := range r.databaseErrors {
			if strings.Contains(joined, "datname = '"+database+"'") {
				return nil, err
			}
		}
		for database, exists := range r.existing {
			if strings.Contains(joined, "datname = '"+database+"'") {
				if exists {
					return []byte("1\n"), nil
				}
				return nil, nil
			}
		}
		if r.missingOriginal && strings.Contains(joined, "datname = 'app_db'") {
			return nil, nil
		}
		if strings.Contains(joined, "_nexa_restore_") || strings.Contains(joined, "_nexa_previous_") {
			return nil, nil
		}
		return []byte("1\n"), nil
	}
	if strings.Contains(joined, "/psql") {
		return []byte("1\n"), nil
	}
	return nil, nil
}

func TestDatabaseExistenceProbeDoesNotTreatCommandFailureAsMissing(t *testing.T) {
	runner := &recordingRunner{databaseErrors: map[string]error{"app_db": errors.New("connection refused")}}
	operator := newTestOperator(t, runner)
	_, err := operator.databaseExists(context.Background(), Change{Version: "18", Port: 5432}, "app_db")
	if err == nil || !strings.Contains(err.Error(), "inspect PostgreSQL database app_db") {
		t.Fatalf("error = %v", err)
	}
}

func TestRestoreRefusesToDeleteRetainedRecoveryDatabase(t *testing.T) {
	root := t.TempDir()
	archive := filepath.Join(root, "postgresql_18_nexa_main", "app_db", "restore_1.dump")
	if err := os.MkdirAll(filepath.Dir(archive), 0o750); err != nil {
		t.Fatal(err)
	}
	content := []byte("fixture custom-format archive")
	if err := os.WriteFile(archive, content, 0o640); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(content)
	runner := &recordingRunner{
		discovery: `[{
			"version":"18","cluster":"nexa_main","port":5432,"status":"online","owner":"postgres"
		}]`,
		existing: map[string]bool{"app_db_nexa_previous_a1b2c3d4": true},
	}
	operator, err := NewHostOperator(runner, HostConfig{BackupRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	operator.now = func() time.Time { return time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC) }
	change := Change{Action: ActionRestoreBackup, InstanceID: "postgresql_18_nexa_main", Database: "app_db", OwnerRole: "app_role", BackupID: "restore_1", BackupPath: archive, BackupSHA256: hex.EncodeToString(digest[:]), RestoreToken: "a1b2c3d4e5f6"}
	plan, err := operator.Plan(context.Background(), change)
	if err != nil {
		t.Fatal(err)
	}
	_, err = operator.Apply(context.Background(), Execution{Plan: plan})
	if err == nil || !strings.Contains(err.Error(), "retained PostgreSQL recovery database") {
		t.Fatalf("error = %v", err)
	}
	for _, command := range runner.commands {
		if command.Name == "runuser" && strings.Contains(strings.Join(command.Args, " "), "dropdb") && strings.Contains(strings.Join(command.Args, " "), "nexa_previous") {
			t.Fatalf("retained recovery database was deleted: %+v", command)
		}
	}
}

func newTestOperator(t *testing.T, runner Runner) *HostOperator {
	t.Helper()
	operator, err := NewHostOperator(runner, HostConfig{DataRoot: "/var/lib/postgresql", ConfigRoot: "/etc/postgresql", LogRoot: "/var/log/postgresql", SocketRoot: "/run/postgresql", BackupRoot: "/var/lib/postgresql/nexa-backups"})
	if err != nil {
		t.Fatal(err)
	}
	operator.now = func() time.Time { return time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC) }
	return operator
}

func TestDiscoverFiltersSupportedInstancesAndDerivesIdentity(t *testing.T) {
	runner := &recordingRunner{discovery: `[
		{"version":18,"cluster":"nexa_main","port":"5432","running":1,"owneruid":0,"pgdata":"/var/lib/postgresql/18/nexa_main","socketdir":"/var/run/postgresql","logfile":"/var/log/postgresql/postgresql-18-nexa_main.log"},
		{"version":"15","cluster":"legacy","port":5433,"status":"online","owner":"postgres"}
	]`}
	operator := newTestOperator(t, runner)
	instances, err := operator.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(instances) != 1 || instances[0].ID != "postgresql_18_nexa_main" || instances[0].SystemdUnit != "postgresql@18-nexa_main.service" || instances[0].SocketPath != "/var/run/postgresql" || !instances[0].ManagedByNexa {
		t.Fatalf("instances = %+v", instances)
	}
}

func TestProvisionPlanSelectsAnUnusedPortAndRejectsDrift(t *testing.T) {
	runner := &recordingRunner{discovery: `[{"version":"18","cluster":"main","port":5432,"status":"online","owner":"postgres"}]`}
	operator := newTestOperator(t, runner)
	plan, err := operator.Plan(context.Background(), Change{Action: ActionProvision, Version: "17", Cluster: "nexa_app"})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Change.Port != 5433 || plan.Change.InstanceID != "postgresql_17_nexa_app" || plan.Interruption {
		t.Fatalf("plan = %+v", plan)
	}
	runner.discovery = `[{"version":"18","cluster":"main","port":5432,"status":"online","owner":"postgres"},{"version":"16","cluster":"other","port":5434,"status":"online","owner":"postgres"}]`
	if _, err := operator.Apply(context.Background(), Execution{Plan: plan}); err == nil || !strings.Contains(err.Error(), "observed state changed") {
		t.Fatalf("expected drift rejection, got %v", err)
	}
}

func TestRoleCredentialIsDigestedInPlanAndOnlySentOnStdin(t *testing.T) {
	const secret = "correct horse battery staple"
	digest := sha256.Sum256([]byte(secret))
	runner := &recordingRunner{discovery: `[{"version":"18","cluster":"nexa_main","port":5432,"status":"online","owner":"postgres"}]`}
	operator := newTestOperator(t, runner)
	plan, err := operator.Plan(context.Background(), Change{Action: ActionCreateRole, InstanceID: "postgresql_18_nexa_main", Role: "app_role", SecretSHA256: hex.EncodeToString(digest[:])})
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(plan)
	if strings.Contains(string(encoded), secret) {
		t.Fatal("plaintext credential leaked into plan")
	}
	if _, err := operator.Apply(context.Background(), Execution{Plan: plan, Secret: "wrong"}); err == nil {
		t.Fatal("expected credential digest rejection")
	}
	result, err := operator.Apply(context.Background(), Execution{Plan: plan, Secret: secret})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Verified || result.Role != "app_role" {
		t.Fatalf("result = %+v", result)
	}
	var create Command
	for _, command := range runner.commands {
		if strings.Contains(command.Stdin, "CREATE ROLE") {
			create = command
			break
		}
	}
	if create.Name == "" || !strings.Contains(create.Stdin, secret) || strings.Contains(strings.Join(create.Args, " "), secret) {
		t.Fatalf("credential command = %+v", create)
	}
	arguments := strings.Join(create.Args, " ")
	if !strings.Contains(arguments, "--single-transaction") || !strings.Contains(arguments, "--file -") {
		t.Fatalf("credential command is not a transactional stdin script: %+v", create)
	}
}

func TestEnsureSocketScramAuthInsertsRuleAheadOfPeerAndIsIdempotent(t *testing.T) {
	configRoot := t.TempDir()
	clusterDir := filepath.Join(configRoot, "18", "main")
	if err := os.MkdirAll(clusterDir, 0o755); err != nil {
		t.Fatal(err)
	}
	hbaPath := filepath.Join(clusterDir, "pg_hba.conf")
	stock := "# comment\n" +
		"local   all             postgres                                peer\n" +
		"local   all             all                                     peer\n" +
		"host    all             all             127.0.0.1/32            scram-sha-256\n"
	if err := os.WriteFile(hbaPath, []byte(stock), 0o640); err != nil {
		t.Fatal(err)
	}
	runner := &recordingRunner{}
	operator, err := NewHostOperator(runner, HostConfig{DataRoot: "/var/lib/postgresql", ConfigRoot: configRoot, LogRoot: "/var/log/postgresql", SocketRoot: "/run/postgresql", BackupRoot: filepath.Join(t.TempDir(), "b")})
	if err != nil {
		t.Fatal(err)
	}

	if err := operator.ensureSocketScramAuth(context.Background(), "18", "main"); err != nil {
		t.Fatal(err)
	}
	updated, err := os.ReadFile(hbaPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(updated), "\n"), "\n")
	scramIndex, peerCatchIndex, postgresPeerIndex := -1, -1, -1
	for i, line := range lines {
		fields := strings.Fields(line)
		if len(fields) < 4 || fields[0] != "local" {
			continue
		}
		switch {
		case fields[1] == "all" && fields[2] == "postgres" && fields[3] == "peer":
			postgresPeerIndex = i
		case fields[1] == "all" && fields[2] == "all" && fields[3] == "scram-sha-256":
			scramIndex = i
		case fields[1] == "all" && fields[2] == "all" && fields[3] == "peer":
			peerCatchIndex = i
		}
	}
	if scramIndex == -1 {
		t.Fatalf("scram socket rule was not inserted:\n%s", updated)
	}
	if !(postgresPeerIndex < scramIndex && scramIndex < peerCatchIndex) {
		t.Fatalf("scram rule must sit after the postgres-peer line and before the peer catch-all (postgres=%d scram=%d peer=%d):\n%s", postgresPeerIndex, scramIndex, peerCatchIndex, updated)
	}
	reloads := 0
	for _, command := range runner.commands {
		if command.Name == "pg_ctlcluster" && strings.Join(command.Args, " ") == "18 main reload" {
			reloads++
		}
	}
	if reloads != 1 {
		t.Fatalf("expected exactly one reload after inserting the rule, got %d", reloads)
	}

	// Second call is a no-op: no duplicate rule, no extra reload.
	if err := operator.ensureSocketScramAuth(context.Background(), "18", "main"); err != nil {
		t.Fatal(err)
	}
	again, err := os.ReadFile(hbaPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(again), "all                                     scram-sha-256") != 1 {
		t.Fatalf("idempotent call duplicated the socket rule:\n%s", again)
	}
	reloads = 0
	for _, command := range runner.commands {
		if command.Name == "pg_ctlcluster" && strings.Join(command.Args, " ") == "18 main reload" {
			reloads++
		}
	}
	if reloads != 1 {
		t.Fatalf("idempotent call must not reload again, total reloads=%d", reloads)
	}
}

func TestEnsureSocketScramAuthToleratesMissingHba(t *testing.T) {
	runner := &recordingRunner{}
	operator, err := NewHostOperator(runner, HostConfig{DataRoot: "/var/lib/postgresql", ConfigRoot: t.TempDir(), LogRoot: "/var/log/postgresql", SocketRoot: "/run/postgresql", BackupRoot: filepath.Join(t.TempDir(), "b")})
	if err != nil {
		t.Fatal(err)
	}
	if err := operator.ensureSocketScramAuth(context.Background(), "18", "absent"); err != nil {
		t.Fatalf("missing pg_hba should be a no-op, got %v", err)
	}
	if len(runner.commands) != 0 {
		t.Fatalf("no cluster reload should occur when pg_hba is absent: %+v", runner.commands)
	}
}

func TestGrantPlanRevokesOldPrivilegesBeforeApplyingNewAccess(t *testing.T) {
	runner := &recordingRunner{discovery: `[{"version":"18","cluster":"nexa_main","port":5432,"status":"online","owner":"postgres"}]`}
	operator := newTestOperator(t, runner)
	plan, err := operator.Plan(context.Background(), Change{Action: ActionApplyGrant, InstanceID: "postgresql_18_nexa_main", Database: "app_db", OwnerRole: "owner_role", Role: "reader_role", Access: AccessReadOnly})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := operator.Apply(context.Background(), Execution{Plan: plan}); err != nil {
		t.Fatal(err)
	}
	var script string
	for _, command := range runner.commands {
		if strings.Contains(command.Stdin, "REVOKE ALL ON DATABASE") {
			script = command.Stdin
		}
	}
	revoke := strings.Index(script, "REVOKE ALL PRIVILEGES ON ALL TABLES")
	grant := strings.Index(script, "GRANT SELECT ON ALL TABLES")
	if revoke < 0 || grant < 0 || revoke > grant || strings.Contains(script, "GRANT INSERT") {
		t.Fatalf("read-only grant script = %s", script)
	}
}

func TestRestoreVerifiedArchiveThroughTemporaryDatabaseSwap(t *testing.T) {
	root := t.TempDir()
	archive := filepath.Join(root, "postgresql_18_nexa_main", "app_db", "restore_1.dump")
	if err := os.MkdirAll(filepath.Dir(archive), 0o750); err != nil {
		t.Fatal(err)
	}
	content := []byte("fixture custom-format archive")
	if err := os.WriteFile(archive, content, 0o640); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(content)
	runner := &recordingRunner{discovery: `[{"version":"18","cluster":"nexa_main","port":5432,"status":"online","owner":"postgres"}]`, missingOriginal: true}
	operator, err := NewHostOperator(runner, HostConfig{DataRoot: "/var/lib/postgresql", ConfigRoot: "/etc/postgresql", LogRoot: "/var/log/postgresql", SocketRoot: "/run/postgresql", BackupRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	operator.now = func() time.Time { return time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC) }
	change := Change{Action: ActionRestoreBackup, InstanceID: "postgresql_18_nexa_main", Database: "app_db", OwnerRole: "app_role", BackupID: "restore_1", BackupPath: archive, BackupSHA256: hex.EncodeToString(digest[:]), RestoreToken: "a1b2c3d4e5f6"}
	plan, err := operator.Plan(context.Background(), change)
	if err != nil {
		t.Fatal(err)
	}
	observation, err := operator.Apply(context.Background(), Execution{Plan: plan})
	if err != nil {
		t.Fatal(err)
	}
	if !observation.Restored || !observation.Verified {
		t.Fatalf("observation = %+v", observation)
	}
	var restored, swapped bool
	for _, command := range runner.commands {
		joined := strings.Join(command.Args, " ")
		if strings.Contains(joined, "/pg_restore") && strings.Contains(joined, "app_db_nexa_restore_a1b2c3d4") {
			restored = true
		}
		if !strings.Contains(command.Stdin, "nexa_previous") && strings.Contains(command.Stdin, `ALTER DATABASE "app_db_nexa_restore_a1b2c3d4" RENAME TO "app_db"`) {
			swapped = true
		}
	}
	if !restored || !swapped {
		t.Fatalf("restore commands missing: %+v", runner.commands)
	}
}

func TestDiscoveryErrorsAreSafeAndBounded(t *testing.T) {
	operator := newTestOperator(t, failingRunner{})
	_, err := operator.Discover(context.Background())
	if err == nil || strings.Contains(err.Error(), strings.Repeat("x", 501)) {
		t.Fatalf("error = %v", err)
	}
}

type failingRunner struct{}

func (failingRunner) Run(context.Context, Command) ([]byte, error) {
	return []byte(strings.Repeat("x", 700)), errors.New("exit status 1")
}
