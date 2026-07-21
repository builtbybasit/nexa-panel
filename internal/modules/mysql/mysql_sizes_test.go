package mysql

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
	mysqloperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/mysql"
	"github.com/nexa-panel/nexa-panel/internal/platform/persistence"
	"github.com/nexa-panel/nexa-panel/internal/platform/secrets"
)

var sizeTestClock = time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)

// newSizeTestModule builds a module over a real SQLite file holding one online
// engine and the named databases, already active. Sizing only ever looks at
// active databases, so driving each one through the full plan/apply flow would
// cost seconds of job polling and say nothing extra about measurement.
func newSizeTestModule(t *testing.T, databases ...string) (*Module, *fakeMySQLOperator) {
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
	operator := &fakeMySQLOperator{engine: mysqloperator.Engine{ID: "mysql", Kind: mysqloperator.EngineMySQL, Version: "8.4.5", VersionText: "8.4.5 MySQL Community Server", Port: 3306, Status: "online", SocketPath: "/run/mysqld/mysqld.sock", SystemdUnit: "mysql.service"}}
	module, err := New(ctx, store, queue, cipher, operator)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := module.SyncEngines(ctx); err != nil {
		t.Fatal(err)
	}
	account := &accountModel{ID: "account_owner", EngineID: operator.engine.ID, Name: "app_owner", Host: "localhost", Status: string(StatusActive), CreatedAt: sizeTestClock, UpdatedAt: sizeTestClock}
	if _, err := store.NewInsert().Model(account).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	for _, name := range databases {
		model := &databaseModel{ID: "database_" + name, EngineID: operator.engine.ID, Name: name, OwnerAccountID: account.ID, Status: string(StatusActive), CreatedAt: sizeTestClock, UpdatedAt: sizeTestClock}
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
	// One aggregate covers every schema on the engine.
	if operator.sizeCalls != 1 {
		t.Fatalf("probes = %d, want 1 for two databases on one engine", operator.sizeCalls)
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

// The engine can be down or restarting while its databases still need to
// render, so a failed probe must not take the list down with it.
func TestDatabaseSizeProbeFailureKeepsTheLastKnownSizeAndStillServes(t *testing.T) {
	ctx := context.Background()
	module, operator := newSizeTestModule(t, "app_db")
	operator.sizes = map[string]int64{"app_db": 491520}
	clock := sizeTestClock
	module.now = func() time.Time { return clock }

	if _, err := module.SyncDatabaseSizes(ctx, ""); err != nil {
		t.Fatal(err)
	}

	operator.sizeError = errors.New("can't connect to local MySQL server")
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
	// empty_db exists on the host and holds no tables; vanished_db was dropped
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

func TestDatabaseSizesSkipTheProbeWithNothingActiveToMeasure(t *testing.T) {
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
