package backups

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func rollbackRestoreActivations(ctx context.Context, activations []restoreActivation, cause error) error {
	recoveryContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), restoreRecoveryTimeout)
	defer cancel()
	failures := []error{cause}
	for index := len(activations) - 1; index >= 0; index-- {
		if err := activations[index].rollback(recoveryContext); err != nil {
			failures = append(failures, fmt.Errorf("roll back %s: %w", activations[index].description, err))
		}
	}
	return errors.Join(failures...)
}

func commitRestoreActivations(ctx context.Context, activations []restoreActivation) error {
	recoveryContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), restoreRecoveryTimeout)
	defer cancel()
	var failures []error
	for index := len(activations) - 1; index >= 0; index-- {
		if err := activations[index].commit(recoveryContext); err != nil {
			failures = append(failures, fmt.Errorf("finalize %s: %w", activations[index].description, err))
		}
	}
	return errors.Join(failures...)
}

func (h *HostOperator) preparePostgresRestore(ctx context.Context, target DatabaseTarget, dump string) (restoreActivation, error) {
	token, err := restoreRandomToken()
	if err != nil {
		return restoreActivation{}, err
	}
	temporary := postgresRecoveryName(target.Name, "restore", token)
	previous := postgresRecoveryName(target.Name, "previous", token)
	owner, err := h.postgresDatabaseOwner(ctx, target)
	if err != nil {
		return restoreActivation{}, err
	}

	create := postgresCommand(target, "createdb", "--owner", owner, "--encoding", "UTF8", "--template", "template0", temporary)
	if output, err := h.runner.Run(ctx, create.name, create.args, nil); err != nil {
		return restoreActivation{}, fmt.Errorf("create PostgreSQL restore staging database: %w: %s", err, sanitize(output))
	}
	restore := postgresCommand(target, "pg_restore", "--dbname", temporary, "--exit-on-error", "--no-owner", "--no-privileges", "--role", owner, dump)
	if output, err := h.runner.Run(ctx, restore.name, restore.args, nil); err != nil {
		cause := fmt.Errorf("restore PostgreSQL staging database: %w: %s", err, sanitize(output))
		return restoreActivation{}, errors.Join(cause, h.dropPostgresForRecovery(ctx, target, temporary))
	}
	if err := h.verifyPostgresDatabase(ctx, target, temporary); err != nil {
		return restoreActivation{}, errors.Join(err, h.dropPostgresForRecovery(ctx, target, temporary))
	}

	swap := postgresCommand(target, "psql", "--dbname", "postgres", "--set", "ON_ERROR_STOP=1")
	swap.args = append(swap.args, "--file", "-")
	swap.stdin = postgresTerminateSQL(target.Name) + postgresTerminateSQL(temporary) +
		"ALTER DATABASE " + quotePostgresIdentifier(target.Name) + " RENAME TO " + quotePostgresIdentifier(previous) + ";\n" +
		"ALTER DATABASE " + quotePostgresIdentifier(temporary) + " RENAME TO " + quotePostgresIdentifier(target.Name) + ";\n"
	if output, err := h.runPostgresScript(ctx, swap); err != nil {
		cause := fmt.Errorf("activate restored PostgreSQL database: %w: %s", err, sanitize(output))
		activated, recoveryErr := h.recoverPostgresSwap(ctx, target, temporary, previous)
		if !activated {
			return restoreActivation{}, errors.Join(cause, recoveryErr)
		}
	}

	activation := h.postgresRestoreActivation(target, temporary, previous)
	if err := h.verifyPostgresDatabase(ctx, target, target.Name); err != nil {
		recoveryContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), restoreRecoveryTimeout)
		defer cancel()
		return restoreActivation{}, errors.Join(err, activation.rollback(recoveryContext))
	}
	return activation, nil
}

type postgresInvocation struct {
	name  string
	args  []string
	stdin string
}

func postgresCommand(target DatabaseTarget, program string, args ...string) postgresInvocation {
	binary := filepath.Join("/usr/lib/postgresql", target.Version, "bin", program)
	connection := []string{"--host", target.Socket, "--port", strconv.Itoa(target.Port), "--username", "postgres"}
	return postgresInvocation{name: "runuser", args: append([]string{"-u", "postgres", "--", binary}, append(connection, args...)...)}
}

