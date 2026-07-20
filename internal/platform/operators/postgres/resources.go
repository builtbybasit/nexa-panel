package postgres

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

func (o *HostOperator) provision(ctx context.Context, change Change) (Observation, error) {
	dataPath := filepath.Join(o.dataRoot, change.Version, change.Cluster)
	logPath := filepath.Join(o.logRoot, fmt.Sprintf("postgresql-%s-%s.log", change.Version, change.Cluster))
	args := []string{"--port", strconv.Itoa(change.Port), "--datadir", dataPath, "--socketdir", o.socketRoot, "--logfile", logPath, "--start-conf", "auto", change.Version, change.Cluster}
	if output, err := o.runner.Run(ctx, Command{Name: "pg_createcluster", Args: args}); err != nil {
		return Observation{}, commandError("create PostgreSQL instance", output, err)
	}
	if output, err := o.runner.Run(ctx, Command{Name: "pg_ctlcluster", Args: []string{change.Version, change.Cluster, "start"}}); err != nil && !strings.Contains(strings.ToLower(string(output)), "already running") {
		return Observation{}, commandError("start PostgreSQL instance", output, err)
	}
	if output, err := o.runner.Run(ctx, Command{Name: binary(change.Version, "pg_isready"), Args: []string{"--host", o.socketRoot, "--port", strconv.Itoa(change.Port), "--dbname", "postgres"}}); err != nil {
		return Observation{}, commandError("verify PostgreSQL instance", output, err)
	}
	instance := o.instance(change.Version, change.Cluster, change.Port, "online", "postgres", dataPath, logPath)
	return Observation{Action: change.Action, Instance: &instance, Verified: true}, nil
}

func (o *HostOperator) createRole(ctx context.Context, change Change, secret string) (Observation, error) {
	command := asPostgres(change.Version, "psql", "--no-psqlrc", "--host", o.socketRoot, "--port", strconv.Itoa(change.Port), "--username", "postgres", "--dbname", "postgres", "--set", "ON_ERROR_STOP=1", "--single-transaction", "--file", "-")
	command.Stdin = "CREATE ROLE " + quoteIdentifier(change.Role) + " LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;\n\\password " + quoteIdentifier(change.Role) + "\n" + secret + "\n" + secret + "\n"
	if output, err := o.runner.Run(ctx, command); err != nil {
		return Observation{}, redactCommandError("create PostgreSQL role", output, err, secret)
	}
	if err := o.verifyRole(ctx, change, change.Role); err != nil {
		return Observation{}, err
	}
	return Observation{Action: change.Action, Role: change.Role, Verified: true}, nil
}

func (o *HostOperator) rotateRole(ctx context.Context, change Change, secret string) (Observation, error) {
	command := asPostgres(change.Version, "psql", "--no-psqlrc", "--host", o.socketRoot, "--port", strconv.Itoa(change.Port), "--username", "postgres", "--dbname", "postgres", "--set", "ON_ERROR_STOP=1", "--single-transaction", "--file", "-")
	command.Stdin = "\\password " + quoteIdentifier(change.Role) + "\n" + secret + "\n" + secret + "\n"
	if output, err := o.runner.Run(ctx, command); err != nil {
		return Observation{}, redactCommandError("rotate PostgreSQL role", output, err, secret)
	}
	if err := o.verifyRole(ctx, change, change.Role); err != nil {
		return Observation{}, err
	}
	return Observation{Action: change.Action, Role: change.Role, Verified: true}, nil
}

func (o *HostOperator) createDatabase(ctx context.Context, change Change) (Observation, error) {
	command := asPostgres(change.Version, "createdb", "--host", o.socketRoot, "--port", strconv.Itoa(change.Port), "--username", "postgres", "--owner", change.OwnerRole, "--encoding", "UTF8", "--template", "template0", change.Database)
	if output, err := o.runner.Run(ctx, command); err != nil {
		return Observation{}, commandError("create PostgreSQL database", output, err)
	}
	if err := o.verifyDatabase(ctx, change, change.Database); err != nil {
		return Observation{}, err
	}
	return Observation{Action: change.Action, Database: change.Database, Role: change.OwnerRole, Verified: true}, nil
}
