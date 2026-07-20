package postgres

import (
	"context"
	"errors"
	"strconv"
	"strings"
)

// dropRoleDatabase drops a managed database and everything in it. It is named
// apart from the recovery-path dropDatabase helper in backups.go, which it
// reuses for the actual removal and absence check.
func (o *HostOperator) dropRoleDatabase(ctx context.Context, change Change) (Observation, error) {
	if err := o.dropDatabase(ctx, change, change.Database); err != nil {
		return Observation{}, err
	}
	return Observation{Action: change.Action, Database: change.Database, Verified: true}, nil
}

// dropRole removes a login role. Postgres refuses DROP ROLE while the role owns
// or is granted anything, so this first hands each owned database to its new
// owner, then — per database the role touches — reassigns the role's in-schema
// objects and revokes its remaining privileges, before dropping the empty role.
func (o *HostOperator) dropRole(ctx context.Context, change Change) (Observation, error) {
	role := quoteIdentifier(change.Role)

	// 1. Move database ownership off the role first. Doing this before REASSIGN
	//    OWNED matters: REASSIGN sweeps every database the role owns onto a single
	//    target, so per-database owners must be set as ALTER DATABASE while the
	//    role still owns them, leaving no shared objects for REASSIGN to touch.
	transfer := []string{}
	for _, item := range change.RoleDatabases {
		if item.NewOwner != "" {
			transfer = append(transfer, "ALTER DATABASE "+quoteIdentifier(item.Name)+" OWNER TO "+quoteIdentifier(item.NewOwner)+";")
		}
	}
	if len(transfer) > 0 {
		if output, err := o.runner.Run(ctx, o.adminPsql(change, "postgres", transfer)); err != nil {
			return Observation{}, commandError("transfer PostgreSQL database ownership", output, err)
		}
	}

	// 2. In each involved database, reassign the role's in-schema objects to the
	//    inheriting owner and revoke every privilege it still holds there.
	for _, item := range change.RoleDatabases {
		statements := []string{}
		if item.NewOwner != "" {
			statements = append(statements, "REASSIGN OWNED BY "+role+" TO "+quoteIdentifier(item.NewOwner)+";")
		}
		statements = append(statements, "DROP OWNED BY "+role+";")
		if output, err := o.runner.Run(ctx, o.adminPsql(change, item.Name, statements)); err != nil {
			return Observation{}, commandError("clear PostgreSQL role ownership in "+item.Name, output, err)
		}
	}

	// 3. Sweep privileges on shared objects and anything in the maintenance
	//    database. DROP OWNED never removes databases, so this cannot delete data.
	if output, err := o.runner.Run(ctx, o.adminPsql(change, "postgres", []string{"DROP OWNED BY " + role + ";"})); err != nil {
		return Observation{}, commandError("clear PostgreSQL role privileges", output, err)
	}

	// 4. Drop the now-empty role.
	if output, err := o.runner.Run(ctx, o.adminPsql(change, "postgres", []string{"DROP ROLE IF EXISTS " + role + ";"})); err != nil {
		return Observation{}, commandError("drop PostgreSQL role", output, err)
	}
	if err := o.verifyRoleAbsent(ctx, change, change.Role); err != nil {
		return Observation{}, err
	}
	return Observation{Action: change.Action, Role: change.Role, Verified: true}, nil
}

// revokeGrant strips a role's access to a database without removing the role,
// mirroring applyGrant's REVOKE prologue but stopping there.
func (o *HostOperator) revokeGrant(ctx context.Context, change Change) (Observation, error) {
	database, role, owner := quoteIdentifier(change.Database), quoteIdentifier(change.Role), quoteIdentifier(change.OwnerRole)
	statements := []string{
		"REVOKE ALL ON DATABASE " + database + " FROM " + role + ";",
		"\\connect " + database,
		"REVOKE ALL ON SCHEMA public FROM " + role + ";",
		"REVOKE ALL PRIVILEGES ON ALL TABLES IN SCHEMA public FROM " + role + ";",
		"REVOKE ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA public FROM " + role + ";",
		"ALTER DEFAULT PRIVILEGES FOR ROLE " + owner + " IN SCHEMA public REVOKE ALL ON TABLES FROM " + role + ";",
		"ALTER DEFAULT PRIVILEGES FOR ROLE " + owner + " IN SCHEMA public REVOKE ALL ON SEQUENCES FROM " + role + ";",
	}
	// \connect forbids --single-transaction, matching applyGrant.
	command := asPostgres(change.Version, "psql", "--no-psqlrc", "--host", o.socketRoot, "--port", strconv.Itoa(change.Port), "--username", "postgres", "--dbname", "postgres", "--set", "ON_ERROR_STOP=1")
	command.Stdin = strings.Join(statements, "\n") + "\n"
	if output, err := o.runner.Run(ctx, command); err != nil {
		return Observation{}, commandError("revoke PostgreSQL grant", output, err)
	}
	return Observation{Action: change.Action, Database: change.Database, Role: change.Role, Verified: true}, nil
}

// adminPsql builds a superuser psql invocation against database, feeding the
// statements over stdin. ON_ERROR_STOP makes any failing statement fail the run.
func (o *HostOperator) adminPsql(change Change, database string, statements []string) Command {
	command := asPostgres(change.Version, "psql", "--no-psqlrc", "--host", o.socketRoot, "--port", strconv.Itoa(change.Port), "--username", "postgres", "--dbname", database, "--set", "ON_ERROR_STOP=1")
	command.Stdin = strings.Join(statements, "\n") + "\n"
	return command
}

func (o *HostOperator) verifyRoleAbsent(ctx context.Context, change Change, role string) error {
	command := asPostgres(change.Version, "psql", "--no-psqlrc", "--tuples-only", "--no-align", "--host", o.socketRoot, "--port", strconv.Itoa(change.Port), "--username", "postgres", "--dbname", "postgres", "--command", "SELECT 1 FROM pg_roles WHERE rolname = '"+role+"';")
	output, err := o.runner.Run(ctx, command)
	if err != nil {
		return commandError("verify PostgreSQL role removal", output, err)
	}
	if strings.TrimSpace(string(output)) != "" {
		return errors.New("PostgreSQL role still exists after removal")
	}
	return nil
}