func (h *HostOperator) runPostgresScript(ctx context.Context, invocation postgresInvocation) (string, error) {
	if invocation.stdin == "" {
		return h.runner.Run(ctx, invocation.name, invocation.args, nil)
	}
	// The script contains only validated identifiers, never credentials.
	// --command avoids a temporary file that the unprivileged postgres account
	// could not safely read from the root-owned backup staging directory.
	for index := 0; index+1 < len(invocation.args); index++ {
		if invocation.args[index] == "--file" && invocation.args[index+1] == "-" {
			invocation.args[index] = "--command"
			invocation.args[index+1] = invocation.stdin
			break
		}
	}
	return h.runner.Run(ctx, invocation.name, invocation.args, nil)
}

func (h *HostOperator) postgresDatabaseOwner(ctx context.Context, target DatabaseTarget) (string, error) {
	query := "SELECT pg_get_userbyid(datdba) FROM pg_database WHERE datname = " + quotePostgresLiteral(target.Name) + ";"
	command := postgresCommand(target, "psql", "--dbname", "postgres", "--tuples-only", "--no-align", "--command", query)
	output, err := h.runner.Run(ctx, command.name, command.args, nil)
	owner := strings.TrimSpace(output)
	if err != nil {
		return "", fmt.Errorf("read PostgreSQL database owner: %w: %s", err, sanitize(output))
	}
	if !databaseNamePattern.MatchString(owner) {
		return "", errors.New("PostgreSQL database owner is missing or invalid")
	}
	return owner, nil
}

func (h *HostOperator) postgresDatabaseExists(ctx context.Context, target DatabaseTarget, database string) (bool, error) {
	query := "SELECT 1 FROM pg_database WHERE datname = " + quotePostgresLiteral(database) + ";"
	command := postgresCommand(target, "psql", "--dbname", "postgres", "--tuples-only", "--no-align", "--command", query)
	output, err := h.runner.Run(ctx, command.name, command.args, nil)
	if err != nil {
		return false, fmt.Errorf("inspect PostgreSQL recovery database: %w: %s", err, sanitize(output))
	}
	switch strings.TrimSpace(output) {
	case "":
		return false, nil
	case "1":
		return true, nil
	default:
		return false, errors.New("inspect PostgreSQL recovery database: unexpected result")
	}
}

func (h *HostOperator) verifyPostgresDatabase(ctx context.Context, target DatabaseTarget, database string) error {
	command := postgresCommand(target, "psql", "--dbname", database, "--tuples-only", "--no-align", "--command", "SELECT 1;")
	output, err := h.runner.Run(ctx, command.name, command.args, nil)
	if err != nil || strings.TrimSpace(output) != "1" {
		return fmt.Errorf("verify restored PostgreSQL database: %w: %s", firstRestoreError(err, errors.New("database did not answer verification query")), sanitize(output))
	}
	return nil
}

func (h *HostOperator) recoverPostgresSwap(ctx context.Context, target DatabaseTarget, temporary, previous string) (bool, error) {
	recoveryContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), restoreRecoveryTimeout)
	defer cancel()
	originalExists, originalErr := h.postgresDatabaseExists(recoveryContext, target, target.Name)
	previousExists, previousErr := h.postgresDatabaseExists(recoveryContext, target, previous)
	temporaryExists, temporaryErr := h.postgresDatabaseExists(recoveryContext, target, temporary)
	if err := errors.Join(originalErr, previousErr, temporaryErr); err != nil {
		return false, err
	}
	if originalExists && previousExists && !temporaryExists {
		return true, h.verifyPostgresDatabase(recoveryContext, target, target.Name)
	}
	if !originalExists && previousExists {
		repair := postgresCommand(target, "psql", "--dbname", "postgres", "--set", "ON_ERROR_STOP=1", "--file", "-")
		repair.stdin = postgresTerminateSQL(previous) + "ALTER DATABASE " + quotePostgresIdentifier(previous) + " RENAME TO " + quotePostgresIdentifier(target.Name) + ";\n"
		output, err := h.runPostgresScript(recoveryContext, repair)
		if err != nil {
			return false, fmt.Errorf("repair failed PostgreSQL restore swap: %w: %s", err, sanitize(output))
		}
		return false, errors.Join(h.verifyPostgresDatabase(recoveryContext, target, target.Name), h.dropPostgres(recoveryContext, target, temporary))
	}
	if originalExists && !previousExists {
		return false, h.dropPostgres(recoveryContext, target, temporary)
	}
	return false, fmt.Errorf("ambiguous PostgreSQL restore state; active=%t previous=%t staging=%t", originalExists, previousExists, temporaryExists)
}

