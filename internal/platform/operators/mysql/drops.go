package mysql

import (
	"context"
	"errors"
	"strings"

	"github.com/nexa-panel/nexa-panel/internal/platform/hostcmd"
)

// dropDatabase removes a managed schema and every table in it. The module has
// already confirmed the desired state; this is the irreversible half.
func (o *HostOperator) dropDatabase(ctx context.Context, engine *Engine, change Change) (Observation, error) {
	sql := "DROP DATABASE IF EXISTS " + quoteIdentifier(change.Database) + ";\n"
	if output, err := o.runner.Run(ctx, o.stdinCommand(engine, sql)); err != nil {
		return Observation{}, hostcmd.Error("drop MySQL-family database", output, err)
	}
	if err := o.verifyDatabaseAbsent(ctx, engine, change.Database); err != nil {
		return Observation{}, err
	}
	return Observation{Action: change.Action, Engine: engine, Database: change.Database, Verified: true}, nil
}

// dropAccount removes a login account. MySQL discards the account's privilege
// grants with it, so no separate REVOKE is needed.
func (o *HostOperator) dropAccount(ctx context.Context, engine *Engine, change Change) (Observation, error) {
	sql := "DROP USER IF EXISTS " + quoteAccount(change.Account, change.AccountHost) + ";\nFLUSH PRIVILEGES;\n"
	if output, err := o.runner.Run(ctx, o.stdinCommand(engine, sql)); err != nil {
		return Observation{}, hostcmd.Error("drop MySQL-family account", output, err)
	}
	if err := o.verifyAccountAbsent(ctx, engine, change); err != nil {
		return Observation{}, err
	}
	return Observation{Action: change.Action, Engine: engine, Account: change.Account + "@" + change.AccountHost, Verified: true}, nil
}

// revokeGrant strips an account's database-scoped privileges without removing
// the account, leaving it able to authenticate but touch nothing in the schema.
func (o *HostOperator) revokeGrant(ctx context.Context, engine *Engine, change Change) (Observation, error) {
	account, database := quoteAccount(change.Account, change.AccountHost), quoteIdentifier(change.Database)
	statements := []string{
		"REVOKE ALL PRIVILEGES ON " + database + ".* FROM " + account + ";",
		"FLUSH PRIVILEGES;",
	}
	if output, err := o.runner.Run(ctx, o.stdinCommand(engine, strings.Join(statements, "\n")+"\n")); err != nil {
		// An account with no remaining grant on the schema makes REVOKE error;
		// that is the desired end state, not a failure.
		if !strings.Contains(strings.ToLower(string(output)), "there is no such grant") {
			return Observation{}, hostcmd.Error("revoke MySQL-family grant", output, err)
		}
	}
	return Observation{Action: change.Action, Engine: engine, Database: change.Database, Account: change.Account + "@" + change.AccountHost, Verified: true}, nil
}

func (o *HostOperator) verifyDatabaseAbsent(ctx context.Context, engine *Engine, database string) error {
	output, err := o.runner.Run(ctx, o.clientCommand(engine, "SELECT SCHEMA_NAME FROM INFORMATION_SCHEMA.SCHEMATA WHERE SCHEMA_NAME="+quoteLiteral(database)+";"))
	if err != nil {
		return hostcmd.Error("verify MySQL-family database removal", output, err)
	}
	if strings.TrimSpace(string(output)) != "" {
		return errors.New("MySQL-family database still exists after removal")
	}
	return nil
}

func (o *HostOperator) verifyAccountAbsent(ctx context.Context, engine *Engine, change Change) error {
	query := "SELECT User FROM mysql.user WHERE User=" + quoteLiteral(change.Account) + " AND Host=" + quoteLiteral(change.AccountHost) + ";"
	output, err := o.runner.Run(ctx, o.clientCommand(engine, query))
	if err != nil {
		return hostcmd.Error("verify MySQL-family account removal", output, err)
	}
	if strings.TrimSpace(string(output)) != "" {
		return errors.New("MySQL-family account still exists after removal")
	}
	return nil
}
