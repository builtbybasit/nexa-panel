package postgres

import (
	"context"
	"fmt"
	"github.com/nexa-panel/nexa-panel/internal/platform/hostcmd"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func (o *HostOperator) provision(ctx context.Context, change Change) (Observation, error) {
	dataPath := filepath.Join(o.dataRoot, change.Version, change.Cluster)
	logPath := filepath.Join(o.logRoot, fmt.Sprintf("postgresql-%s-%s.log", change.Version, change.Cluster))
	args := []string{"--port", strconv.Itoa(change.Port), "--datadir", dataPath, "--socketdir", o.socketRoot, "--logfile", logPath, "--start-conf", "auto", change.Version, change.Cluster}
	if output, err := o.runner.Run(ctx, Command{Name: "pg_createcluster", Args: args}); err != nil {
		return Observation{}, hostcmd.Error("create PostgreSQL instance", output, err)
	}
	if output, err := o.runner.Run(ctx, Command{Name: "pg_ctlcluster", Args: []string{change.Version, change.Cluster, "start"}}); err != nil && !strings.Contains(strings.ToLower(string(output)), "already running") {
		return Observation{}, hostcmd.Error("start PostgreSQL instance", output, err)
	}
	if output, err := o.runner.Run(ctx, Command{Name: binary(change.Version, "pg_isready"), Args: []string{"--host", o.socketRoot, "--port", strconv.Itoa(change.Port), "--dbname", "postgres"}}); err != nil {
		return Observation{}, hostcmd.Error("verify PostgreSQL instance", output, err)
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
	if err := o.ensureSocketScramAuth(ctx, change.Version, change.Cluster); err != nil {
		return Observation{}, err
	}
	return Observation{Action: change.Action, Role: change.Role, Verified: true}, nil
}

// ensureSocketScramAuth guarantees the cluster accepts password (scram) logins
// over its Unix socket. Admin tools such as pgAdmin run in a container that
// cannot reach loopback-only Postgres over TCP, so they connect via the mounted
// socket and sign in as a managed role. The stock pg_hba.conf authenticates
// local connections with "peer", which can never match a containerised client,
// so this inserts a "local all all scram-sha-256" rule ahead of the peer
// catch-all. The "local all postgres peer" superuser line sorts earlier and is
// left untouched, so Nexa's own maintenance access keeps using peer. The edit is
// idempotent and only reloads the cluster when it actually changes the file. A
// missing pg_hba.conf (e.g. no such cluster in a unit-test environment) is a
// no-op rather than an error.
func (o *HostOperator) ensureSocketScramAuth(ctx context.Context, version, cluster string) error {
	if version == "" || cluster == "" {
		return nil
	}
	path := filepath.Join(o.configRoot, version, cluster, "pg_hba.conf")
	content, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read pg_hba for socket authentication: %w", err)
	}
	lines := strings.Split(string(content), "\n")
	insertAt := -1
	for index, line := range lines {
		fields := strings.Fields(line)
		if len(fields) < 4 || strings.HasPrefix(fields[0], "#") {
			continue
		}
		if fields[0] == "local" && fields[1] == "all" && fields[2] == "all" {
			if fields[3] == "scram-sha-256" {
				return nil // already authorised for socket password logins
			}
			if insertAt == -1 {
				insertAt = index // sit ahead of the first catch-all peer rule
			}
		}
	}
	rule := "local   all             all                                     scram-sha-256"
	if insertAt == -1 {
		lines = append(lines, rule, "")
	} else {
		expanded := make([]string, 0, len(lines)+1)
		expanded = append(expanded, lines[:insertAt]...)
		expanded = append(expanded, rule)
		expanded = append(expanded, lines[insertAt:]...)
		lines = expanded
	}
	if err := writePgHba(path, strings.Join(lines, "\n")); err != nil {
		return err
	}
	if output, err := o.runner.Run(ctx, Command{Name: "pg_ctlcluster", Args: []string{version, cluster, "reload"}}); err != nil {
		return hostcmd.Error("reload PostgreSQL for socket authentication", output, err)
	}
	return nil
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
		return Observation{}, hostcmd.Error("create PostgreSQL database", output, err)
	}
	if err := o.verifyDatabase(ctx, change, change.Database); err != nil {
		return Observation{}, err
	}
	return Observation{Action: change.Action, Database: change.Database, Role: change.OwnerRole, Verified: true}, nil
}
