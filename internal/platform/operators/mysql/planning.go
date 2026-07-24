package mysql

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/platform/hostcmd"
)

func (o *HostOperator) Plan(ctx context.Context, change Change) (Plan, error) {
	engine, err := o.Discover(ctx)
	if err != nil {
		return Plan{}, err
	}
	change, err = o.normalizeChange(ctx, change, engine)
	if err != nil {
		return Plan{}, err
	}
	fingerprint, err := fingerprint(engine)
	if err != nil {
		return Plan{}, err
	}
	steps, warnings, interruption := planDescription(change)
	now := o.now().UTC()
	return Plan{ID: randomID(), Kind: PlanKind, Change: change, Steps: steps, Warnings: warnings, ObservedFingerprint: fingerprint, Interruption: interruption, PlannedAt: now, ExpiresAt: now.Add(20 * time.Minute)}, nil
}

func (o *HostOperator) normalizeChange(ctx context.Context, change Change, engine *Engine) (Change, error) {
	if engine == nil || engine.Status != "online" {
		return Change{}, errors.New("one active native MySQL or MariaDB engine is required")
	}
	change.EngineID = strings.TrimSpace(change.EngineID)
	if change.EngineID == "" {
		change.EngineID = engine.ID
	}
	if change.EngineID != engine.ID {
		return Change{}, errors.New("requested MySQL-family engine does not match the active native engine")
	}
	change.Database = strings.ToLower(strings.TrimSpace(change.Database))
	change.Account = strings.ToLower(strings.TrimSpace(change.Account))
	change.AccountHost = strings.TrimSpace(change.AccountHost)
	if change.AccountHost == "" {
		change.AccountHost = "localhost"
	}
	change.BackupID = strings.TrimSpace(change.BackupID)
	change.BackupPath = filepath.Clean(strings.TrimSpace(change.BackupPath))
	change.BackupSHA256 = strings.ToLower(strings.TrimSpace(change.BackupSHA256))
	change.SecretSHA256 = strings.ToLower(strings.TrimSpace(change.SecretSHA256))
	if err := validateChange(change, o.backupRoot); err != nil {
		return Change{}, err
	}
	if change.Action == ActionCreateAccount || change.Action == ActionRotateAccount {
		output, err := o.runner.Run(ctx, o.clientCommand(engine, "SELECT @@GLOBAL.general_log;"))
		if err != nil {
			return Change{}, hostcmd.Error("check MySQL-family general query log", output, err)
		}
		if strings.TrimSpace(string(output)) != "0" {
			return Change{}, errors.New("credential changes require the MySQL-family general query log to be disabled")
		}
	}
	return change, nil
}

func validateChange(change Change, root string) error {
	switch change.Action {
	case ActionCreateDatabase:
		if !namePattern.MatchString(change.Database) || !namePattern.MatchString(change.Account) || !hostPattern.MatchString(change.AccountHost) {
			return errors.New("a valid database and primary account are required")
		}
	case ActionDropDatabase:
		if !namePattern.MatchString(change.Database) {
			return errors.New("a valid database is required")
		}
	case ActionDropAccount:
		if !namePattern.MatchString(change.Account) || !hostPattern.MatchString(change.AccountHost) {
			return errors.New("account and host are required")
		}
	case ActionRevokeGrant:
		if !namePattern.MatchString(change.Database) || !namePattern.MatchString(change.Account) || !hostPattern.MatchString(change.AccountHost) {
			return errors.New("database, account, and host are required")
		}
	case ActionCreateAccount, ActionRotateAccount:
		if !namePattern.MatchString(change.Account) || !hostPattern.MatchString(change.AccountHost) || !hashPattern.MatchString(change.SecretSHA256) {
			return errors.New("account, host, and credential digest are required")
		}
	case ActionApplyGrant:
		if !namePattern.MatchString(change.Database) || !namePattern.MatchString(change.Account) || !hostPattern.MatchString(change.AccountHost) {
			return errors.New("database, account, and host are required")
		}
		if change.Access != AccessConnect && change.Access != AccessReadOnly && change.Access != AccessReadWrite {
			return errors.New("database access must be connect, read_only, or read_write")
		}
	case ActionCreateBackup:
		if !namePattern.MatchString(change.Database) || change.BackupID == "" {
			return errors.New("database and backup identity are required")
		}
		expected := filepath.Join(root, change.EngineID, change.Database, change.BackupID+".sql")
		if change.BackupPath == "." {
			change.BackupPath = expected
		}
		if change.BackupPath != expected {
			return errors.New("backup path is outside the managed MySQL-family backup root")
		}
	case ActionRestoreBackup:
		if !namePattern.MatchString(change.Database) || change.BackupID == "" || !hashPattern.MatchString(change.BackupSHA256) || !tokenPattern.MatchString(change.RestoreToken) {
			return errors.New("database, verified backup, and restore confirmation are required")
		}
		expected := filepath.Join(root, change.EngineID, change.Database, change.BackupID+".sql")
		if change.BackupPath != expected {
			return errors.New("restore path is outside the managed MySQL-family backup root")
		}
	default:
		return errors.New("MySQL-family action is unsupported")
	}
	return nil
}

func planDescription(change Change) ([]string, []string, bool) {
	switch change.Action {
	case ActionCreateDatabase:
		return []string{"Create the UTF8MB4 database.", "Verify local connectivity."}, nil, false
	case ActionDropDatabase:
		return []string{"Drop the database and all of its tables.", "Verify the schema no longer exists."}, []string{"This permanently deletes the database and everything in it. Back it up first."}, true
	case ActionDropAccount:
		return []string{"Drop the login account and its privilege grants.", "Verify the account no longer exists."}, []string{"Applications still using this account will lose access."}, false
	case ActionRevokeGrant:
		return []string{"Revoke the account's database-scoped privileges.", "Flush privileges."}, []string{"The account will lose the access this grant provided."}, false
	case ActionCreateAccount:
		return []string{"Create a least-privilege login account.", "Verify the account without returning its password."}, nil, false
	case ActionRotateAccount:
		return []string{"Rotate the account credential through agent stdin.", "Verify the account remains present."}, []string{"Existing clients must be updated with the new credential."}, false
	case ActionApplyGrant:
		return []string{"Replace database-scoped grants with the selected access level.", "Flush and verify privileges."}, nil, false
	case ActionCreateBackup:
		return []string{"Create a consistent logical SQL backup.", "Hash the completed artifact."}, nil, false
	case ActionRestoreBackup:
		return []string{"Create an automatic rollback dump.", "Recreate and restore the selected database.", "Verify the restored database and roll back if import or verification fails."}, []string{"The database is unavailable during replacement."}, true
	default:
		return nil, nil, false
	}
}
