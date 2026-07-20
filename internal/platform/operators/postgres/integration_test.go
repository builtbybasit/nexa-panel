package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type containerRunner struct{ execRunner }

func (r containerRunner) Run(ctx context.Context, command Command) ([]byte, error) {
	if command.Name == "pg_lsclusters" {
		return []byte(`[{"version":"18","cluster":"nexa_test","port":5432,"running":1,"owneruid":999,"pgdata":"/var/lib/postgresql/18/docker","logfile":"/var/log/postgresql/postgresql-18-nexa_test.log"}]`), nil
	}
	return r.execRunner.Run(ctx, command)
}

func TestPostgres18DestroyedDatabaseRestoreIntegration(t *testing.T) {
	if os.Getenv("NEXA_POSTGRES_INTEGRATION") != "1" {
		t.Skip("set NEXA_POSTGRES_INTEGRATION=1 inside the PostgreSQL 18 test container")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	runner := containerRunner{}
	operator, err := NewHostOperator(runner, HostConfig{DataRoot: "/var/lib/postgresql", ConfigRoot: "/etc/postgresql", LogRoot: "/var/log/postgresql", SocketRoot: "/var/run/postgresql", BackupRoot: "/var/lib/postgresql/nexa-backups"})
	if err != nil {
		t.Fatal(err)
	}

	const role, database, secret = "nexa_restore_owner", "nexa_restore_fixture", "integration-only-secret-18"
	cleanup := func() {
		_, _ = runner.Run(context.Background(), asPostgres("18", "dropdb", "--if-exists", "--force", "--host", "/var/run/postgresql", "--port", "5432", "--username", "postgres", database))
		command := asPostgres("18", "psql", "--no-psqlrc", "--host", "/var/run/postgresql", "--port", "5432", "--username", "postgres", "--dbname", "postgres", "--set", "ON_ERROR_STOP=1")
		command.Stdin = "DROP ROLE IF EXISTS " + quoteIdentifier(role) + ";\n"
		_, _ = runner.Run(context.Background(), command)
		_ = os.RemoveAll("/var/lib/postgresql/nexa-backups/postgresql_18_nexa_test")
	}
	cleanup()
	t.Cleanup(cleanup)

	secretDigest := sha256.Sum256([]byte(secret))
	rolePlan, err := operator.Plan(ctx, Change{Action: ActionCreateRole, InstanceID: "postgresql_18_nexa_test", Role: role, SecretSHA256: hex.EncodeToString(secretDigest[:])})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := operator.Apply(ctx, Execution{Plan: rolePlan, Secret: secret}); err != nil {
		t.Fatal(err)
	}
	databasePlan, err := operator.Plan(ctx, Change{Action: ActionCreateDatabase, InstanceID: "postgresql_18_nexa_test", Database: database, OwnerRole: role})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := operator.Apply(ctx, Execution{Plan: databasePlan}); err != nil {
		t.Fatal(err)
	}
	fixture := asPostgres("18", "psql", "--no-psqlrc", "--host", "/var/run/postgresql", "--port", "5432", "--username", "postgres", "--dbname", database, "--set", "ON_ERROR_STOP=1")
	fixture.Stdin = "CREATE TABLE recovery_fixture (payload text NOT NULL);\nINSERT INTO recovery_fixture(payload) VALUES ('restored-through-nexa');\n"
	if output, err := runner.Run(ctx, fixture); err != nil {
		t.Fatalf("create fixture: %v: %s", err, output)
	}

	backupID := "restore_integration_18"
	backupPath := filepath.Join("/var/lib/postgresql/nexa-backups", "postgresql_18_nexa_test", database, backupID+".dump")
	backupPlan, err := operator.Plan(ctx, Change{Action: ActionCreateBackup, InstanceID: "postgresql_18_nexa_test", Database: database, OwnerRole: role, BackupID: backupID, BackupPath: backupPath})
	if err != nil {
		t.Fatal(err)
	}
	backupObservation, err := operator.Apply(ctx, Execution{Plan: backupPlan})
	if err != nil {
		t.Fatal(err)
	}
	if backupObservation.Backup == nil || !backupObservation.Backup.Verified {
		t.Fatalf("backup observation = %+v", backupObservation)
	}
	if output, err := runner.Run(ctx, asPostgres("18", "dropdb", "--force", "--host", "/var/run/postgresql", "--port", "5432", "--username", "postgres", database)); err != nil {
		t.Fatalf("destroy fixture database: %v: %s", err, output)
	}

	restorePlan, err := operator.Plan(ctx, Change{Action: ActionRestoreBackup, InstanceID: "postgresql_18_nexa_test", Database: database, OwnerRole: role, BackupID: backupID, BackupPath: backupPath, BackupSHA256: backupObservation.Backup.SHA256, RestoreToken: "18abcdef1234"})
	if err != nil {
		t.Fatal(err)
	}
	restored, err := operator.Apply(ctx, Execution{Plan: restorePlan})
	if err != nil {
		t.Fatal(err)
	}
	if !restored.Restored || !restored.Verified {
		t.Fatalf("restore observation = %+v", restored)
	}
	verify := asPostgres("18", "psql", "--no-psqlrc", "--tuples-only", "--no-align", "--host", "/var/run/postgresql", "--port", "5432", "--username", "postgres", "--dbname", database, "--command", "SELECT payload FROM recovery_fixture;")
	output, err := runner.Run(ctx, verify)
	if err != nil || strings.TrimSpace(string(output)) != "restored-through-nexa" {
		t.Fatalf("restored payload = %q, err=%v", output, err)
	}
	t.Logf("restored PostgreSQL 18 database from verified archive %s", backupObservation.Backup.SHA256)
}
