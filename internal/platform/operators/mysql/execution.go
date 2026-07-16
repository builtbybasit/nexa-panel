package mysql

import (
	"context"
	"errors"

	"crypto/sha256"
	"encoding/hex"
	"strings"
)

func (o *HostOperator) Apply(ctx context.Context, execution Execution) (Observation, error) {
	plan := execution.Plan
	if plan.ID == "" || plan.Kind != PlanKind || o.now().UTC().After(plan.ExpiresAt) {
		return Observation{}, errors.New("MySQL-family plan is invalid or expired")
	}
	if plan.Change.SecretSHA256 != "" {
		digest := sha256.Sum256([]byte(execution.Secret))
		if !strings.EqualFold(hex.EncodeToString(digest[:]), plan.Change.SecretSHA256) {
			return Observation{}, errors.New("credential does not match the approved MySQL-family plan")
		}
	} else if execution.Secret != "" {
		return Observation{}, errors.New("unexpected credential for MySQL-family operation")
	}
	engine, err := o.Discover(ctx)
	if err != nil {
		return Observation{}, err
	}
	change, err := o.normalizeChange(ctx, plan.Change, engine)
	if err != nil {
		return Observation{}, err
	}
	current, _ := fingerprint(engine)
	if current != plan.ObservedFingerprint {
		return Observation{}, errors.New("MySQL-family observed state changed after planning")
	}
	return o.execute(ctx, engine, change, execution.Secret)
}

func (o *HostOperator) execute(ctx context.Context, engine *Engine, change Change, secret string) (Observation, error) {
	switch change.Action {
	case ActionCreateDatabase:
		return o.createDatabase(ctx, engine, change)
	case ActionCreateAccount:
		return o.createAccount(ctx, engine, change, secret, false)
	case ActionRotateAccount:
		return o.createAccount(ctx, engine, change, secret, true)
	case ActionApplyGrant:
		return o.applyGrant(ctx, engine, change)
	case ActionCreateBackup:
		return o.createBackup(ctx, engine, change)
	case ActionRestoreBackup:
		return o.restoreBackup(ctx, engine, change)
	default:
		return Observation{}, errors.New("MySQL-family action is unsupported")
	}
}

func (o *HostOperator) applyGrant(ctx context.Context, engine *Engine, change Change) (Observation, error) {
	account, database := quoteAccount(change.Account, change.AccountHost), quoteIdentifier(change.Database)
	query := "SELECT PRIVILEGE_TYPE FROM INFORMATION_SCHEMA.SCHEMA_PRIVILEGES WHERE TABLE_SCHEMA=" + quoteLiteral(change.Database) + " AND GRANTEE=CONCAT(QUOTE(" + quoteLiteral(change.Account) + "),'@',QUOTE(" + quoteLiteral(change.AccountHost) + ")) ORDER BY PRIVILEGE_TYPE;"
	output, err := o.runner.Run(ctx, o.clientCommand(engine, query))
	if err != nil {
		return Observation{}, commandError("inspect existing MySQL-family grant", output, err)
	}
	privileges, err := safePrivileges(string(output))
	if err != nil {
		return Observation{}, err
	}
	statements := []string{}
	if len(privileges) > 0 {
		statements = append(statements, "REVOKE "+strings.Join(privileges, ", ")+" ON "+database+".* FROM "+account+";")
	}
	if change.Access == AccessReadOnly {
		statements = append(statements, "GRANT SELECT, SHOW VIEW ON "+database+".* TO "+account+";")
	}
	if change.Access == AccessReadWrite {
		statements = append(statements, "GRANT SELECT, INSERT, UPDATE, DELETE, CREATE, ALTER, INDEX, DROP, REFERENCES, CREATE VIEW, SHOW VIEW, TRIGGER ON "+database+".* TO "+account+";")
	}
	statements = append(statements, "FLUSH PRIVILEGES;")
	if output, err := o.runner.Run(ctx, o.stdinCommand(engine, strings.Join(statements, "\n")+"\n")); err != nil {
		return Observation{}, commandError("apply MySQL-family grant", output, err)
	}
	return Observation{Action: change.Action, Engine: engine, Database: change.Database, Account: change.Account + "@" + change.AccountHost, Access: string(change.Access), Verified: true}, nil
}
