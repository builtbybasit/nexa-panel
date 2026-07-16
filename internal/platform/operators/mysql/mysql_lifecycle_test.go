package mysql

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type recordingRunner struct {
	engine     string
	generalLog string
	commands   []Command
	failImport bool
	privileges string
}

func (r *recordingRunner) Run(_ context.Context, command Command) ([]byte, error) {
	r.commands = append(r.commands, command)
	joined := strings.Join(command.Args, " ")
	if strings.Contains(joined, "SELECT @@version") {
		return []byte(r.engine), nil
	}
	if strings.Contains(joined, "@@GLOBAL.general_log") {
		return []byte(r.generalLog + "\n"), nil
	}
	if strings.Contains(joined, "INFORMATION_SCHEMA.SCHEMATA") {
		return []byte("app_db\n"), nil
	}
	if strings.Contains(joined, "FROM mysql.user") {
		return []byte("app_user\n"), nil
	}
	if strings.Contains(joined, "SCHEMA_PRIVILEGES") {
		return []byte(r.privileges), nil
	}
	if command.StdoutPath != "" {
		return nil, os.WriteFile(command.StdoutPath, []byte("-- logical backup\nCREATE TABLE example(id INT);\n"), 0o640)
	}
	if r.failImport && command.StdinPath != "" && !strings.Contains(command.StdinPath, ".rollback-") {
		return []byte("import failed"), errors.New("exit status 1")
	}
	return nil, nil
}

func newTestOperator(t *testing.T, runner Runner, backupRoot string) *HostOperator {
	t.Helper()
	operator, err := NewHostOperator(runner, HostConfig{SocketPath: "/run/mysqld/mysqld.sock", BackupRoot: backupRoot})
	if err != nil {
		t.Fatal(err)
	}
	operator.now = func() time.Time { return time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC) }
	return operator
}

func TestDiscoverDistinguishesMariaDB(t *testing.T) {
	runner := &recordingRunner{engine: "11.8.2-MariaDB-ubu2404\tUbuntu 24.04\t/run/mysqld/mysqld.sock\t3306"}
	engine, err := newTestOperator(t, runner, t.TempDir()).Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if engine.Kind != EngineMariaDB || engine.SystemdUnit != "mariadb.service" || engine.Version != "11.8.2-MariaDB-ubu2404" {
		t.Fatalf("engine = %+v", engine)
	}
}

func TestCredentialPlanRejectsGeneralLogAndUsesOnlyStdin(t *testing.T) {
	secret := "correct horse battery staple"
	digest := sha256.Sum256([]byte(secret))
	runner := &recordingRunner{engine: "8.4.5\tMySQL Community Server - GPL\t/run/mysqld/mysqld.sock\t3306", generalLog: "1"}
	operator := newTestOperator(t, runner, t.TempDir())
	change := Change{Action: ActionCreateAccount, EngineID: "mysql", Account: "app_user", SecretSHA256: hex.EncodeToString(digest[:])}
	if _, err := operator.Plan(context.Background(), change); err == nil || !strings.Contains(err.Error(), "general query log") {
		t.Fatalf("expected general-log rejection, got %v", err)
	}
	runner.generalLog = "0"
	plan, err := operator.Plan(context.Background(), change)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := operator.Apply(context.Background(), Execution{Plan: plan, Secret: secret}); err != nil {
		t.Fatal(err)
	}
	var mutation Command
	for _, command := range runner.commands {
		if strings.Contains(command.Stdin, "CREATE USER") {
			mutation = command
		}
	}
	if mutation.Name == "" || !strings.Contains(mutation.Stdin, secret) || strings.Contains(strings.Join(mutation.Args, " "), secret) {
		t.Fatalf("mutation = %+v", mutation)
	}
	if !contains(mutation.Env, "MYSQL_HISTFILE=/dev/null") {
		t.Fatalf("history protection missing: %+v", mutation.Env)
	}
}

func TestGrantIsDatabaseScoped(t *testing.T) {
	runner := &recordingRunner{engine: "8.4.5\tMySQL Community Server - GPL\t/run/mysqld/mysqld.sock\t3306", generalLog: "0", privileges: "INSERT\nSELECT\n"}
	operator := newTestOperator(t, runner, t.TempDir())
	plan, err := operator.Plan(context.Background(), Change{Action: ActionApplyGrant, EngineID: "mysql", Database: "app_db", Account: "app_user", Access: AccessReadOnly})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := operator.Apply(context.Background(), Execution{Plan: plan}); err != nil {
		t.Fatal(err)
	}
	var sql string
	for _, command := range runner.commands {
		if strings.Contains(command.Stdin, "GRANT SELECT") {
			sql = command.Stdin
		}
	}
	if !strings.Contains(sql, "REVOKE INSERT, SELECT ON `app_db`.*") || !strings.Contains(sql, "GRANT SELECT, SHOW VIEW ON `app_db`.*") || strings.Contains(sql, " ON *.*") {
		t.Fatalf("grant SQL = %s", sql)
	}
}

func TestBackupAndRestoreUseManagedPathsAndRollback(t *testing.T) {
	root := t.TempDir()
	runner := &recordingRunner{engine: "11.8.2-MariaDB\tMariaDB Server\t/run/mysqld/mysqld.sock\t3306", generalLog: "0"}
	operator := newTestOperator(t, runner, root)
	backupPath := filepath.Join(root, "mariadb", "app_db", "backup_1.sql")
	plan, err := operator.Plan(context.Background(), Change{Action: ActionCreateBackup, EngineID: "mariadb", Database: "app_db", BackupID: "backup_1", BackupPath: backupPath})
	if err != nil {
		t.Fatal(err)
	}
	result, err := operator.Apply(context.Background(), Execution{Plan: plan})
	if err != nil {
		t.Fatal(err)
	}
	if result.Backup == nil || !result.Backup.Verified {
		t.Fatalf("backup = %+v", result.Backup)
	}
	restore, err := operator.Plan(context.Background(), Change{Action: ActionRestoreBackup, EngineID: "mariadb", Database: "app_db", BackupID: "backup_1", BackupPath: backupPath, BackupSHA256: result.Backup.SHA256, RestoreToken: "a1b2c3d4"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := operator.Apply(context.Background(), Execution{Plan: restore}); err != nil {
		t.Fatal(err)
	}
	var sawDump, sawImport bool
	for _, command := range runner.commands {
		if command.Name == "mariadb-dump" {
			sawDump = true
		}
		if command.StdinPath == backupPath {
			sawImport = true
		}
	}
	if !sawDump || !sawImport {
		t.Fatalf("commands = %+v", runner.commands)
	}
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
