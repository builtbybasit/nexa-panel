package mysql

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/nexa-panel/nexa-panel/internal/platform/hostcmd"
	"github.com/nexa-panel/nexa-panel/internal/platform/secureid"
)

func (execRunner) Run(ctx context.Context, command Command) ([]byte, error) {
	process := exec.CommandContext(ctx, command.Name, command.Args...)
	process.Env = append(os.Environ(), command.Env...)
	var input *os.File
	var output *os.File
	var err error
	if command.StdinPath != "" {
		input, err = os.Open(command.StdinPath)
		if err != nil {
			return nil, err
		}
		defer input.Close()
		process.Stdin = input
	} else if command.Stdin != "" {
		process.Stdin = strings.NewReader(command.Stdin)
	}
	buffer := &hostcmd.Writer{Limit: 64 * 1024}
	if command.StdoutPath != "" {
		output, err = os.OpenFile(command.StdoutPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o640)
		if err != nil {
			return nil, err
		}
		defer output.Close()
		process.Stdout = output
	} else {
		process.Stdout = buffer
	}
	process.Stderr = buffer
	err = process.Run()
	return buffer.Bytes(), err
}

func (o *HostOperator) createDatabase(ctx context.Context, engine *Engine, change Change) (Observation, error) {
	sql := "CREATE DATABASE " + quoteIdentifier(change.Database) + " CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;\n"
	if output, err := o.runner.Run(ctx, o.stdinCommand(engine, sql)); err != nil {
		return Observation{}, hostcmd.Error("create MySQL-family database", output, err)
	}
	if err := o.verifyDatabase(ctx, engine, change.Database); err != nil {
		return Observation{}, err
	}
	grantChange := change
	grantChange.Action = ActionApplyGrant
	grantChange.Access = AccessReadWrite
	granted, err := o.applyGrant(ctx, engine, grantChange)
	if err != nil {
		_, _ = o.runner.Run(context.WithoutCancel(ctx), o.stdinCommand(engine, "DROP DATABASE "+quoteIdentifier(change.Database)+";\n"))
		return Observation{}, err
	}
	granted.Action = change.Action
	return granted, nil
}

func (o *HostOperator) createAccount(ctx context.Context, engine *Engine, change Change, secret string, rotate bool) (Observation, error) {
	account := quoteAccount(change.Account, change.AccountHost)
	statement := "CREATE USER " + account + " IDENTIFIED BY " + quoteLiteral(secret) + ";"
	if rotate {
		statement = "ALTER USER " + account + " IDENTIFIED BY " + quoteLiteral(secret) + ";"
	}
	if output, err := o.runner.Run(ctx, o.stdinCommand(engine, statement+"\n")); err != nil {
		return Observation{}, redactCommandError("mutate MySQL-family account", output, err, secret)
	}
	if err := o.verifyAccount(ctx, engine, change); err != nil {
		return Observation{}, err
	}
	return Observation{Action: change.Action, Engine: engine, Account: change.Account + "@" + change.AccountHost, Verified: true}, nil
}

func safePrivileges(output string) ([]string, error) {
	allowed := map[string]struct{}{"SELECT": {}, "INSERT": {}, "UPDATE": {}, "DELETE": {}, "CREATE": {}, "ALTER": {}, "INDEX": {}, "DROP": {}, "REFERENCES": {}, "CREATE VIEW": {}, "SHOW VIEW": {}, "TRIGGER": {}}
	values := []string{}
	for _, line := range strings.Split(output, "\n") {
		value := strings.ToUpper(strings.TrimSpace(line))
		if value == "" {
			continue
		}
		if _, ok := allowed[value]; !ok {
			return nil, errors.New("MySQL-family returned an unsupported database privilege")
		}
		values = append(values, value)
	}
	return values, nil
}

func (o *HostOperator) databaseExists(ctx context.Context, engine *Engine, database string) (bool, error) {
	output, err := o.runner.Run(ctx, o.clientCommand(engine, "SELECT SCHEMA_NAME FROM INFORMATION_SCHEMA.SCHEMATA WHERE SCHEMA_NAME="+quoteLiteral(database)+";"))
	if err != nil {
		return false, hostcmd.Error("inspect MySQL-family database before restore", output, err)
	}
	observed := strings.TrimSpace(string(output))
	switch observed {
	case "":
		return false, nil
	case database:
		return true, nil
	default:
		return false, hostcmd.Error("inspect MySQL-family database before restore", output, errors.New("unexpected database query response"))
	}
}

