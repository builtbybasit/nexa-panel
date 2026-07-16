package postgres

import (
	"context"

	"errors"
	"fmt"

	"path/filepath"

	"os"
	"strconv"

	"strings"
)

func (o *HostOperator) createBackup(ctx context.Context, change Change) (Observation, error) {
	if err := preparePostgresDirectory(o.backupRoot, filepath.Dir(change.BackupPath)); err != nil {
		return Observation{}, err
	}
	temporary := change.BackupPath + ".partial"
	_ = os.Remove(temporary)
	command := asPostgres(change.Version, "pg_dump", "--host", o.socketRoot, "--port", strconv.Itoa(change.Port), "--username", "postgres", "--format", "custom", "--no-owner", "--no-privileges", "--file", temporary, change.Database)
	if output, err := o.runner.Run(ctx, command); err != nil {
		_ = os.Remove(temporary)
		return Observation{}, commandError("create PostgreSQL logical backup", output, err)
	}
	if output, err := o.runner.Run(ctx, asPostgres(change.Version, "pg_restore", "--list", temporary)); err != nil {
		_ = os.Remove(temporary)
		return Observation{}, commandError("verify PostgreSQL logical backup", output, err)
	}
	if err := os.Rename(temporary, change.BackupPath); err != nil {
		_ = os.Remove(temporary)
		return Observation{}, fmt.Errorf("activate PostgreSQL backup: %w", err)
	}
	if err := os.Chmod(change.BackupPath, 0o640); err != nil {
		return Observation{}, fmt.Errorf("secure PostgreSQL backup: %w", err)
	}
	digest, size, err := fileDigest(change.BackupPath)
	if err != nil {
		return Observation{}, err
	}
	backup := &Backup{ID: change.BackupID, Path: change.BackupPath, SHA256: digest, SizeBytes: size, CreatedAt: o.now().UTC(), Verified: true}
	return Observation{Action: change.Action, Database: change.Database, Backup: backup, Verified: true}, nil
}

func (o *HostOperator) restoreBackup(ctx context.Context, change Change) (Observation, error) {
	digest, _, err := fileDigest(change.BackupPath)
	if err != nil {
		return Observation{}, err
	}
	if digest != change.BackupSHA256 {
		return Observation{}, errors.New("PostgreSQL restore point checksum does not match")
	}
	suffix := strings.ToLower(change.RestoreToken)
	if len(suffix) > 8 {
		suffix = suffix[:8]
	}
	temporary := truncateName(change.Database + "_nexa_restore_" + suffix)
	previous := truncateName(change.Database + "_nexa_previous_" + suffix)
	cleanup := func(name string) {
		_, _ = o.runner.Run(context.WithoutCancel(ctx), asPostgres(change.Version, "dropdb", "--if-exists", "--force", "--host", o.socketRoot, "--port", strconv.Itoa(change.Port), "--username", "postgres", name))
	}
	cleanup(temporary)
	cleanup(previous)
	create := asPostgres(change.Version, "createdb", "--host", o.socketRoot, "--port", strconv.Itoa(change.Port), "--username", "postgres", "--owner", change.OwnerRole, "--encoding", "UTF8", "--template", "template0", temporary)
	if output, err := o.runner.Run(ctx, create); err != nil {
		return Observation{}, commandError("create PostgreSQL restore staging database", output, err)
	}
	restore := asPostgres(change.Version, "pg_restore", "--host", o.socketRoot, "--port", strconv.Itoa(change.Port), "--username", "postgres", "--dbname", temporary, "--exit-on-error", "--no-owner", "--no-privileges", "--role", change.OwnerRole, change.BackupPath)
	if output, err := o.runner.Run(ctx, restore); err != nil {
		cleanup(temporary)
		return Observation{}, commandError("restore PostgreSQL archive", output, err)
	}
	if err := o.verifyDatabase(ctx, change, temporary); err != nil {
		cleanup(temporary)
		return Observation{}, err
	}
	originalExisted := o.databaseExists(ctx, change, change.Database)
	admin := asPostgres(change.Version, "psql", "--no-psqlrc", "--host", o.socketRoot, "--port", strconv.Itoa(change.Port), "--username", "postgres", "--dbname", "postgres", "--set", "ON_ERROR_STOP=1")
	if originalExisted {
		admin.Stdin = terminateSQL(change.Database) + terminateSQL(temporary) + "ALTER DATABASE " + quoteIdentifier(change.Database) + " RENAME TO " + quoteIdentifier(previous) + ";\nALTER DATABASE " + quoteIdentifier(temporary) + " RENAME TO " + quoteIdentifier(change.Database) + ";\n"
	} else {
		admin.Stdin = terminateSQL(temporary) + "ALTER DATABASE " + quoteIdentifier(temporary) + " RENAME TO " + quoteIdentifier(change.Database) + ";\n"
	}
	if output, err := o.runner.Run(ctx, admin); err != nil {
		if originalExisted {
			o.repairFailedSwap(ctx, change, previous)
		}
		cleanup(temporary)
		return Observation{}, commandError("activate restored PostgreSQL database", output, err)
	}
	if err := o.verifyDatabase(ctx, change, change.Database); err != nil {
		rollback := admin
		if originalExisted {
			rollback.Stdin = terminateSQL(change.Database) + terminateSQL(previous) + "ALTER DATABASE " + quoteIdentifier(change.Database) + " RENAME TO " + quoteIdentifier(temporary) + ";\nALTER DATABASE " + quoteIdentifier(previous) + " RENAME TO " + quoteIdentifier(change.Database) + ";\n"
		} else {
			rollback.Stdin = terminateSQL(change.Database) + "ALTER DATABASE " + quoteIdentifier(change.Database) + " RENAME TO " + quoteIdentifier(temporary) + ";\n"
		}
		_, _ = o.runner.Run(context.WithoutCancel(ctx), rollback)
		cleanup(temporary)
		return Observation{}, err
	}
	if originalExisted {
		cleanup(previous)
	}
	return Observation{Action: change.Action, Database: change.Database, Restored: true, Verified: true}, nil
}
