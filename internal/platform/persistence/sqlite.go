package persistence

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/sqlitedialect"
	_ "modernc.org/sqlite"
)

type schemaMigration struct {
	bun.BaseModel `bun:"table:schema_migrations"`
	Module        string    `bun:",pk"`
	Version       int       `bun:",pk"`
	AppliedAt     time.Time `bun:",notnull"`
}

// Open creates the small, local control-plane database. Feature modules own
// their schemas and apply them through Migrate.
func Open(path string) (*bun.DB, error) {
	if path == "" {
		return nil, errors.New("database path is required")
	}

	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve database path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(absolute), 0o750); err != nil {
		return nil, fmt.Errorf("create database directory: %w", err)
	}

	dsn := (&url.URL{Scheme: "file", Path: absolute}).String() +
		"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)"
	sqlDatabase, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite database: %w", err)
	}
	// A single writer is enough for this low-traffic control plane and avoids
	// lock contention while keeping connection overhead predictable.
	sqlDatabase.SetMaxOpenConns(1)
	sqlDatabase.SetMaxIdleConns(1)
	sqlDatabase.SetConnMaxLifetime(0)
	database := bun.NewDB(sqlDatabase, sqlitedialect.New())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := database.PingContext(ctx); err != nil {
		_ = database.Close()
		return nil, fmt.Errorf("ping sqlite database: %w", err)
	}
	if err := ensureMigrationTable(ctx, database); err != nil {
		_ = database.Close()
		return nil, err
	}
	return database, nil
}

func ensureMigrationTable(ctx context.Context, database *bun.DB) error {
	_, err := database.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			module TEXT NOT NULL,
			version INTEGER NOT NULL,
			applied_at TEXT NOT NULL,
			PRIMARY KEY (module, version)
		)`)
	if err != nil {
		return fmt.Errorf("create migration table: %w", err)
	}
	return nil
}

// Migrate applies a module's append-only migration list in order.
func Migrate(ctx context.Context, database *bun.DB, moduleName string, migrations []string) error {
	if database == nil || moduleName == "" {
		return errors.New("database and module name are required")
	}
	for index, statement := range migrations {
		version := index + 1
		applied, err := database.NewSelect().Model((*schemaMigration)(nil)).
			Where("module = ?", moduleName).Where("version = ?", version).Exists(ctx)
		if err != nil {
			return fmt.Errorf("inspect %s migration %d: %w", moduleName, version, err)
		}
		if applied {
			continue
		}

		tx, err := database.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin %s migration %d: %w", moduleName, version, err)
		}
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("apply %s migration %d: %w", moduleName, version, err)
		}
		migration := &schemaMigration{Module: moduleName, Version: version, AppliedAt: time.Now().UTC()}
		if _, err := tx.NewInsert().Model(migration).Exec(ctx); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("record %s migration %d: %w", moduleName, version, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit %s migration %d: %w", moduleName, version, err)
		}
	}
	return nil
}
