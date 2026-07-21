package persistence

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/uptrace/bun"
)

func openTemp(t *testing.T) *bun.DB {
	t.Helper()
	database, err := Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatalf("open temp database: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database
}

func tableExists(t *testing.T, database *bun.DB, name string) bool {
	t.Helper()
	exists, err := database.NewSelect().
		TableExpr("sqlite_master").
		Where("type = ?", "table").
		Where("name = ?", name).
		Exists(context.Background())
	if err != nil {
		t.Fatalf("inspect table %q: %v", name, err)
	}
	return exists
}

func countRows(t *testing.T, database *bun.DB, table string) int {
	t.Helper()
	count, err := database.NewSelect().TableExpr(table).Count(context.Background())
	if err != nil {
		t.Fatalf("count %q: %v", table, err)
	}
	return count
}

func TestRunMigrationsFreshAppliesAndIsIdempotent(t *testing.T) {
	database := openTemp(t)
	ctx := context.Background()

	if err := RunMigrations(ctx, database); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if !tableExists(t, database, "audit_events") {
		t.Fatal("audit_events table was not created")
	}
	if got := countRows(t, database, "bun_migrations"); got == 0 {
		t.Fatal("expected the audit migration to be recorded in bun_migrations")
	}

	// A second run must be a no-op, not an error (the migration is recorded).
	if err := RunMigrations(ctx, database); err != nil {
		t.Fatalf("second run should be idempotent: %v", err)
	}
}

func TestRunMigrationsPreseedsLegacyInstall(t *testing.T) {
	database := openTemp(t)
	ctx := context.Background()

	// Simulate a database created by the old per-module runner: the
	// schema_migrations ledger exists and records audit v1, and its table is
	// already present. (Open no longer creates schema_migrations — only legacy
	// installs carry it, which is exactly what preseedLegacy keys off of.)
	if _, err := database.ExecContext(ctx, `
		CREATE TABLE schema_migrations (
			module TEXT NOT NULL,
			version INTEGER NOT NULL,
			applied_at TEXT NOT NULL,
			PRIMARY KEY (module, version)
		)`); err != nil {
		t.Fatalf("create legacy ledger: %v", err)
	}
	if _, err := database.ExecContext(ctx,
		`INSERT INTO schema_migrations (module, version, applied_at) VALUES ('audit', 1, 'legacy')`); err != nil {
		t.Fatalf("seed legacy ledger: %v", err)
	}
	if _, err := database.ExecContext(ctx, `
		CREATE TABLE audit_events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			occurred_at TIMESTAMP NOT NULL,
			actor_user_id TEXT,
			action TEXT NOT NULL,
			subject TEXT NOT NULL,
			remote_address TEXT NOT NULL DEFAULT '',
			metadata TEXT NOT NULL DEFAULT '{}'
		)`); err != nil {
		t.Fatalf("seed legacy table: %v", err)
	}

	// RunMigrations must reconcile the ledger rather than re-create audit_events
	// (which would fail with "table already exists"). Reaching here without an
	// error already proves the audit migration was skipped, not replayed.
	if err := RunMigrations(ctx, database); err != nil {
		t.Fatalf("run against legacy install: %v", err)
	}

	// The audit migration must be recorded as a pre-seeded baseline (group 1),
	// distinguishing it from migrations bun actually applied on this run.
	var groupID int64
	if err := database.NewSelect().
		TableExpr("bun_migrations").
		Column("group_id").
		Where("name = ?", "20260721000001").
		Scan(ctx, &groupID); err != nil {
		t.Fatalf("audit migration was not recorded: %v", err)
	}
	if groupID != 1 {
		t.Fatalf("expected audit migration pre-seeded into baseline group 1, got group %d", groupID)
	}
}
