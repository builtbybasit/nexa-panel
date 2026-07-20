package postgres

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const postgresRestoreRecoveryTimeout = 30 * time.Minute

func (o *HostOperator) createBackup(ctx context.Context, change Change) (Observation, error) {
	if err := preparePostgresDirectory(o.backupRoot, filepath.Dir(change.BackupPath)); err != nil {
		return Observation{}, err
	}
	temporary := change.BackupPath + ".partial-" + randomID()
	defer os.Remove(temporary)
	command := asPostgres(change.Version, "pg_dump", "--host", o.socketRoot, "--port", strconv.Itoa(change.Port), "--username", "postgres", "--format", "custom", "--no-owner", "--no-privileges", "--file", temporary, change.Database)
	if output, err := o.runner.Run(ctx, command); err != nil {
		return Observation{}, commandError("create PostgreSQL logical backup", output, err)
	}
	if output, err := o.runner.Run(ctx, asPostgres(change.Version, "pg_restore", "--list", temporary)); err != nil {
		return Observation{}, commandError("verify PostgreSQL logical backup", output, err)
	}
	digest, size, err := fileDigest(temporary)
	if err != nil || size == 0 {
		return Observation{}, fmt.Errorf("verify PostgreSQL backup artifact: %w", firstError(err, errors.New("backup is empty")))
	}
	if err := os.Chmod(temporary, 0o640); err != nil {
		return Observation{}, fmt.Errorf("secure PostgreSQL backup: %w", err)
	}
	if err := syncFile(temporary); err != nil {
		return Observation{}, fmt.Errorf("persist PostgreSQL backup: %w", err)
	}
	// A hard link publishes atomically while refusing to replace an existing
	// verified restore point with the same immutable backup identity.
	if err := os.Link(temporary, change.BackupPath); err != nil {
		return Observation{}, fmt.Errorf("publish PostgreSQL backup without replacing an existing restore point: %w", err)
	}
	if err := syncDirectory(filepath.Dir(change.BackupPath)); err != nil {
		_ = os.Remove(change.BackupPath)
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
	if err := o.dropDatabase(ctx, change, temporary); err != nil {
		return Observation{}, fmt.Errorf("remove stale PostgreSQL restore staging database: %w", err)
	}
	previousExists, err := o.databaseExists(ctx, change, previous)
	if err != nil {
		return Observation{}, err
	}
	if previousExists {
		return Observation{}, fmt.Errorf("retained PostgreSQL recovery database %s already exists; recover or remove it before retrying", previous)
	}
	originalExisted, err := o.databaseExists(ctx, change, change.Database)
	if err != nil {
		return Observation{}, err
	}
	create := asPostgres(change.Version, "createdb", "--host", o.socketRoot, "--port", strconv.Itoa(change.Port), "--username", "postgres", "--owner", change.OwnerRole, "--encoding", "UTF8", "--template", "template0", temporary)
	if output, err := o.runner.Run(ctx, create); err != nil {
		return Observation{}, commandError("create PostgreSQL restore staging database", output, err)
	}
	restore := asPostgres(change.Version, "pg_restore", "--host", o.socketRoot, "--port", strconv.Itoa(change.Port), "--username", "postgres", "--dbname", temporary, "--exit-on-error", "--no-owner", "--no-privileges", "--role", change.OwnerRole, change.BackupPath)
	if output, err := o.runner.Run(ctx, restore); err != nil {
		cause := commandError("restore PostgreSQL archive", output, err)
		return Observation{}, errors.Join(cause, o.dropDatabaseForRecovery(ctx, change, temporary))
	}
	if err := o.verifyDatabase(ctx, change, temporary); err != nil {
		return Observation{}, errors.Join(err, o.dropDatabaseForRecovery(ctx, change, temporary))
	}
	admin := asPostgres(change.Version, "psql", "--no-psqlrc", "--host", o.socketRoot, "--port", strconv.Itoa(change.Port), "--username", "postgres", "--dbname", "postgres", "--set", "ON_ERROR_STOP=1")
	if originalExisted {
		admin.Stdin = terminateSQL(change.Database) + terminateSQL(temporary) + "ALTER DATABASE " + quoteIdentifier(change.Database) + " RENAME TO " + quoteIdentifier(previous) + ";\nALTER DATABASE " + quoteIdentifier(temporary) + " RENAME TO " + quoteIdentifier(change.Database) + ";\n"
	} else {
		admin.Stdin = terminateSQL(temporary) + "ALTER DATABASE " + quoteIdentifier(temporary) + " RENAME TO " + quoteIdentifier(change.Database) + ";\n"
	}
	if output, err := o.runner.Run(ctx, admin); err != nil {
		cause := commandError("activate restored PostgreSQL database", output, err)
		if originalExisted {
			cause = errors.Join(cause, o.repairFailedSwapBounded(ctx, change, previous))
		}
		return Observation{}, errors.Join(cause, o.dropDatabaseForRecovery(ctx, change, temporary))
	}
	if err := o.verifyDatabase(ctx, change, change.Database); err != nil {
		recoveryDatabase := previous
		if !originalExisted {
			recoveryDatabase = change.Database
		}
		return Observation{}, postgresRestoreFailure(err, o.rollbackRestoreBounded(ctx, change, admin, temporary, previous, originalExisted), recoveryDatabase)
	}
	if originalExisted {
		if err := o.dropDatabaseForRecovery(ctx, change, previous); err != nil {
			return Observation{}, fmt.Errorf("restored PostgreSQL database is active and verified, but replaced database %s could not be removed: %w", previous, err)
		}
	}
	return Observation{Action: change.Action, Database: change.Database, Restored: true, Verified: true}, nil
}

func (o *HostOperator) rollbackRestoreBounded(ctx context.Context, change Change, admin Command, temporary, previous string, originalExisted bool) error {
	recoveryContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), postgresRestoreRecoveryTimeout)
	defer cancel()

	if !originalExisted {
		return o.dropDatabase(recoveryContext, change, change.Database)
	}
	admin.Stdin = terminateSQL(change.Database) + terminateSQL(previous) +
		"ALTER DATABASE " + quoteIdentifier(change.Database) + " RENAME TO " + quoteIdentifier(temporary) + ";\n" +
		"ALTER DATABASE " + quoteIdentifier(previous) + " RENAME TO " + quoteIdentifier(change.Database) + ";\n"
	if output, err := o.runner.Run(recoveryContext, admin); err != nil {
		return commandError("roll back PostgreSQL restore database swap", output, err)
	}
	if err := o.verifyDatabase(recoveryContext, change, change.Database); err != nil {
		return fmt.Errorf("verify PostgreSQL restore rollback: %w", err)
	}
	if err := o.dropDatabase(recoveryContext, change, temporary); err != nil {
		return fmt.Errorf("remove failed PostgreSQL restore after rollback: %w", err)
	}
	return nil
}

func (o *HostOperator) dropDatabaseForRecovery(ctx context.Context, change Change, database string) error {
	recoveryContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), postgresRestoreRecoveryTimeout)
	defer cancel()
	return o.dropDatabase(recoveryContext, change, database)
}

func (o *HostOperator) dropDatabase(ctx context.Context, change Change, database string) error {
	command := asPostgres(change.Version, "dropdb", "--if-exists", "--force", "--host", o.socketRoot, "--port", strconv.Itoa(change.Port), "--username", "postgres", database)
	if output, err := o.runner.Run(ctx, command); err != nil {
		return commandError("drop PostgreSQL database "+database, output, err)
	}
	exists, err := o.databaseExists(ctx, change, database)
	if err != nil {
		return err
	}
	if exists {
		return fmt.Errorf("PostgreSQL database %s still exists after removal", database)
	}
	return nil
}

func postgresRestoreFailure(cause, rollbackErr error, recoveryDatabase string) error {
	if rollbackErr == nil {
		return cause
	}
	return errors.Join(cause, fmt.Errorf("automatic PostgreSQL rollback failed; recovery database %s was retained: %w", recoveryDatabase, rollbackErr))
}
