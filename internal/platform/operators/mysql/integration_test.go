package mysql

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type dockerRunner struct{ container string }

func (r dockerRunner) Run(ctx context.Context, command Command) ([]byte, error) {
	args := []string{"exec", "-i", r.container, command.Name}
	args = append(args, command.Args...)
	process := exec.CommandContext(ctx, "docker", args...)
	if command.StdinPath != "" {
		value, err := os.ReadFile(command.StdinPath)
		if err != nil {
			return nil, err
		}
		process.Stdin = bytes.NewReader(value)
	} else if command.Stdin != "" {
		process.Stdin = strings.NewReader(command.Stdin)
	}
	output, err := process.CombinedOutput()
	if err == nil && command.StdoutPath != "" {
		if writeErr := os.WriteFile(command.StdoutPath, output, 0o640); writeErr != nil {
			return nil, writeErr
		}
		return nil, nil
	}
	return output, err
}

func TestMySQLFamilyDestroyedDatabaseRestoreIntegration(t *testing.T) {
	if os.Getenv("NEXA_MYSQL_INTEGRATION") != "1" {
		t.Skip("set NEXA_MYSQL_INTEGRATION=1 to run Docker acceptance")
	}
	cases := []struct {
		name, image string
		kind        EngineKind
	}{{"mysql84", "mysql:8.4", EngineMySQL}, {"mariadb118", "mariadb:11.8", EngineMariaDB}}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			name := fmt.Sprintf("nexa-%s-%d", testCase.name, time.Now().UnixNano())
			run := exec.Command("docker", "run", "-d", "--name", name, "-e", "MYSQL_ALLOW_EMPTY_PASSWORD=yes", testCase.image, "--port=3306")
			output, err := run.CombinedOutput()
			if err != nil {
				t.Fatalf("start %s: %v: %s", testCase.image, err, output)
			}
			t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", name).Run() })
			deadline := time.Now().Add(90 * time.Second)
			for {
				client := "mysql"
				if testCase.kind == EngineMariaDB {
					client = "mariadb"
				}
				probe := exec.Command("docker", "exec", name, client, "--user=root", "--batch", "--skip-column-names", "--execute", "SELECT @@port;")
				probeOutput, probeErr := probe.CombinedOutput()
				if probeErr == nil && strings.TrimSpace(string(probeOutput)) == "3306" {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("database container readiness timeout")
				}
				time.Sleep(time.Second)
			}
			root := t.TempDir()
			runner := dockerRunner{container: name}
			operator, err := NewHostOperator(runner, HostConfig{SocketPath: "/var/run/mysqld/mysqld.sock", BackupRoot: root})
			if err != nil {
				t.Fatal(err)
			}
			engine, err := operator.Discover(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if engine == nil || engine.Kind != testCase.kind {
				t.Fatalf("engine=%+v", engine)
			}
			secret := "NexaIntegration-Secret-42"
			digest := sha256.Sum256([]byte(secret))
			accountPlan, err := operator.Plan(context.Background(), Change{Action: ActionCreateAccount, EngineID: engine.ID, Account: "app_user", SecretSHA256: hex.EncodeToString(digest[:])})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = operator.Apply(context.Background(), Execution{Plan: accountPlan, Secret: secret}); err != nil {
				t.Fatal(err)
			}
			databasePlan, err := operator.Plan(context.Background(), Change{Action: ActionCreateDatabase, EngineID: engine.ID, Database: "app_db", Account: "app_user"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = operator.Apply(context.Background(), Execution{Plan: databasePlan}); err != nil {
				t.Fatal(err)
			}
			insert := operator.stdinCommand(engine, "USE `app_db`; CREATE TABLE fixture(id INT PRIMARY KEY, value_text VARCHAR(64)); INSERT INTO fixture VALUES (1, 'restored-value');\n")
			if output, err := runner.Run(context.Background(), insert); err != nil {
				t.Fatalf("insert fixture: %v: %s", err, output)
			}
			backupID := "backup_1"
			backupPath := filepath.Join(root, engine.ID, "app_db", backupID+".sql")
			backupPlan, err := operator.Plan(context.Background(), Change{Action: ActionCreateBackup, EngineID: engine.ID, Database: "app_db", BackupID: backupID, BackupPath: backupPath})
			if err != nil {
				t.Fatal(err)
			}
			backup, err := operator.Apply(context.Background(), Execution{Plan: backupPlan})
			if err != nil {
				t.Fatal(err)
			}
			if backup.Backup == nil || !backup.Backup.Verified {
				t.Fatalf("backup=%+v", backup)
			}
			if output, err := runner.Run(context.Background(), operator.stdinCommand(engine, "DROP DATABASE `app_db`;\n")); err != nil {
				t.Fatalf("destroy database: %v: %s", err, output)
			}
			restorePlan, err := operator.Plan(context.Background(), Change{Action: ActionRestoreBackup, EngineID: engine.ID, Database: "app_db", BackupID: backupID, BackupPath: backupPath, BackupSHA256: backup.Backup.SHA256, RestoreToken: "a1b2c3d4"})
			if err != nil {
				t.Fatal(err)
			}
			restored, err := operator.Apply(context.Background(), Execution{Plan: restorePlan})
			if err != nil {
				t.Fatal(err)
			}
			if !restored.Restored || !restored.Verified {
				t.Fatalf("restored=%+v", restored)
			}
			query := operator.clientCommand(engine, "SELECT value_text FROM app_db.fixture WHERE id=1;")
			value, err := runner.Run(context.Background(), query)
			if err != nil || strings.TrimSpace(string(value)) != "restored-value" {
				t.Fatalf("restored value=%q err=%v", value, err)
			}
		})
	}
}
