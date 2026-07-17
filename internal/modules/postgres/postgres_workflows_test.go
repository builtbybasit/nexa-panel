package postgres

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
	postgresoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/postgres"
	"github.com/nexa-panel/nexa-panel/internal/platform/persistence"
	"github.com/nexa-panel/nexa-panel/internal/platform/secrets"
)

type fakePostgresOperator struct {
	instance  postgresoperator.Instance
	failNext  bool
	secrets   []string
	sizes     map[string]int64
	sizeError error
	sizeCalls int
}

func (f *fakePostgresOperator) Discover(context.Context) ([]postgresoperator.Instance, error) {
	return []postgresoperator.Instance{f.instance}, nil
}
func (f *fakePostgresOperator) Sizes(_ context.Context, instanceID string) (map[string]int64, error) {
	f.sizeCalls++
	if f.sizeError != nil {
		return nil, f.sizeError
	}
	if instanceID != f.instance.ID {
		return nil, errors.New("unknown PostgreSQL instance")
	}
	return f.sizes, nil
}
func (f *fakePostgresOperator) Plan(_ context.Context, change postgresoperator.Change) (postgresoperator.Plan, error) {
	now := time.Now().UTC()
	return postgresoperator.Plan{ID: randomToken(8), Kind: postgresoperator.PlanKind, Change: change, Steps: []string{"typed change"}, ObservedFingerprint: "observed", PlannedAt: now, ExpiresAt: now.Add(time.Hour), Signature: "agent-signed"}, nil
}
func (f *fakePostgresOperator) Apply(_ context.Context, execution postgresoperator.Execution) (postgresoperator.Observation, error) {
	if execution.Secret != "" {
		f.secrets = append(f.secrets, execution.Secret)
	}
	if f.failNext {
		f.failNext = false
		return postgresoperator.Observation{}, errors.New("simulated PostgreSQL failure")
	}
	change := execution.Plan.Change
	observation := postgresoperator.Observation{Action: change.Action, Database: change.Database, Role: change.Role, Access: string(change.Access), Verified: true}
	if change.Action == postgresoperator.ActionCreateBackup {
		observation.Backup = &postgresoperator.Backup{ID: change.BackupID, Path: change.BackupPath, SHA256: strings.Repeat("a", 64), SizeBytes: 4096, CreatedAt: time.Now().UTC(), Verified: true}
	}
	if change.Action == postgresoperator.ActionRestoreBackup {
		observation.Restored = true
	}
	return observation, nil
}

func TestPostgresRoleDatabaseBackupRestoreAndFailedRotation(t *testing.T) {
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
	operator := &fakePostgresOperator{instance: postgresoperator.Instance{ID: "postgresql_18_nexa_main", Version: "18", Cluster: "nexa_main", Port: 5432, Status: "online", Owner: "postgres", DataPath: "/var/lib/postgresql/18/nexa_main", SocketPath: "/run/postgresql", LogPath: "/var/log/postgresql/postgresql-18-nexa_main.log", ConfigPath: "/etc/postgresql/18/nexa_main", SystemdUnit: "postgresql@18-nexa_main.service", ManagedByNexa: true}}
	module, err := New(ctx, database, queue, cipher, operator)
	if err != nil {
		t.Fatal(err)
	}
	queue.Start(ctx)
	t.Cleanup(queue.Close)
	if _, err := module.SyncInstances(ctx); err != nil {
		t.Fatal(err)
	}

	role, planJob, err := module.CreateRole(ctx, CreateRoleRequest{InstanceID: operator.instance.ID, Name: "app_role"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitJob(t, queue, planJob.ID, jobs.StateSucceeded)
	stored, err := module.StoredPlan(ctx, resourceRole, role.ID)
	if err != nil {
		t.Fatal(err)
	}
	encodedPlan := string(mustJSON(t, stored))
	if strings.Contains(encodedPlan, "credential_ciphertext") || strings.Contains(encodedPlan, "pending_credential") {
		t.Fatal("encrypted or plaintext credential leaked into the reviewed plan")
	}
	applyJob, err := module.ApplyPlan(ctx, resourceRole, role.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitJob(t, queue, applyJob.ID, jobs.StateSucceeded)
	roles, _ := module.ListRoles(ctx, operator.instance.ID)
	if len(roles) != 1 || roles[0].Status != StatusActive || !roles[0].CredentialAvailable || roles[0].CredentialVersion != 1 || len(operator.secrets) != 1 {
		t.Fatalf("roles=%+v secrets=%d", roles, len(operator.secrets))
	}
	credential, err := module.RevealCredential(ctx, role.ID)
	if err != nil || credential == "" || credential != operator.secrets[0] {
		t.Fatalf("credential reveal failed: %v", err)
	}
	if _, err := module.RevealCredential(ctx, role.ID); err == nil {
		t.Fatal("credential should be revealable exactly once")
	}

	_, rotatePlanJob, err := module.RotateRole(ctx, role.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitJob(t, queue, rotatePlanJob.ID, jobs.StateSucceeded)
	operator.failNext = true
	rotateApplyJob, err := module.ApplyPlan(ctx, resourceRole, role.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitJob(t, queue, rotateApplyJob.ID, jobs.StateFailed)
	roleModel, err := module.getRoleModel(ctx, role.ID)
	if err != nil || Status(roleModel.Status) != StatusActive || roleModel.CredentialCiphertext == nil || roleModel.PendingCredentialCiphertext != nil || roleModel.Failure == nil {
		t.Fatalf("failed rotation did not preserve active credential: %+v, %v", roleModel, err)
	}

	managed, databasePlanJob, err := module.CreateDatabase(ctx, CreateDatabaseRequest{InstanceID: operator.instance.ID, Name: "app_db", OwnerRoleID: role.ID}, nil)
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
