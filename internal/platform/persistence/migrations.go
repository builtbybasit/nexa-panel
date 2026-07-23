package persistence

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nexa-panel/nexa-panel/migrations"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/migrate"
)

// defaultSnapshotRetention caps how many pre-migration control.db snapshots are
// kept next to the live database; older ones are pruned after each new snapshot.
const defaultSnapshotRetention = 5

// MigrateOption configures optional migration behaviour without breaking the
// many callers (tests included) that invoke RunMigrations with just a context
// and a database.
type MigrateOption func(*migrateSettings)

// migrateSettings holds the resolved optional behaviour threaded into the
// testable runMigrations core.
type migrateSettings struct {
	// snapshotStatePath, when set, is the control.db path a pre-migration
	// snapshot is taken next to. An empty value disables snapshotting.
	snapshotStatePath string
	retain            int
	logger            *slog.Logger
}

// WithPreMigrationSnapshot takes a consistent SQLite snapshot of the control
// database, alongside statePath, immediately before applying pending migrations
// to an existing install. statePath is the live control.db path. A snapshot
// failure aborts the migration so a partial or failed upgrade never runs without
// a restore point. Only the most recent defaultSnapshotRetention snapshots are
// kept.
func WithPreMigrationSnapshot(statePath string, logger *slog.Logger) MigrateOption {
	return func(s *migrateSettings) {
		s.snapshotStatePath = statePath
		s.retain = defaultSnapshotRetention
		s.logger = logger
	}
}

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
func RunMigrations(ctx context.Context, database *bun.DB, opts ...MigrateOption) error {
	set := migrate.NewMigrations()
	if err := set.Discover(migrations.FS); err != nil {
		return fmt.Errorf("discover migrations: %w", err)
	}
	var settings migrateSettings
	for _, opt := range opts {
		opt(&settings)
	}
	if settings.logger == nil {
		settings.logger = slog.Default()
	}
	return runMigrations(ctx, database, set, legacyLedger, settings)
}

// AssertMigrationsCurrent reports an error unless every embedded migration is
// recorded as applied in the ledger. It is the schema half of API readiness: a
// binary whose migrations have not run — or which was rolled back onto a
// database a newer build already migrated — must not be reported healthy, and
// must therefore fail an update's activation health gate.
//
// It only reads: an install whose ledger table does not exist yet is not ready
// either, and says so rather than creating one.
func AssertMigrationsCurrent(ctx context.Context, database *bun.DB) error {
	set := migrate.NewMigrations()
	if err := set.Discover(migrations.FS); err != nil {
		return fmt.Errorf("discover migrations: %w", err)
	}
	migrator := migrate.NewMigrator(database, set)
	status, err := migrator.MigrationsWithStatus(ctx)
	if err != nil {
		return fmt.Errorf("inspect migration status: %w", err)
	}
	if pending := status.Unapplied(); len(pending) > 0 {
		return fmt.Errorf("%d migration(s) are not applied, starting at %q", len(pending), pending[0].Name)
	}
	return nil
}

// runMigrations is the testable core: reconcile pre-bun installs, then apply any
// unapplied migrations under an advisory lock so two boots cannot race. When a
// pre-migration snapshot is configured and an existing install is actually being
// upgraded, it snapshots control.db first and aborts on snapshot failure.
func runMigrations(ctx context.Context, database *bun.DB, set *migrate.Migrations, ledger []legacyMigration, settings migrateSettings) error {
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

	// Before altering schema on an existing install, capture a consistent
	// snapshot of control.db so a failed or partial upgrade migration has a
	// restore point. A snapshot failure aborts the migration rather than
	// upgrading blind. To restore: stop the panel, replace control.db with the
	// chosen .bak (removing any -wal/-shm sidecars), and restart.
	if settings.snapshotStatePath != "" {
		status, err := migrator.MigrationsWithStatus(ctx)
		if err != nil {
			return fmt.Errorf("inspect migration status: %w", err)
		}
		pending := status.Unapplied()
		applied := status.Applied()
		// Only an existing install actually being upgraded is worth protecting:
		// a fresh install (applied==0) has no state to lose, and a caught-up
		// install (pending==0) changes nothing.
		if len(pending) > 0 && len(applied) > 0 {
			snapshot, err := snapshotControlDB(ctx, database, settings.snapshotStatePath, settings.retain, settings.logger)
			if err != nil {
				return fmt.Errorf("snapshot control database before migrating: %w", err)
			}
			settings.logger.Info("captured pre-migration control database snapshot", "snapshot", snapshot, "pending", len(pending))
		}
	}

	if _, err := migrator.Migrate(ctx); err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}
	return nil
}

// snapshotControlDB writes a consistent, self-contained copy of the live control
// database to a timestamped sibling of statePath using SQLite's VACUUM INTO.
// VACUUM INTO checkpoints the WAL and produces a single fully-written file,
// unlike a naive file copy of a live WAL-mode database, which could capture a
// torn page set. It refuses to overwrite an existing target and prunes older
// snapshots best-effort.
func snapshotControlDB(ctx context.Context, database *bun.DB, statePath string, retain int, logger *slog.Logger) (string, error) {
	target := fmt.Sprintf("%s.%s.bak", statePath, time.Now().UTC().Format("20060102T150405Z"))
	if _, err := os.Stat(target); err == nil {
		return "", fmt.Errorf("snapshot target %s already exists", target)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("inspect snapshot target %s: %w", target, err)
	}
	// The path is derived, not caller input, but escape single quotes anyway so
	// the VACUUM INTO string literal stays well-formed.
	escaped := strings.ReplaceAll(target, "'", "''")
	if _, err := database.ExecContext(ctx, fmt.Sprintf("VACUUM INTO '%s'", escaped)); err != nil {
		return "", fmt.Errorf("vacuum into %s: %w", target, err)
	}
	if retain > 0 {
		pruneSnapshots(statePath, retain, logger)
	}
	return target, nil
}

// pruneSnapshots keeps only the newest retain snapshots of statePath, removing
// the rest. The timestamped names sort chronologically, so lexical order is
// chronological order. Prune errors are logged, not fatal: a stale snapshot next
// to a freshly written one is harmless.
func pruneSnapshots(statePath string, retain int, logger *slog.Logger) {
	matches, err := filepath.Glob(statePath + ".*.bak")
	if err != nil {
		logger.Warn("could not list control database snapshots to prune", "error", err)
		return
	}
	if len(matches) <= retain {
		return
	}
	sort.Strings(matches)
	for _, stale := range matches[:len(matches)-retain] {
		if err := os.Remove(stale); err != nil {
			logger.Warn("could not prune old control database snapshot", "snapshot", stale, "error", err)
		}
	}
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