func (h *HostOperator) postgresRestoreActivation(target DatabaseTarget, temporary, previous string) restoreActivation {
	return restoreActivation{
		description: "PostgreSQL database " + target.Name,
		commit: func(ctx context.Context) error {
			return h.dropPostgres(ctx, target, previous)
		},
		rollback: func(ctx context.Context) error {
			rollback := postgresCommand(target, "psql", "--dbname", "postgres", "--set", "ON_ERROR_STOP=1", "--file", "-")
			rollback.stdin = postgresTerminateSQL(target.Name) + postgresTerminateSQL(previous) +
				"ALTER DATABASE " + quotePostgresIdentifier(target.Name) + " RENAME TO " + quotePostgresIdentifier(temporary) + ";\n" +
				"ALTER DATABASE " + quotePostgresIdentifier(previous) + " RENAME TO " + quotePostgresIdentifier(target.Name) + ";\n"
			output, err := h.runPostgresScript(ctx, rollback)
			if err != nil {
				return fmt.Errorf("restore original PostgreSQL database: %w: %s", err, sanitize(output))
			}
			if err := h.verifyPostgresDatabase(ctx, target, target.Name); err != nil {
				return err
			}
			return h.dropPostgres(ctx, target, temporary)
		},
	}
}

func (h *HostOperator) dropPostgresForRecovery(ctx context.Context, target DatabaseTarget, database string) error {
	recoveryContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), restoreRecoveryTimeout)
	defer cancel()
	return h.dropPostgres(recoveryContext, target, database)
}

func (h *HostOperator) dropPostgres(ctx context.Context, target DatabaseTarget, database string) error {
	command := postgresCommand(target, "dropdb", "--if-exists", "--force", database)
	output, err := h.runner.Run(ctx, command.name, command.args, nil)
	if err != nil {
		return fmt.Errorf("drop PostgreSQL recovery database %s: %w: %s", database, err, sanitize(output))
	}
	return nil
}

func postgresRecoveryName(database, kind, token string) string {
	suffix := "_nexa_" + kind + "_" + token[:8]
	if len(database)+len(suffix) > 63 {
		database = database[:63-len(suffix)]
	}
	return database + suffix
}

func postgresTerminateSQL(database string) string {
	return "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = " + quotePostgresLiteral(database) + " AND pid <> pg_backend_pid();\n"
}

func quotePostgresIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

func quotePostgresLiteral(value string) string {
	return `'` + strings.ReplaceAll(value, `'`, `''`) + `'`
}

func (h *HostOperator) prepareMySQLRestore(ctx context.Context, staging string, database DatabaseRestoreTarget, dump string) (restoreActivation, error) {
	token, err := restoreRandomToken()
	if err != nil {
		return restoreActivation{}, err
	}
	recoveryRoot := filepath.Join(h.stagingRoot, "recovery")
	if err := os.MkdirAll(recoveryRoot, 0o700); err != nil {
		return restoreActivation{}, fmt.Errorf("prepare database recovery directory: %w", err)
	}
	if err := os.Chmod(recoveryRoot, 0o700); err != nil {
		return restoreActivation{}, fmt.Errorf("secure database recovery directory: %w", err)
	}
	rollbackPath := filepath.Join(recoveryRoot, database.Target.Engine+"-"+database.Target.Name+"-"+token+".sql")
	tool := "mysqldump"
	client := "mysql"
	if database.Target.Engine == "mariadb" {
		tool, client = "mariadb-dump", "mariadb"
	}
	connection := []string{"--protocol=socket", "--socket=" + database.Target.Socket, "--user=root"}
	dumpArgs := append(append([]string{}, connection...), "--single-transaction", "--routines", "--events", "--triggers", "--hex-blob", "--default-character-set=utf8mb4", "--databases", database.Target.Name)
	if database.Target.Engine == "mysql" {
		dumpArgs = append(dumpArgs[:len(dumpArgs)-2], "--set-gtid-purged=OFF", "--databases", database.Target.Name)
	}
	if err := h.runner.RunToFile(ctx, tool, dumpArgs, []string{"MYSQL_HISTFILE=/dev/null"}, rollbackPath); err != nil {
		return restoreActivation{}, fmt.Errorf("create %s restore rollback point: %w", database.Target.Engine, err)
	}
	removeRollback := true
	defer func() {
		if removeRollback {
			_ = os.Remove(rollbackPath)
		}
	}()
	if size, _, err := artifactMetadata(rollbackPath); err != nil || size == 0 {
		return restoreActivation{}, fmt.Errorf("verify %s restore rollback point: %w", database.Target.Engine, firstRestoreError(err, errors.New("rollback point is empty")))
	}
	if err := syncRecoveryArtifact(rollbackPath); err != nil {
		return restoreActivation{}, err
	}

	activation := h.mySQLRestoreActivation(database.Target, client, connection, rollbackPath)
	if database.Clear {
		if err := h.resetMySQLDatabase(ctx, database.Target, client, connection); err != nil {
			rollbackErr := activation.rollbackBounded(ctx)
			removeRollback = rollbackErr == nil
			return restoreActivation{}, errors.Join(err, rollbackErr)
		}
	}
	if err := h.runner.RunFromFile(ctx, client, connection, []string{"MYSQL_HISTFILE=/dev/null"}, dump); err != nil {
		cause := fmt.Errorf("restore %s database %s: %w", database.Target.Engine, database.Target.Name, err)
		rollbackErr := activation.rollbackBounded(ctx)
		removeRollback = rollbackErr == nil
		return restoreActivation{}, errors.Join(cause, rollbackErr)
	}
	if err := h.verifyMySQLDatabase(ctx, database.Target, client, connection); err != nil {
		rollbackErr := activation.rollbackBounded(ctx)
		removeRollback = rollbackErr == nil
		return restoreActivation{}, errors.Join(err, rollbackErr)
	}
	removeRollback = false
	return activation, nil
}

