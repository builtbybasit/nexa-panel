package mysql

import (
	"context"
	"strings"
	"testing"
)

// dropRunner answers Discover and reports the target schema/account as absent,
// so the post-drop verification passes and the emitted SQL can be inspected.
type dropRunner struct {
	engine   string
	commands []Command
}

func (r *dropRunner) Run(_ context.Context, command Command) ([]byte, error) {
	r.commands = append(r.commands, command)
	joined := strings.Join(command.Args, " ")
	if strings.Contains(joined, "SELECT @@version") {
		return []byte(r.engine), nil
	}
	// INFORMATION_SCHEMA.SCHEMATA and mysql.user both come back empty: gone.
	return nil, nil
}

func (r *dropRunner) stdinFor(fragment string) string {
	for _, command := range r.commands {
		if strings.Contains(command.Stdin, fragment) {
			return command.Stdin
		}
	}
	return ""
}

func TestDropDatabaseIssuesDropSchema(t *testing.T) {
	runner := &dropRunner{engine: "8.4.5\tMySQL Community Server - GPL\t/run/mysqld/mysqld.sock\t3306"}
	operator := newTestOperator(t, runner, t.TempDir())
	plan, err := operator.Plan(context.Background(), Change{Action: ActionDropDatabase, EngineID: "mysql", Database: "app_db"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := operator.Apply(context.Background(), Execution{Plan: plan}); err != nil {
		t.Fatal(err)
	}
	if sql := runner.stdinFor("DROP DATABASE"); !strings.Contains(sql, "DROP DATABASE IF EXISTS `app_db`") {
		t.Fatalf("drop SQL = %q", sql)
	}
}

func TestDropAccountIssuesDropUser(t *testing.T) {
	runner := &dropRunner{engine: "8.4.5\tMySQL Community Server - GPL\t/run/mysqld/mysqld.sock\t3306"}
	operator := newTestOperator(t, runner, t.TempDir())
	plan, err := operator.Plan(context.Background(), Change{Action: ActionDropAccount, EngineID: "mysql", Account: "app_user", AccountHost: "localhost"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := operator.Apply(context.Background(), Execution{Plan: plan}); err != nil {
		t.Fatal(err)
	}
	sql := runner.stdinFor("DROP USER")
	if !strings.Contains(sql, "DROP USER IF EXISTS 'app_user'@'localhost'") || !strings.Contains(sql, "FLUSH PRIVILEGES") {
		t.Fatalf("drop SQL = %q", sql)
	}
}

func TestRevokeGrantStripsDatabaseScope(t *testing.T) {
	runner := &dropRunner{engine: "8.4.5\tMySQL Community Server - GPL\t/run/mysqld/mysqld.sock\t3306"}
	operator := newTestOperator(t, runner, t.TempDir())
	plan, err := operator.Plan(context.Background(), Change{Action: ActionRevokeGrant, EngineID: "mysql", Database: "app_db", Account: "app_user", AccountHost: "localhost"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := operator.Apply(context.Background(), Execution{Plan: plan}); err != nil {
		t.Fatal(err)
	}
	if sql := runner.stdinFor("REVOKE ALL PRIVILEGES"); !strings.Contains(sql, "REVOKE ALL PRIVILEGES ON `app_db`.* FROM 'app_user'@'localhost'") {
		t.Fatalf("revoke SQL = %q", sql)
	}
}
