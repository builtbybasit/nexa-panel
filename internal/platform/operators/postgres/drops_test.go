package postgres

import (
	"context"
	"strings"
	"testing"
)

// dropRoleRunner answers Discover and reports the role as absent after the drop,
// so verification passes and the emitted SQL can be inspected.
type dropRoleRunner struct {
	discovery string
	commands  []Command
}

func (r *dropRoleRunner) Run(_ context.Context, command Command) ([]byte, error) {
	r.commands = append(r.commands, command)
	if command.Name == "pg_lsclusters" {
		return []byte(r.discovery), nil
	}
	// pg_roles existence probe comes back empty: the role is gone.
	return nil, nil
}

func (r *dropRoleRunner) stdinFor(fragment string) string {
	for _, command := range r.commands {
		if strings.Contains(command.Stdin, fragment) {
			return command.Stdin
		}
	}
	return ""
}

// TestDropRoleTransfersOwnershipBeforeDropping checks the ordering that makes a
// Postgres role droppable: ownership of an owned database is moved with ALTER
// DATABASE, its objects are reassigned, and only then is the role dropped.
func TestDropRoleTransfersOwnershipBeforeDropping(t *testing.T) {
	runner := &dropRoleRunner{discovery: `[{"version":"18","cluster":"nexa_main","port":5432,"status":"online","owner":"postgres"}]`}
	operator := newTestOperator(t, runner)
	change := Change{
		Action:     ActionDropRole,
		InstanceID: "postgresql_18_nexa_main",
		Role:       "app_owner",
		RoleDatabases: []RoleDatabase{
			{Name: "app_db", NewOwner: "app_reader"},
			{Name: "shared_db"},
		},
	}
	plan, err := operator.Plan(context.Background(), change)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := operator.Apply(context.Background(), Execution{Plan: plan}); err != nil {
		t.Fatal(err)
	}
	if sql := runner.stdinFor("ALTER DATABASE"); !strings.Contains(sql, `ALTER DATABASE "app_db" OWNER TO "app_reader"`) {
		t.Fatalf("ownership transfer = %q", sql)
	}
	if sql := runner.stdinFor("REASSIGN OWNED"); !strings.Contains(sql, `REASSIGN OWNED BY "app_owner" TO "app_reader"`) {
		t.Fatalf("reassign = %q", sql)
	}
	if sql := runner.stdinFor("DROP ROLE"); !strings.Contains(sql, `DROP ROLE IF EXISTS "app_owner"`) {
		t.Fatalf("drop role = %q", sql)
	}
	// The granted-only database is cleared without an ownership transfer.
	var sawSharedDropOwned bool
	for _, command := range runner.commands {
		joined := strings.Join(command.Args, " ")
		if strings.Contains(joined, "--dbname shared_db") && strings.Contains(command.Stdin, "DROP OWNED BY") {
			sawSharedDropOwned = true
			if strings.Contains(command.Stdin, "REASSIGN OWNED") {
				t.Fatalf("granted-only database should not reassign ownership: %q", command.Stdin)
			}
		}
	}
	if !sawSharedDropOwned {
		t.Fatalf("expected DROP OWNED BY in the granted-only database; commands: %+v", runner.commands)
	}
}
