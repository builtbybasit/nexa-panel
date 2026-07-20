package mysql

import (
	"context"
	"strings"
	"testing"
)

// TestGuardAccountRemovableRefusesLastUserOfOwnedDatabase locks in the rule the
// user asked for: a database must never be left with no managed user, so the
// sole owner cannot be deleted.
func TestGuardAccountRemovableRefusesLastUserOfOwnedDatabase(t *testing.T) {
	module, _ := newSizeTestModule(t, "app_db")
	ctx := context.Background()
	err := module.guardAccountRemovable(ctx, "account_owner", "app_owner")
	if err == nil || !strings.Contains(err.Error(), "only user of database app_db") {
		t.Fatalf("guard err = %v, want a last-user refusal", err)
	}
}

// TestGuardAccountRemovableAllowsAndReassignsWhenAnotherUserExists is the other
// half: with a second user on the database, the owner can go and ownership is
// handed to the survivor at commit.
func TestGuardAccountRemovableAllowsAndReassignsWhenAnotherUserExists(t *testing.T) {
	module, _ := newSizeTestModule(t, "app_db")
	ctx := context.Background()
	second := &accountModel{ID: "account_second", EngineID: "mysql", Name: "app_reader", Host: "localhost", Status: string(StatusActive), CreatedAt: sizeTestClock, UpdatedAt: sizeTestClock}
	if _, err := module.database.NewInsert().Model(second).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	grant := &grantModel{ID: "grant_1", DatabaseID: "database_app_db", AccountID: second.ID, Access: "read_only", Status: string(StatusActive), CreatedAt: sizeTestClock, UpdatedAt: sizeTestClock}
	if _, err := module.database.NewInsert().Model(grant).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if err := module.guardAccountRemovable(ctx, "account_owner", "app_owner"); err != nil {
		t.Fatalf("guard err = %v, want the delete to be allowed", err)
	}
	if err := module.reassignOwnedDatabases(ctx, "account_owner"); err != nil {
		t.Fatal(err)
	}
	db, err := module.getDatabaseModel(ctx, "database_app_db")
	if err != nil {
		t.Fatal(err)
	}
	if db.OwnerAccountID != "account_second" {
		t.Fatalf("owner after reassignment = %q, want account_second", db.OwnerAccountID)
	}
}