func (o *HostOperator) verifyDatabase(ctx context.Context, engine *Engine, database string) error {
	output, err := o.runner.Run(ctx, o.clientCommand(engine, "SELECT SCHEMA_NAME FROM INFORMATION_SCHEMA.SCHEMATA WHERE SCHEMA_NAME="+quoteLiteral(database)+";"))
	if err != nil || strings.TrimSpace(string(output)) != database {
		return hostcmd.Error("verify MySQL-family database", output, firstError(err, errors.New("database was not observed")))
	}
	return nil
}

func (o *HostOperator) verifyAccount(ctx context.Context, engine *Engine, change Change) error {
	query := "SELECT User FROM mysql.user WHERE User=" + quoteLiteral(change.Account) + " AND Host=" + quoteLiteral(change.AccountHost) + ";"
	output, err := o.runner.Run(ctx, o.clientCommand(engine, query))
	if err != nil || strings.TrimSpace(string(output)) != change.Account {
		return hostcmd.Error("verify MySQL-family account", output, firstError(err, errors.New("account was not observed")))
	}
	return nil
}

func (o *HostOperator) baseClientArgs(engine *Engine) Command {
	name := "mysql"
	if engine != nil && engine.Kind == EngineMariaDB {
		name = "mariadb"
	}
	return Command{Name: name, Args: []string{"--protocol=socket", "--socket=" + o.socketPath, "--user=root", "--batch", "--skip-column-names", "--skip-auto-rehash"}, Env: []string{"MYSQL_HISTFILE=/dev/null"}}
}

func (o *HostOperator) clientCommand(engine *Engine, query string) Command {
	command := o.baseClientArgs(engine)
	command.Args = append(command.Args, "--execute", query)
	return command
}

func (o *HostOperator) clientCommandName(name, query string) Command {
	command := Command{Name: name, Args: []string{"--protocol=socket", "--socket=" + o.socketPath, "--user=root", "--batch", "--skip-column-names", "--skip-auto-rehash"}, Env: []string{"MYSQL_HISTFILE=/dev/null"}}
	command.Args = append(command.Args, "--execute", query)
	return command
}

func (o *HostOperator) stdinCommand(engine *Engine, sql string) Command {
	command := o.baseClientArgs(engine)
	command.Stdin = sql
	return command
}

func commandMissing(output []byte, err error) bool {
	message := strings.ToLower(string(output))
	return errors.Is(err, exec.ErrNotFound) || strings.Contains(message, "executable file not found") || strings.Contains(message, "no such file or directory")
}

func (o *HostOperator) dumpCommand(engine *Engine, database string) Command {
	name := "mysqldump"
	if engine.Kind == EngineMariaDB {
		name = "mariadb-dump"
	}
	args := []string{"--protocol=socket", "--socket=" + o.socketPath, "--user=root", "--single-transaction", "--routines", "--events", "--triggers", "--hex-blob", "--default-character-set=utf8mb4"}
	if engine.Kind == EngineMySQL {
		args = append(args, "--set-gtid-purged=OFF")
	}
	args = append(args, "--databases", database)
	return Command{Name: name, Args: args, Env: []string{"MYSQL_HISTFILE=/dev/null"}}
}

func quoteIdentifier(value string) string { return "`" + strings.ReplaceAll(value, "`", "``") + "`" }

func quoteLiteral(value string) string {
	return "'" + strings.ReplaceAll(strings.ReplaceAll(value, "\\", "\\\\"), "'", "''") + "'"
}

func quoteAccount(name, host string) string { return quoteLiteral(name) + "@" + quoteLiteral(host) }

func fingerprint(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func randomID() string { return secureid.Hex(16) }

func fileDigest(path string) (string, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	hasher := sha256.New()
	size, err := io.Copy(hasher, file)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(hasher.Sum(nil)), size, nil
}

func syncFile(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Sync()
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open MySQL-family backup directory for sync: %w", err)
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("sync MySQL-family backup directory: %w", err)
	}
	return nil
}

func firstError(values ...error) error {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return errors.New("operation failed")
}

func redactCommandError(action string, output []byte, err error, secret string) error {
	return hostcmd.Error(action, []byte(strings.ReplaceAll(string(output), secret, "[redacted]")), err)
}
