package postgres

import (
	"errors"
	"strings"

	"context"
	"strconv"
)

func (o *HostOperator) repairFailedSwap(ctx context.Context, change Change, previous string) {
	originalExists := o.databaseExists(context.WithoutCancel(ctx), change, change.Database)
	previousExists := o.databaseExists(context.WithoutCancel(ctx), change, previous)
	if originalExists || !previousExists {
		return
	}
	command := asPostgres(change.Version, "psql", "--no-psqlrc", "--host", o.socketRoot, "--port", strconv.Itoa(change.Port), "--username", "postgres", "--dbname", "postgres", "--set", "ON_ERROR_STOP=1")
	command.Stdin = terminateSQL(previous) + "ALTER DATABASE " + quoteIdentifier(previous) + " RENAME TO " + quoteIdentifier(change.Database) + ";\n"
	_, _ = o.runner.Run(context.WithoutCancel(ctx), command)
}

func (o *HostOperator) databaseExists(ctx context.Context, change Change, database string) bool {
	command := asPostgres(change.Version, "psql", "--no-psqlrc", "--tuples-only", "--no-align", "--host", o.socketRoot, "--port", strconv.Itoa(change.Port), "--username", "postgres", "--dbname", "postgres", "--command", "SELECT 1 FROM pg_database WHERE datname = '"+database+"';")
	output, err := o.runner.Run(ctx, command)
	return err == nil && strings.TrimSpace(string(output)) == "1"
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
