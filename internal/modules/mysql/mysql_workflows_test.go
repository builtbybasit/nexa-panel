package mysql

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/platform/audit"
	"github.com/nexa-panel/nexa-panel/internal/platform/jobs"
	mysqloperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/mysql"
	"github.com/nexa-panel/nexa-panel/internal/platform/persistence"
	"github.com/nexa-panel/nexa-panel/internal/platform/secrets"
)

type fakeMySQLOperator struct {
	engine    mysqloperator.Engine
	failNext  bool
	secrets   []string
	sizes     map[string]int64
	sizeError error
	sizeCalls int
}

func (f *fakeMySQLOperator) Discover(context.Context) (*mysqloperator.Engine, error) {
	engine := f.engine
	return &engine, nil
}

func (f *fakeMySQLOperator) Sizes(context.Context) (map[string]int64, error) {
	f.sizeCalls++
	if f.sizeError != nil {
		return nil, f.sizeError
	}
	return f.sizes, nil
}

func (f *fakeMySQLOperator) Plan(_ context.Context, change mysqloperator.Change) (mysqloperator.Plan, error) {
	now := time.Now().UTC()
	return mysqloperator.Plan{ID: randomToken(8), Kind: mysqloperator.PlanKind, Change: change, Steps: []string{"typed change"}, ObservedFingerprint: "observed", PlannedAt: now, ExpiresAt: now.Add(time.Hour), Signature: "agent-signed"}, nil
}

func (f *fakeMySQLOperator) Apply(_ context.Context, execution mysqloperator.Execution) (mysqloperator.Observation, error) {
	if execution.Secret != "" {
		f.secrets = append(f.secrets, execution.Secret)
	}
	if f.failNext {
		f.failNext = false
		return mysqloperator.Observation{}, errors.New("simulated MySQL-family failure")
	}
	change := execution.Plan.Change
	observation := mysqloperator.Observation{Action: change.Action, Database: change.Database, Account: change.Account, Access: string(change.Access), Verified: true}
	if change.Action == mysqloperator.ActionCreateBackup {
		observation.Backup = &mysqloperator.Backup{ID: change.BackupID, Path: change.BackupPath, SHA256: strings.Repeat("a", 64), SizeBytes: 4096, CreatedAt: time.Now().UTC(), Verified: true}
	}
	if change.Action == mysqloperator.ActionRestoreBackup {
		observation.Restored = true
	}
	return observation, nil
}

func TestMySQLAccountDatabaseBackupRestoreAndFailedRotation(t *testing.T) {
	ctx := context.Background()
	database, err := persistence.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	auditLog, err := audit.New(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	queue, err := jobs.NewWithConfig(ctx, database, auditLog, slog.New(slog.NewTextHandler(io.Discard, nil)), jobs.Config{PollInterval: 5 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := secrets.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	operator := &fakeMySQLOperator{engine: mysqloperator.Engine{ID: "mysql", Kind: mysqloperator.EngineMySQL, Version: "8.4.5", VersionText: "8.4.5 MySQL Community Server", Port: 3306, Status: "online", SocketPath: "/run/mysqld/mysqld.sock", SystemdUnit: "mysql.service"}}
	module, err := New(ctx, database, queue, cipher, operator)
	if err != nil {
		t.Fatal(err)
	}
	queue.Start(ctx)
	t.Cleanup(queue.Close)
	if _, err := module.SyncEngines(ctx); err != nil {
		t.Fatal(err)
	}

	account, planJob, err := module.CreateAccount(ctx, CreateAccountRequest{EngineID: operator.engine.ID, Name: "app_account"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitJob(t, queue, planJob.ID, jobs.StateSucceeded)
	stored, err := module.StoredPlan(ctx, resourceAccount, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	encodedPlan := string(mustJSON(t, stored))
	if strings.Contains(encodedPlan, "credential_ciphertext") || strings.Contains(encodedPlan, "pending_credential") {
		t.Fatal("encrypted or plaintext credential leaked into the reviewed plan")
	}
	applyJob, err := module.ApplyPlan(ctx, resourceAccount, account.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitJob(t, queue, applyJob.ID, jobs.StateSucceeded)
	accounts, _ := module.ListAccounts(ctx, operator.engine.ID)
	if len(accounts) != 1 || accounts[0].Status != StatusActive || !accounts[0].CredentialAvailable || accounts[0].CredentialVersion != 1 || len(operator.secrets) != 1 {
		t.Fatalf("accounts=%+v secrets=%d", accounts, len(operator.secrets))
	}
	credential, err := module.RevealCredential(ctx, account.ID)
	if err != nil || credential == "" || credential != operator.secrets[0] {
		t.Fatalf("credential reveal failed: %v", err)
	}
	if _, err := module.RevealCredential(ctx, account.ID); err == nil {
		t.Fatal("credential should be revealable exactly once")
	}

	_, rotatePlanJob, err := module.RotateAccount(ctx, account.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitJob(t, queue, rotatePlanJob.ID, jobs.StateSucceeded)
	operator.failNext = true
	rotateApplyJob, err := module.ApplyPlan(ctx, resourceAccount, account.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitJob(t, queue, rotateApplyJob.ID, jobs.StateFailed)
	accountModel, err := module.getAccountModel(ctx, account.ID)
	if err != nil || Status(accountModel.Status) != StatusActive || accountModel.CredentialCiphertext == nil || accountModel.PendingCredentialCiphertext != nil || accountModel.Failure == nil {
		t.Fatalf("failed rotation did not preserve active credential: %+v, %v", accountModel, err)
	}

	managed, databasePlanJob, err := module.CreateDatabase(ctx, CreateDatabaseRequest{EngineID: operator.engine.ID, Name: "app_db", OwnerAccountID: account.ID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitJob(t, queue, databasePlanJob.ID, jobs.StateSucceeded)
	databaseApplyJob, err := module.ApplyPlan(ctx, resourceDatabase, managed.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitJob(t, queue, databaseApplyJob.ID, jobs.StateSucceeded)

	point, backupPlanJob, err := module.CreateBackup(ctx, managed.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitJob(t, queue, backupPlanJob.ID, jobs.StateSucceeded)
	backupApplyJob, err := module.ApplyPlan(ctx, resourceRestorePoint, point.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitJob(t, queue, backupApplyJob.ID, jobs.StateSucceeded)
	points, _ := module.ListRestorePoints(ctx, managed.ID)
	if len(points) != 1 || points[0].Status != StatusVerified || points[0].SHA256 == "" {
		t.Fatalf("restore points = %+v", points)
	}

	_, restorePlanJob, err := module.PrepareRestore(ctx, point.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitJob(t, queue, restorePlanJob.ID, jobs.StateSucceeded)
	restoreApplyJob, err := module.ApplyPlan(ctx, resourceRestorePoint, point.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitJob(t, queue, restoreApplyJob.ID, jobs.StateSucceeded)
}

func waitJob(t *testing.T, queue *jobs.Module, id int64, expected jobs.State) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		job, err := queue.Get(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if job.State.Terminal() {
			if job.State != expected {
				t.Fatalf("job %d state=%s failure=%s", id, job.State, job.Failure)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("job timeout")
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