func (h *HostOperator) mySQLRestoreActivation(target DatabaseTarget, client string, connection []string, rollbackPath string) restoreActivation {
	activation := restoreActivation{
		description: target.Engine + " database " + target.Name,
		commit: func(context.Context) error {
			if err := os.Remove(rollbackPath); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("remove database rollback point: %w", err)
			}
			return syncParentDirectory(filepath.Dir(rollbackPath))
		},
	}
	activation.rollback = func(ctx context.Context) error {
		if err := h.resetMySQLDatabase(ctx, target, client, connection); err != nil {
			return err
		}
		if err := h.runner.RunFromFile(ctx, client, connection, []string{"MYSQL_HISTFILE=/dev/null"}, rollbackPath); err != nil {
			return fmt.Errorf("replay %s restore rollback point retained at %s: %w", target.Engine, rollbackPath, err)
		}
		if err := h.verifyMySQLDatabase(ctx, target, client, connection); err != nil {
			return fmt.Errorf("verify %s restore rollback retained at %s: %w", target.Engine, rollbackPath, err)
		}
		if err := os.Remove(rollbackPath); err != nil {
			return fmt.Errorf("remove applied database rollback point: %w", err)
		}
		return syncParentDirectory(filepath.Dir(rollbackPath))
	}
	return activation
}

func (activation restoreActivation) rollbackBounded(ctx context.Context) error {
	recoveryContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), restoreRecoveryTimeout)
	defer cancel()
	return activation.rollback(recoveryContext)
}

func (h *HostOperator) resetMySQLDatabase(ctx context.Context, target DatabaseTarget, client string, connection []string) error {
	statement := "DROP DATABASE IF EXISTS " + quoteMySQLIdentifier(target.Name) + ";\n" +
		"CREATE DATABASE " + quoteMySQLIdentifier(target.Name) + " CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;\n"
	file, err := os.CreateTemp(h.stagingRoot, ".mysql-reset-*.sql")
	if err != nil {
		return err
	}
	path := file.Name()
	defer os.Remove(path)
	err = file.Chmod(0o600)
	if err == nil {
		_, err = file.WriteString(statement)
	}
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		return errors.Join(err, closeErr)
	}
	if err := h.runner.RunFromFile(ctx, client, connection, []string{"MYSQL_HISTFILE=/dev/null"}, path); err != nil {
		return fmt.Errorf("reset %s database %s: %w", target.Engine, target.Name, err)
	}
	return nil
}

func (h *HostOperator) verifyMySQLDatabase(ctx context.Context, target DatabaseTarget, client string, connection []string) error {
	args := append(append([]string{}, connection...), "--batch", "--skip-column-names", "--execute", "SELECT 1;", target.Name)
	output, err := h.runner.Run(ctx, client, args, []string{"MYSQL_HISTFILE=/dev/null"})
	if err != nil || strings.TrimSpace(output) != "1" {
		return fmt.Errorf("verify restored %s database: %w: %s", target.Engine, firstRestoreError(err, errors.New("database did not answer verification query")), sanitize(output))
	}
	return nil
}

func quoteMySQLIdentifier(value string) string {
	return "`" + strings.ReplaceAll(value, "`", "``") + "`"
}

func syncRecoveryArtifact(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	syncErr := file.Sync()
	closeErr := file.Close()
	if syncErr != nil || closeErr != nil {
		return fmt.Errorf("persist database rollback point: %w", errors.Join(syncErr, closeErr))
	}
	if err := syncParentDirectory(filepath.Dir(path)); err != nil {
		return fmt.Errorf("persist database recovery directory: %w", err)
	}
	return nil
}

func firstRestoreError(values ...error) error {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return errors.New("restore failed")
}
