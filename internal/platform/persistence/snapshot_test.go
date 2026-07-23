package persistence

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/migrate"
)

// discardLogger keeps snapshot logging out of test output.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// openTempAt opens a control database at an explicit path so the snapshot target
// (derived from that path) can be inspected.
func openTempAt(t *testing.T, path string) *bun.DB {
	t.Helper()
	database, err := Open(path)
	if err != nil {
		t.Fatalf("open temp database at %s: %v", path, err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database
}

// sqlSet builds an in-memory migration set from name -> up-SQL pairs. Each entry
// becomes a discoverable <name>.up.sql (with a no-op down) so the tests exercise
// the real bun migrator without touching the shipped migration files.
func sqlSet(t *testing.T, files map[string]string) *migrate.Migrations {
	t.Helper()
	fsys := fstest.MapFS{}
	for name, up := range files {
		fsys[name+".up.sql"] = &fstest.MapFile{Data: []byte(up)}
		fsys[name+".down.sql"] = &fstest.MapFile{Data: []byte("SELECT 1;")}
	}
	set := migrate.NewMigrations()
	if err := set.Discover(fsys); err != nil {
		t.Fatalf("discover in-memory migrations: %v", err)
	}
	return set
}

func snapshotSettings(statePath string) migrateSettings {
	return migrateSettings{snapshotStatePath: statePath, retain: defaultSnapshotRetention, logger: discardLogger()}
}

func snapshotFiles(t *testing.T, statePath string) []string {
	t.Helper()
	matches, err := filepath.Glob(statePath + ".*.bak")
	if err != nil {
		t.Fatalf("glob snapshots: %v", err)
	}
	return matches
}

func TestRunMigrationsSnapshotsWhenUpgradingExistingInstall(t *testing.T) {
	ctx := context.Background()
	statePath := filepath.Join(t.TempDir(), "control.db")
	database := openTempAt(t, statePath)

	// An existing install: migration 0001 has already been applied.
	first := sqlSet(t, map[string]string{"0001_create_t1": "CREATE TABLE t1 (id INTEGER PRIMARY KEY);"})
	if err := runMigrations(ctx, database, first, nil, migrateSettings{}); err != nil {
		t.Fatalf("seed existing install: %v", err)
	}

	// Now upgrade it: 0002 is pending. A snapshot must be taken first.
	second := sqlSet(t, map[string]string{
		"0001_create_t1": "CREATE TABLE t1 (id INTEGER PRIMARY KEY);",
		"0002_create_t2": "CREATE TABLE t2 (id INTEGER PRIMARY KEY);",
	})
	if err := runMigrations(ctx, database, second, nil, snapshotSettings(statePath)); err != nil {
		t.Fatalf("upgrade run: %v", err)
	}
	if !tableExists(t, database, "t2") {
		t.Fatal("the pending migration was not applied")
	}

	snapshots := snapshotFiles(t, statePath)
	if len(snapshots) != 1 {
		t.Fatalf("expected exactly one snapshot, got %d: %v", len(snapshots), snapshots)
	}

	// The snapshot must be a consistent, self-contained copy of the pre-upgrade
	// state: opening it as a fresh database shows only the single migration that
	// was applied before the upgrade ran.
	snap := openTempAt(t, snapshots[0])
	if got := countRows(t, snap, "bun_migrations"); got != 1 {
		t.Fatalf("snapshot recorded %d migrations, want the pre-upgrade count of 1", got)
	}
	if tableExists(t, snap, "t2") {
		t.Fatal("snapshot captured t2, but it should predate the upgrade migration")
	}
}

func TestRunMigrationsSkipsSnapshotOnFreshInstall(t *testing.T) {
	ctx := context.Background()
	statePath := filepath.Join(t.TempDir(), "control.db")
	database := openTempAt(t, statePath)

	set := sqlSet(t, map[string]string{"0001_create_t1": "CREATE TABLE t1 (id INTEGER PRIMARY KEY);"})
	if err := runMigrations(ctx, database, set, nil, snapshotSettings(statePath)); err != nil {
		t.Fatalf("fresh run: %v", err)
	}
	if !tableExists(t, database, "t1") {
		t.Fatal("the fresh migration was not applied")
	}
	if snapshots := snapshotFiles(t, statePath); len(snapshots) != 0 {
		t.Fatalf("a fresh install has nothing to snapshot, got %v", snapshots)
	}
}

func TestSnapshotFailureAbortsMigration(t *testing.T) {
	ctx := context.Background()
	statePath := filepath.Join(t.TempDir(), "control.db")
	database := openTempAt(t, statePath)

	// Seed an existing install so the upgrade path (and thus a snapshot) is taken.
	first := sqlSet(t, map[string]string{"0001_create_t1": "CREATE TABLE t1 (id INTEGER PRIMARY KEY);"})
	if err := runMigrations(ctx, database, first, nil, migrateSettings{}); err != nil {
		t.Fatalf("seed existing install: %v", err)
	}

	// Point the snapshot at a directory that does not exist so VACUUM INTO fails.
	badPath := filepath.Join(t.TempDir(), "missing-dir", "control.db")
	settings := migrateSettings{snapshotStatePath: badPath, retain: defaultSnapshotRetention, logger: discardLogger()}

	second := sqlSet(t, map[string]string{
		"0001_create_t1": "CREATE TABLE t1 (id INTEGER PRIMARY KEY);",
		"0002_create_t2": "CREATE TABLE t2 (id INTEGER PRIMARY KEY);",
	})
	if err := runMigrations(ctx, database, second, nil, settings); err == nil {
		t.Fatal("expected a snapshot failure to abort the migration")
	}
	if tableExists(t, database, "t2") {
		t.Fatal("the pending migration must not apply when the pre-migration snapshot fails")
	}
}

func TestPruneSnapshotsRetainsNewest(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "control.db")

	// Eight snapshots with sortable timestamps; only the newest five should stay.
	stamps := []string{
		"20260101T000000Z", "20260102T000000Z", "20260103T000000Z", "20260104T000000Z",
		"20260105T000000Z", "20260106T000000Z", "20260107T000000Z", "20260108T000000Z",
	}
	for _, stamp := range stamps {
		name := fmt.Sprintf("%s.%s.bak", statePath, stamp)
		if err := os.WriteFile(name, []byte("snap"), 0o600); err != nil {
			t.Fatalf("write fake snapshot: %v", err)
		}
	}

	pruneSnapshots(statePath, 5, discardLogger())

	remaining := snapshotFiles(t, statePath)
	if len(remaining) != 5 {
		t.Fatalf("expected 5 snapshots retained, got %d: %v", len(remaining), remaining)
	}
	for _, stamp := range stamps[:3] {
		if _, err := os.Stat(fmt.Sprintf("%s.%s.bak", statePath, stamp)); !os.IsNotExist(err) {
			t.Fatalf("expected oldest snapshot %s to be pruned", stamp)
		}
	}
	for _, stamp := range stamps[3:] {
		if _, err := os.Stat(fmt.Sprintf("%s.%s.bak", statePath, stamp)); err != nil {
			t.Fatalf("expected newest snapshot %s to be retained: %v", stamp, err)
		}
	}
}
