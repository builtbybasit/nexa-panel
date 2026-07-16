package mysql

import (
	"context"
	"fmt"
	"os"

	"errors"
	"path/filepath"
)

func (o *HostOperator) createBackup(ctx context.Context, engine *Engine, change Change) (Observation, error) {
	if err := os.MkdirAll(filepath.Dir(change.BackupPath), 0o750); err != nil {
		return Observation{}, fmt.Errorf("create MySQL-family backup directory: %w", err)
	}
	temporary := change.BackupPath + ".partial"
	_ = os.Remove(temporary)
	command := o.dumpCommand(engine, change.Database)
	command.StdoutPath = temporary
	if output, err := o.runner.Run(ctx, command); err != nil {
		_ = os.Remove(temporary)
		return Observation{}, commandError("create MySQL-family logical backup", output, err)
	}
	digest, size, err := fileDigest(temporary)
	if err != nil || size == 0 {
		_ = os.Remove(temporary)
		return Observation{}, errors.New("MySQL-family logical backup is empty or unreadable")
	}
	if err := os.Rename(temporary, change.BackupPath); err != nil {
		_ = os.Remove(temporary)
		return Observation{}, fmt.Errorf("activate MySQL-family backup: %w", err)
	}
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
	_ = os.Remove(rollback)
	originalExisted := o.databaseExists(ctx, engine, change.Database)
	if originalExisted {
		dump := o.dumpCommand(engine, change.Database)
		dump.StdoutPath = rollback
		if output, err := o.runner.Run(ctx, dump); err != nil {
			return Observation{}, commandError("create MySQL-family restore rollback point", output, err)
		}
		defer os.Remove(rollback)
	}
	reset := "DROP DATABASE IF EXISTS " + quoteIdentifier(change.Database) + ";\nCREATE DATABASE " + quoteIdentifier(change.Database) + " CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;\n"
	if output, err := o.runner.Run(ctx, o.stdinCommand(engine, reset)); err != nil {
		return Observation{}, commandError("prepare MySQL-family restore", output, err)
	}
	importCommand := o.baseClientArgs(engine)
	importCommand.Args = append(importCommand.Args, change.Database)
	importCommand.StdinPath = change.BackupPath
	if output, err := o.runner.Run(ctx, importCommand); err != nil {
		_, _ = o.runner.Run(context.WithoutCancel(ctx), o.stdinCommand(engine, reset))
		if originalExisted {
			rollbackCommand := o.baseClientArgs(engine)
			rollbackCommand.Args = append(rollbackCommand.Args, change.Database)
			rollbackCommand.StdinPath = rollback
			_, _ = o.runner.Run(context.WithoutCancel(ctx), rollbackCommand)
		}
		return Observation{}, commandError("restore MySQL-family backup", output, err)
	}
	if err := o.verifyDatabase(ctx, engine, change.Database); err != nil {
		return Observation{}, err
	}
	return Observation{Action: change.Action, Engine: engine, Database: change.Database, Restored: true, Verified: true}, nil
}
