package persistence

import (
	"context"
	"fmt"

	"github.com/nexa-panel/nexa-panel/migrations"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/migrate"
)

// legacyMigration maps a converted migration to the (module, version) it used to
// occupy under the pre-bun schema_migrations ledger. It lets installs created by
// the old per-module runner skip re-applying DDL they already ran. Delete an
// entry once no install predating its cutover can still exist.
type legacyMigration struct {
	Name    string // bun migration name (the timestamp prefix), e.g. "20260721000001"
	Module  string
	Version int
}

// legacyLedger lists every migration that also shipped under the old runner, so
// pre-existing databases are reconciled into the bun ledger instead of having
// their DDL re-executed. Append one entry per migration as modules are converted.
var legacyLedger = []legacyMigration{
	{Name: "20260721000001", Module: "audit", Version: 1},
	{Name: "20260721000002", Module: "identity", Version: 1},
	{Name: "20260721000003", Module: "identity", Version: 2},
	{Name: "20260721000004", Module: "identity", Version: 3},
	{Name: "20260721000005", Module: "jobs", Version: 1},
	{Name: "20260721000006", Module: "jobs", Version: 2},
	{Name: "20260721000007", Module: "sites", Version: 1},
	{Name: "20260721000008", Module: "domains", Version: 1},
	{Name: "20260721000009", Module: "domains", Version: 2},
	{Name: "20260721000010", Module: "certificates", Version: 1},
	{Name: "20260721000011", Module: "databases", Version: 1},
	{Name: "20260721000012", Module: "databases", Version: 2},
	{Name: "20260721000013", Module: "mysql_databases", Version: 1},
	{Name: "20260721000014", Module: "mysql_databases", Version: 2},
	{Name: "20260721000015", Module: "admin_tools", Version: 1},
	{Name: "20260721000016", Module: "admin_tools", Version: 2},
	{Name: "20260721000017", Module: "schedules", Version: 1},
	{Name: "20260721000018", Module: "applications", Version: 1},
	{Name: "20260721000019", Module: "sftp", Version: 1},
	{Name: "20260721000020", Module: "backups", Version: 1},
	{Name: "20260721000021", Module: "backups", Version: 2},
	{Name: "20260721000022", Module: "backups", Version: 3},
	{Name: "20260721000023", Module: "backups", Version: 4},
	{Name: "20260721000024", Module: "backups", Version: 5},
	{Name: "20260721000025", Module: "backups", Version: 6},
}

// RunMigrations brings the control-plane schema up to date. It is called once,
// in the API composition root, after Open and before the module constructors.
func RunMigrations(ctx context.Context, database *bun.DB) error {
	set := migrate.NewMigrations()
	if err := set.Discover(migrations.FS); err != nil {
		return fmt.Errorf("discover migrations: %w", err)
	}
	return runMigrations(ctx, database, set, legacyLedger)
}

// runMigrations is the testable core: reconcile pre-bun installs, then apply any
// unapplied migrations under an advisory lock so two boots cannot race.
func runMigrations(ctx context.Context, database *bun.DB, set *migrate.Migrations, ledger []legacyMigration) error {
	// WithMarkAppliedOnSuccess records a migration only after its statements
	// succeed, so a failed migration is retried rather than silently skipped.
	migrator := migrate.NewMigrator(database, set, migrate.WithMarkAppliedOnSuccess(true))
	if err := migrator.Init(ctx); err != nil {
		return fmt.Errorf("init migration tables: %w", err)
	}
	if err := preseedLegacy(ctx, migrator, database, set, ledger); err != nil {
		return err
	}
	if err := migrator.Lock(ctx); err != nil {
		return fmt.Errorf("acquire migration lock: %w", err)
	}
	defer func() { _ = migrator.Unlock(ctx) }()
	if _, err := migrator.Migrate(ctx); err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}
	return nil
}

// preseedLegacy marks converted migrations as already applied when the old
// schema_migrations ledger shows the equivalent (module, version) ran. On a
// fresh database (no legacy rows) it is a no-op and bun applies everything.
func preseedLegacy(ctx context.Context, migrator *migrate.Migrator, database *bun.DB, set *migrate.Migrations, ledger []legacyMigration) error {
	hasLegacy, err := legacyTableExists(ctx, database)
	if err != nil {
		return fmt.Errorf("inspect legacy ledger: %w", err)
	}
	if !hasLegacy {
		return nil
	}

	applied, err := migrator.AppliedMigrations(ctx)
	if err != nil {
		return fmt.Errorf("read applied migrations: %w", err)
	}
	alreadyMarked := make(map[string]bool, len(applied))
	for _, m := range applied {
		alreadyMarked[m.Name] = true
	}

	byName := make(map[string]migrate.Migration)
	for _, m := range set.Sorted() {
		byName[m.Name] = m
	}

	for _, entry := range ledger {
		if alreadyMarked[entry.Name] {
			continue
		}
		present, err := legacyRowExists(ctx, database, entry.Module, entry.Version)
		if err != nil {
			return fmt.Errorf("inspect legacy %s v%d: %w", entry.Module, entry.Version, err)
		}
		if !present {
			continue
		}
		migration, ok := byName[entry.Name]
		if !ok {
			return fmt.Errorf("legacy ledger references unknown migration %q", entry.Name)
		}
		// GroupID 1 collapses every pre-existing migration into a single
		// baseline batch; migrations added after the cutover get later groups.
		migration.GroupID = 1
		if err := migrator.MarkApplied(ctx, &migration); err != nil {
			return fmt.Errorf("pre-seed migration %q: %w", entry.Name, err)
		}
	}
	return nil
}

func legacyTableExists(ctx context.Context, database *bun.DB) (bool, error) {
	return database.NewSelect().
		TableExpr("sqlite_master").
		Where("type = ?", "table").
		Where("name = ?", "schema_migrations").
		Exists(ctx)
}

func legacyRowExists(ctx context.Context, database *bun.DB, module string, version int) (bool, error) {
	return database.NewSelect().
		TableExpr("schema_migrations").
		Where("module = ?", module).
		Where("version = ?", version).
		Exists(ctx)
}
