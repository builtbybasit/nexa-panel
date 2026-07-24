package mysql

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/platform/hostcmd"
)

const restoreRollbackTimeout = 30 * time.Minute

func (o *HostOperator) createBackup(ctx context.Context, engine *Engine, change Change) (Observation, error) {
	if err := os.MkdirAll(filepath.Dir(change.BackupPath), 0o750); err != nil {
		return Observation{}, fmt.Errorf("create MySQL-family backup directory: %w", err)
	}
	temporary := change.BackupPath + ".partial-" + randomID()
	defer os.Remove(temporary)
	command := o.dumpCommand(engine, change.Database)
	command.StdoutPath = temporary
	if output, err := o.runner.Run(ctx, command); err != nil {
		_ = os.Remove(temporary)
		return Observation{}, hostcmd.Error("create MySQL-family logical backup", output, err)
	}
	digest, size, err := fileDigest(temporary)
	if err != nil || size == 0 {
		_ = os.Remove(temporary)
		return Observation{}, errors.New("MySQL-family logical backup is empty or unreadable")
	}
	if err := os.Chmod(temporary, 0o640); err != nil {
		return Observation{}, fmt.Errorf("secure MySQL-family backup: %w", err)
	}
	if err := syncFile(temporary); err != nil {
		return Observation{}, fmt.Errorf("persist MySQL-family backup: %w", err)
	}
	// Hard-link publication is atomic and refuses to replace an existing backup
	// identity. os.Rename would silently overwrite a prior verified restore point.
	if err := os.Link(temporary, change.BackupPath); err != nil {
		return Observation{}, fmt.Errorf("publish MySQL-family backup without replacing an existing restore point: %w", err)
	}
	if err := syncDirectory(filepath.Dir(change.BackupPath)); err != nil {
		_ = os.Remove(change.BackupPath)
		return Observation{}, err
	}
	_ = os.Remove(temporary)
	backup := &Backup{ID: change.BackupID, Path: change.BackupPath, SHA256: digest, SizeBytes: size, CreatedAt: o.now().UTC(), Verified: true}
	return Observation{Action: change.Action, Engine: engine, Database: change.Database, Backup: backup, Verified: true}, nil
}

func (o *HostOperator) restoreBackup(ctx context.Context, engine *Engine, change Change) (Observation, error) {
	digest, _, err := fileDigest(change.BackupPath)
	if err != nil {
		return Observation{}, err
	}
	if digest != change.BackupSHA256 {
		return Observation{}, errors.New("MySQL-family restore point checksum does not match")
	}
	rollback := filepath.Join(filepath.Dir(change.BackupPath), ".rollback-"+change.RestoreToken+".sql")
	originalExisted, err := o.databaseExists(ctx, engine, change.Database)
	if err != nil {
		return Observation{}, err
	}
	removeRollback := false
	if originalExisted {
		if _, err := os.Lstat(rollback); err == nil {
			return Observation{}, fmt.Errorf("a retained MySQL-family rollback point already exists at %s; recover or remove it before retrying", rollback)
		} else if !errors.Is(err, os.ErrNotExist) {
			return Observation{}, fmt.Errorf("inspect MySQL-family rollback point: %w", err)
		}
		dump := o.dumpCommand(engine, change.Database)
		dump.StdoutPath = rollback
		if output, err := o.runner.Run(ctx, dump); err != nil {
			return Observation{}, hostcmd.Error("create MySQL-family restore rollback point", output, err)
		}
		if _, size, digestErr := fileDigest(rollback); digestErr != nil || size == 0 {
			_ = os.Remove(rollback)
			return Observation{}, fmt.Errorf("verify MySQL-family restore rollback point: %w", firstError(digestErr, errors.New("rollback point is empty")))
		}
		if err := os.Chmod(rollback, 0o600); err != nil {
			_ = os.Remove(rollback)
			return Observation{}, fmt.Errorf("secure MySQL-family restore rollback point: %w", err)
		}
		if err := syncFile(rollback); err != nil {
			_ = os.Remove(rollback)
			return Observation{}, fmt.Errorf("persist MySQL-family restore rollback point: %w", err)
		}
		if err := syncDirectory(filepath.Dir(rollback)); err != nil {
			_ = os.Remove(rollback)
			return Observation{}, err
		}
		defer func() {
			if removeRollback {
				_ = os.Remove(rollback)
			}
		}()
	}
	reset := "DROP DATABASE IF EXISTS " + quoteIdentifier(change.Database) + ";\nCREATE DATABASE " + quoteIdentifier(change.Database) + " CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;\n"
	if output, err := o.runner.Run(ctx, o.stdinCommand(engine, reset)); err != nil {
		cause := hostcmd.Error("prepare MySQL-family restore", output, err)
		rollbackErr := o.rollbackRestoreBounded(ctx, engine, change.Database, reset, rollback, originalExisted)
		removeRollback = rollbackErr == nil
		return Observation{}, restoreFailure(cause, rollbackErr, rollback)
	}
	importCommand := o.baseClientArgs(engine)
	importCommand.Args = append(importCommand.Args, change.Database)
	importCommand.StdinPath = change.BackupPath
	if output, err := o.runner.Run(ctx, importCommand); err != nil {
		cause := hostcmd.Error("restore MySQL-family backup", output, err)
		rollbackErr := o.rollbackRestoreBounded(ctx, engine, change.Database, reset, rollback, originalExisted)
		removeRollback = rollbackErr == nil
		return Observation{}, restoreFailure(cause, rollbackErr, rollback)
	}
	if err := o.verifyDatabase(ctx, engine, change.Database); err != nil {
		rollbackErr := o.rollbackRestoreBounded(ctx, engine, change.Database, reset, rollback, originalExisted)
		removeRollback = rollbackErr == nil
		return Observation{}, restoreFailure(err, rollbackErr, rollback)
	}
	removeRollback = true
	return Observation{Action: change.Action, Engine: engine, Database: change.Database, Restored: true, Verified: true}, nil
}

