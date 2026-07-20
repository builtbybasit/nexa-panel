package postgres

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

func (o *HostOperator) repairFailedSwapBounded(ctx context.Context, change Change, previous string) error {
	recoveryContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), postgresRestoreRecoveryTimeout)
	defer cancel()
	return o.repairFailedSwap(recoveryContext, change, previous)
}

func (o *HostOperator) repairFailedSwap(ctx context.Context, change Change, previous string) error {
	originalExists, err := o.databaseExists(ctx, change, change.Database)
	if err != nil {
		return err
	}
	previousExists, err := o.databaseExists(ctx, change, previous)
	if err != nil {
		return err
	}
	if originalExists || !previousExists {
		if originalExists {
			return nil
		}
		return fmt.Errorf("neither active database %s nor recovery database %s exists after failed swap", change.Database, previous)
	}
	command := asPostgres(change.Version, "psql", "--no-psqlrc", "--host", o.socketRoot, "--port", strconv.Itoa(change.Port), "--username", "postgres", "--dbname", "postgres", "--set", "ON_ERROR_STOP=1")
	command.Stdin = terminateSQL(previous) + "ALTER DATABASE " + quoteIdentifier(previous) + " RENAME TO " + quoteIdentifier(change.Database) + ";\n"
	if output, err := o.runner.Run(ctx, command); err != nil {
		return commandError("repair failed PostgreSQL database swap", output, err)
	}
	if err := o.verifyDatabase(ctx, change, change.Database); err != nil {
		return fmt.Errorf("verify repaired PostgreSQL database swap: %w", err)
	}
	return nil
}

func (o *HostOperator) databaseExists(ctx context.Context, change Change, database string) (bool, error) {
	command := asPostgres(change.Version, "psql", "--no-psqlrc", "--tuples-only", "--no-align", "--host", o.socketRoot, "--port", strconv.Itoa(change.Port), "--username", "postgres", "--dbname", "postgres", "--command", "SELECT 1 FROM pg_database WHERE datname = "+quoteLiteral(database)+";")
	output, err := o.runner.Run(ctx, command)
	if err != nil {
		return false, commandError("inspect PostgreSQL database "+database, output, err)
	}
	switch strings.TrimSpace(string(output)) {
	case "":
		return false, nil
	case "1":
		return true, nil
	default:
		return false, fmt.Errorf("inspect PostgreSQL database %s: unexpected query result", database)
	}
}

func (o *HostOperator) verifyRole(ctx context.Context, change Change, role string) error {
	command := asPostgres(change.Version, "psql", "--no-psqlrc", "--tuples-only", "--no-align", "--host", o.socketRoot, "--port", strconv.Itoa(change.Port), "--username", "postgres", "--dbname", "postgres", "--command", "SELECT 1 FROM pg_roles WHERE rolname = '"+role+"';")
	output, err := o.runner.Run(ctx, command)
	if err != nil || strings.TrimSpace(string(output)) != "1" {
		return commandError("verify PostgreSQL role", output, firstError(err, errors.New("role was not observed")))
	}
	return nil
}

func (o *HostOperator) verifyDatabase(ctx context.Context, change Change, database string) error {
	command := asPostgres(change.Version, "psql", "--no-psqlrc", "--tuples-only", "--no-align", "--host", o.socketRoot, "--port", strconv.Itoa(change.Port), "--username", "postgres", "--dbname", database, "--command", "SELECT 1;")
	output, err := o.runner.Run(ctx, command)
	if err != nil || strings.TrimSpace(string(output)) != "1" {
		return commandError("verify PostgreSQL database", output, firstError(err, errors.New("database was not reachable")))
	}
	return nil
}
