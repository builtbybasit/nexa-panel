package postgres

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/platform/audit"
	"github.com/nexa-panel/nexa-panel/internal/platform/jobs"
	postgresoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/postgres"
	"github.com/nexa-panel/nexa-panel/internal/platform/persistence"
	"github.com/nexa-panel/nexa-panel/internal/platform/secrets"
)

var sizeTestClock = time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)

// newSizeTestModule builds a module over a real SQLite file holding one online
// instance and the named databases, already active. Sizing only ever looks at
// active databases, so driving each one through the full plan/apply flow would
// cost seconds of job polling and say nothing extra about measurement.
func newSizeTestModule(t *testing.T, databases ...string) (*Module, *fakePostgresOperator) {
	t.Helper()
	ctx := context.Background()
	store, err := persistence.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := persistence.RunMigrations(context.Background(), store); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	auditLog, err := audit.New(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	queue, err := jobs.NewWithConfig(ctx, store, auditLog, slog.New(slog.NewTextHandler(io.Discard, nil)), jobs.Config{PollInterval: 5 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := secrets.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	operator := &fakePostgresOperator{instance: postgresoperator.Instance{ID: "postgresql_18_nexa_main", Version: "18", Cluster: "nexa_main", Port: 5432, Status: "online", Owner: "postgres", DataPath: "/var/lib/postgresql/18/nexa_main", SocketPath: "/run/postgresql", LogPath: "/var/log/postgresql/postgresql-18-nexa_main.log", ConfigPath: "/etc/postgresql/18/nexa_main", SystemdUnit: "postgresql@18-nexa_main.service", ManagedByNexa: true}}
	module, err := New(ctx, store, queue, cipher, operator)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := module.SyncInstances(ctx); err != nil {
		t.Fatal(err)
	}
	role := &roleModel{ID: "role_owner", InstanceID: operator.instance.ID, Name: "app_owner", Status: string(StatusActive), CreatedAt: sizeTestClock, UpdatedAt: sizeTestClock}
	if _, err := store.NewInsert().Model(role).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	for _, name := range databases {
		model := &databaseModel{ID: "database_" + name, InstanceID: operator.instance.ID, Name: name, OwnerRoleID: role.ID, Status: string(StatusActive), CreatedAt: sizeTestClock, UpdatedAt: sizeTestClock}
		if _, err := store.NewInsert().Model(model).Exec(ctx); err != nil {
			t.Fatal(err)
		}
	}
	return module, operator
}

func TestDatabaseSizesArePersistedAndReusedWithinTheRefreshInterval(t *testing.T) {
	ctx := context.Background()
	module, operator := newSizeTestModule(t, "app_db", "webmail")
	operator.sizes = map[string]int64{"app_db": 491520, "webmail": 278528}
	clock := sizeTestClock
	module.now = func() time.Time { return clock }

	items, err := module.SyncDatabaseSizes(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].SizeBytes == nil || *items[0].SizeBytes != 491520 || items[1].SizeBytes == nil || *items[1].SizeBytes != 278528 {
		t.Fatalf("sizes were not persisted: %+v", items)
	}
	// One query covers every database on the instance; probing per database
	// would multiply agent round trips by the number of rows on screen.
	if operator.sizeCalls != 1 {
		t.Fatalf("probes = %d, want 1 for two databases on one instance", operator.sizeCalls)
	}
	if items[0].SizeObservedAt == nil || !items[0].SizeObservedAt.Equal(sizeTestClock) {
		t.Fatalf("observedAt = %v, want the measurement time", items[0].SizeObservedAt)
	}

	// Inside the interval the stored value is served as-is, even though the
	// host has since changed.
	clock = sizeTestClock.Add(sizeRefreshInterval - time.Second)
	operator.sizes = map[string]int64{"app_db": 999999, "webmail": 999999}
	items, err = module.SyncDatabaseSizes(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if operator.sizeCalls != 1 {
		t.Fatalf("probes = %d, want no re-probe inside the refresh interval", operator.sizeCalls)
	}
	if *items[0].SizeBytes != 491520 {
		t.Fatalf("cached size = %d, want the previously measured 491520", *items[0].SizeBytes)
	}

	// Once it lapses, the next read re-measures.
	clock = sizeTestClock.Add(sizeRefreshInterval + time.Second)
	items, err = module.SyncDatabaseSizes(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if operator.sizeCalls != 2 {
		t.Fatalf("probes = %d, want a re-probe after the refresh interval lapsed", operator.sizeCalls)
	}
	if *items[0].SizeBytes != 999999 {
		t.Fatalf("refreshed size = %d, want 999999", *items[0].SizeBytes)
	}
}

// An instance can be down, starting, or mid-restore while its databases still
// need to render, so a failed probe must not take the list down with it.
func TestDatabaseSizeProbeFailureKeepsTheLastKnownSizeAndStillServes(t *testing.T) {
	ctx := context.Background()
	module, operator := newSizeTestModule(t, "app_db")
	operator.sizes = map[string]int64{"app_db": 491520}
	clock := sizeTestClock
	module.now = func() time.Time { return clock }

	if _, err := module.SyncDatabaseSizes(ctx, ""); err != nil {
		t.Fatal(err)
	}

	operator.sizeError = errors.New("the database system is starting up")
	clock = sizeTestClock.Add(sizeRefreshInterval + time.Second)
	items, err := module.SyncDatabaseSizes(ctx, "")
	if err != nil {
		t.Fatalf("a failed size probe took down the whole list: %v", err)
	}
	if len(items) != 1 || items[0].SizeBytes == nil || *items[0].SizeBytes != 491520 {
		t.Fatalf("last known size was lost on probe failure: %+v", items)
	}
	// observedAt stays at the last real measurement so callers can see the
	// number has gone stale rather than trusting it as current.
	if items[0].SizeObservedAt == nil || !items[0].SizeObservedAt.Equal(sizeTestClock) {
		t.Fatalf("observedAt = %v, want the last successful measurement", items[0].SizeObservedAt)
	}
}

func TestDatabaseMeasuredAtZeroIsDistinctFromNeverMeasured(t *testing.T) {
	ctx := context.Background()
	module, operator := newSizeTestModule(t, "empty_db", "vanished_db")
	// empty_db exists on the host and holds nothing; vanished_db was dropped
	// out of band and so is absent from the probe entirely.
	operator.sizes = map[string]int64{"empty_db": 0}
	module.now = func() time.Time { return sizeTestClock }

	items, err := module.SyncDatabaseSizes(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("items = %+v", items)
	}
	if items[0].SizeBytes == nil || *items[0].SizeBytes != 0 {
		t.Fatalf("empty_db size = %v, want a present zero rather than nil", items[0].SizeBytes)
	}
	if items[1].SizeBytes != nil {
		t.Fatalf("vanished_db size = %v, want nil rather than a fabricated zero", *items[1].SizeBytes)
	}
}

func TestDatabaseSizesSkipInstancesWithNothingActiveToMeasure(t *testing.T) {
	ctx := context.Background()
	module, operator := newSizeTestModule(t)
	module.now = func() time.Time { return sizeTestClock }

	if _, err := module.SyncDatabaseSizes(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if operator.sizeCalls != 0 {
		t.Fatalf("probes = %d, want none when there is no active database to measure", operator.sizeCalls)
	}
}
