package postgres

import (
	"context"
	"strings"
	"testing"
)

// TestGuardRoleRemovableRefusesLastRoleOfOwnedDatabase enforces the rule the
// user asked for: Postgres must not be left with a database whose only managed
// role is being deleted.
func TestGuardRoleRemovableRefusesLastRoleOfOwnedDatabase(t *testing.T) {
	module, _ := newSizeTestModule(t, "app_db")
	ctx := context.Background()
	err := module.guardRoleRemovable(ctx, "role_owner", "app_owner")
	if err == nil || !strings.Contains(err.Error(), "only role of database app_db") {
		t.Fatalf("guard err = %v, want a last-role refusal", err)
	}
}

// TestGuardRoleRemovableAllowsAndPicksInheritor is the other half: with a second
// role granted on the database, the owner can go and roleDatabasesFor names the
// survivor as the inheriting owner so the operator can transfer ownership.
func TestGuardRoleRemovableAllowsAndPicksInheritor(t *testing.T) {
	module, _ := newSizeTestModule(t, "app_db")
	ctx := context.Background()
	second := &roleModel{ID: "role_second", InstanceID: "postgresql_18_nexa_main", Name: "app_reader", Status: string(StatusActive), CreatedAt: sizeTestClock, UpdatedAt: sizeTestClock}
	if _, err := module.database.NewInsert().Model(second).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	grant := &grantModel{ID: "grant_1", DatabaseID: "database_app_db", RoleID: second.ID, Access: "read_only", Status: string(StatusActive), CreatedAt: sizeTestClock, UpdatedAt: sizeTestClock}
	if _, err := module.database.NewInsert().Model(grant).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if err := module.guardRoleRemovable(ctx, "role_owner", "app_owner"); err != nil {
		t.Fatalf("guard err = %v, want the delete to be allowed", err)
	}
	roleDatabases, err := module.roleDatabasesFor(ctx, "role_owner")
	if err != nil {
		t.Fatal(err)
	}
	if len(roleDatabases) != 1 || roleDatabases[0].Name != "app_db" || roleDatabases[0].NewOwner != "app_reader" {
		t.Fatalf("roleDatabases = %+v, want app_db inherited by app_reader", roleDatabases)
	}
}