func (o *HostOperator) rollbackRestoreBounded(ctx context.Context, engine *Engine, database, reset, rollback string, originalExisted bool) error {
	rollbackContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), restoreRollbackTimeout)
	defer cancel()
	return o.rollbackRestore(rollbackContext, engine, database, reset, rollback, originalExisted)
}

func (o *HostOperator) rollbackRestore(ctx context.Context, engine *Engine, database, reset, rollback string, originalExisted bool) error {
	if !originalExisted {
		statement := "DROP DATABASE IF EXISTS " + quoteIdentifier(database) + ";\n"
		if output, err := o.runner.Run(ctx, o.stdinCommand(engine, statement)); err != nil {
			return hostcmd.Error("remove failed MySQL-family restore", output, err)
		}
		output, err := o.runner.Run(ctx, o.clientCommand(engine, "SELECT SCHEMA_NAME FROM INFORMATION_SCHEMA.SCHEMATA WHERE SCHEMA_NAME="+quoteLiteral(database)+";"))
		if err != nil || strings.TrimSpace(string(output)) != "" {
			return hostcmd.Error("verify removal of failed MySQL-family restore", output, firstError(err, errors.New("database still exists")))
		}
		return nil
	}
	if output, err := o.runner.Run(ctx, o.stdinCommand(engine, reset)); err != nil {
		return hostcmd.Error("prepare MySQL-family restore rollback", output, err)
	}
	rollbackCommand := o.baseClientArgs(engine)
	rollbackCommand.Args = append(rollbackCommand.Args, database)
	rollbackCommand.StdinPath = rollback
	if output, err := o.runner.Run(ctx, rollbackCommand); err != nil {
		return hostcmd.Error("replay MySQL-family restore rollback point", output, err)
	}
	if err := o.verifyDatabase(ctx, engine, database); err != nil {
		return fmt.Errorf("verify MySQL-family restore rollback: %w", err)
	}
	return nil
}

func restoreFailure(cause, rollbackErr error, rollbackPath string) error {
	if rollbackErr == nil {
		return cause
	}
	return errors.Join(cause, fmt.Errorf("automatic rollback failed; rollback point retained at %s: %w", rollbackPath, rollbackErr))
}
