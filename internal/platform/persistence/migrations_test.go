package persistence

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nexa-panel/nexa-panel/migrations"
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

// TestAssertMigrationsCurrentGatesAnUnmigratedDatabase covers the schema half of
// API readiness, which a self-update's activation health gate depends on: a
// binary whose migrations have not run is not ready, however reachable its
// database is.
func TestAssertMigrationsCurrentGatesAnUnmigratedDatabase(t *testing.T) {
	database := openTemp(t)
	ctx := context.Background()

	if err := AssertMigrationsCurrent(ctx, database); err == nil {
		t.Fatal("a database with no migration ledger must not report a current schema")
	}
	if err := RunMigrations(ctx, database); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := AssertMigrationsCurrent(ctx, database); err != nil {
		t.Fatalf("a fully migrated database must report current: %v", err)
	}

	// Drop the most recent ledger row: this is what an older binary rolled back
	// onto a newer schema, or an interrupted migration, leaves behind.
	if _, err := database.ExecContext(ctx, "DELETE FROM bun_migrations WHERE id = (SELECT MAX(id) FROM bun_migrations)"); err != nil {
		t.Fatalf("unapply latest migration: %v", err)
	}
	if err := AssertMigrationsCurrent(ctx, database); err == nil {
		t.Fatal("a partially applied ledger must not report a current schema")
	}
}

// TestSiteTeardownIntegrityMigrationRebuildsTablesWithoutLosingRows covers the
// one migration in the timeline that rebuilds populated tables. scheduled_tasks
// is a parent of scheduled_task_plans, and with foreign keys on a DROP TABLE
// cascades those plans away, so the rebuild has to carry them across; the
// backup-plan join tables have to come out of the JSON columns already stored.
// The migration is exercised by rolling its own down file back over a fully
// migrated database, seeding rows, and re-applying it.
func TestSiteTeardownIntegrityMigrationRebuildsTablesWithoutLosingRows(t *testing.T) {
	database := openTemp(t)
	ctx := context.Background()
	if err := RunMigrations(ctx, database); err != nil {
		t.Fatal(err)
	}
	run := func(file string) {
		body, err := migrations.FS.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, statement := range strings.Split(string(body), "--bun:split") {
			if strings.TrimSpace(statement) == "" {
				continue
			}
			if _, err := database.ExecContext(ctx, statement); err != nil {
				t.Fatalf("%s: %v\n%s", file, err, statement)
			}
		}
	}
	run("20260722000006_site_teardown_integrity.tx.down.sql")
	now := time.Now().UTC()
	if _, err := database.ExecContext(ctx, `
		INSERT INTO sites (id, slug, display_name, primary_domain, php_version, unix_user, root_path, socket_path, status, deployment_mode, created_at, updated_at)
		VALUES ('site_1','blog','Blog','blog.example.com','8.4','nexa_blog','/srv/nexa/sites/blog','/run/php/nexa-blog.sock','active','standard',?,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `
		INSERT INTO scheduled_tasks (id, site_id, name, cron_expression, command, timeout_seconds, enabled, status, pending_removal, created_at, updated_at)
		VALUES ('task_1','site_1','Queue','* * * * *','php x',60,1,'active',0,?,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `
		INSERT INTO scheduled_tasks (id, site_id, name, cron_expression, command, timeout_seconds, enabled, status, pending_removal, created_at, updated_at)
		VALUES ('task_orphan','site_gone','Queue','* * * * *','php x',60,1,'active',0,?,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `
		INSERT INTO scheduled_task_plans (task_id, plan_json, created_at, expires_at) VALUES ('task_1','{}',?,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `
		INSERT INTO sftp_access (site_id, enabled, username, updated_at) VALUES ('site_1',1,'nexa_blog',?)`, now); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `
		INSERT INTO backup_accounts (id, name, type, path, config_json, created_at, updated_at) VALUES ('acct','Local','local','/b','{}',?,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `
		INSERT INTO backup_plans (id, name, account_id, copies_limit, site_ids, database_ids, schedule, enabled, created_at, updated_at)
		VALUES ('p1','Nightly','acct',5,'["site_1"]','["mysql:db_1"]','0 2 * * *',1,?,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	run("20260722000006_site_teardown_integrity.tx.up.sql")

	for table, want := range map[string]int{
		"scheduled_tasks": 1, "scheduled_task_plans": 1, "sftp_access": 1,
		"backup_plan_sites": 1, "backup_plan_databases": 1,
	} {
		if got := countRows(t, database, table); got != want {
			t.Errorf("%s rows = %d, want %d", table, got, want)
		}
	}
	var site string
	if err := database.NewSelect().TableExpr("backup_plan_sites").Column("site_id").Scan(ctx, &site); err != nil || site != "site_1" {
		t.Fatalf("backfilled target = %q, err %v", site, err)
	}
}
