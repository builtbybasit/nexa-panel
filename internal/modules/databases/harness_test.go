package databases

import (
	"context"
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
	postgresoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/postgres"
	"github.com/nexa-panel/nexa-panel/internal/platform/persistence"
	"github.com/nexa-panel/nexa-panel/internal/platform/secrets"
)

var testClock = time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)

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
		return mysqloperator.Observation{}, errors.New("simulated MySQL failure")
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
	if change.Action == postgresoperator.ActionProvision {
		instance := f.instance
		instance.ID = change.InstanceID
		instance.Version, instance.Cluster, instance.Port = change.Version, change.Cluster, change.Port
		observation.Instance = &instance
	}
	if change.Action == postgresoperator.ActionCreateBackup {
		observation.Backup = &postgresoperator.Backup{ID: change.BackupID, Path: change.BackupPath, SHA256: strings.Repeat("a", 64), SizeBytes: 4096, CreatedAt: time.Now().UTC(), Verified: true}
	}
	if change.Action == postgresoperator.ActionRestoreBackup {
		observation.Restored = true
	}
	return observation, nil
}

type testHarness struct {
	module   *Module
	queue    *jobs.Module
	mysql    *fakeMySQLOperator
	postgres *fakePostgresOperator
}

// newTestModule builds the unified module over a real SQLite file with one
// discovered server per engine, the job queue running, and both fakes wired.
func newTestModule(t *testing.T) *testHarness {
	t.Helper()
	ctx := context.Background()
	database, err := persistence.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := persistence.RunMigrations(ctx, database); err != nil {
		t.Fatalf("run migrations: %v", err)
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
	mysqlOperator := &fakeMySQLOperator{engine: mysqloperator.Engine{ID: "mysql", Kind: mysqloperator.EngineMariaDB, Version: "10.11", VersionText: "10.11.9-MariaDB", Port: 3306, Status: "online", SocketPath: "/run/mysqld/mysqld.sock", SystemdUnit: "mariadb.service"}}
	postgresOperator := &fakePostgresOperator{instance: postgresoperator.Instance{ID: "postgresql_18_nexa_main", Version: "18", Cluster: "nexa_main", Port: 5432, Status: "online", Owner: "postgres", DataPath: "/var/lib/postgresql/18/nexa_main", SocketPath: "/run/postgresql", LogPath: "/var/log/postgresql/postgresql-18-nexa_main.log", ConfigPath: "/etc/postgresql/18/nexa_main", SystemdUnit: "postgresql@18-nexa_main.service", ManagedByNexa: true}}
	module, err := New(ctx, database, queue, cipher, mysqlOperator, postgresOperator)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := module.SyncServers(ctx); err != nil {
		t.Fatal(err)
	}
	return &testHarness{module: module, queue: queue, mysql: mysqlOperator, postgres: postgresOperator}
}

func (h *testHarness) start(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	h.queue.Start(ctx)
	t.Cleanup(h.queue.Close)
}

func (h *testHarness) engine(t *testing.T, key string) *engine {
	t.Helper()
	eng, err := h.module.engineByKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return eng
}

func (h *testHarness) serverID(key string) string {
	if key == "mysql" {
		return h.mysql.engine.ID
	}
	return h.postgres.instance.ID
}

func (h *testHarness) operatorSecrets(key string) []string {
	if key == "mysql" {
		return h.mysql.secrets
	}
	return h.postgres.secrets
}

func (h *testHarness) failNextApply(key string) {
	if key == "mysql" {
		h.mysql.failNext = true
		return
	}
	h.postgres.failNext = true
}

// seedUser inserts an already-active user, bypassing the plan/apply flow, for
// tests that only exercise reads or guards.
func (h *testHarness) seedUser(t *testing.T, key, id, name string) {
	t.Helper()
	eng := h.engine(t, key)
	now := testClock
	if err := eng.store.InsertUser(context.Background(), userRow{ID: id, ServerID: h.serverID(key), Name: name, Host: "localhost", CreatedAt: now, UpdatedAt: now, Status: string(StatusActive)}); err != nil {
		t.Fatal(err)
	}
}

// seedDatabase inserts an already-active database owned by the given user.
func (h *testHarness) seedDatabase(t *testing.T, key, id, name, ownerID string) {
	t.Helper()
	eng := h.engine(t, key)
	now := testClock
	if err := eng.store.InsertDatabase(context.Background(), databaseRow{ID: id, ServerID: h.serverID(key), Name: name, OwnerUserID: ownerID, Status: string(StatusActive), CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
}

// seedGrant inserts an already-active grant.
func (h *testHarness) seedGrant(t *testing.T, key, id, databaseID, userID string) {
	t.Helper()
	eng := h.engine(t, key)
	now := testClock
	if err := eng.store.InsertGrant(context.Background(), grantRow{ID: id, DatabaseID: databaseID, UserID: userID, Access: AccessReadOnly, Status: string(StatusActive), CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
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
