package databases

import (
	"context"
	"strings"
	"testing"
)

// TestGuardUserRemovableRefusesLastUserOfOwnedDatabase locks in the invariant:
// a database must never be left with no managed user, so its sole owner cannot
// be deleted — on either engine.
func TestGuardUserRemovableRefusesLastUserOfOwnedDatabase(t *testing.T) {
	for _, engineKey := range []string{"mysql", "postgresql"} {
		t.Run(engineKey, func(t *testing.T) {
			ctx := context.Background()
			h := newTestModule(t)
			h.seedUser(t, engineKey, "user_owner", "app_owner")
			h.seedDatabase(t, engineKey, "database_app", "app_db", "user_owner")
			err := h.module.guardUserRemovable(ctx, h.engine(t, engineKey), "user_owner", "app_owner")
			if err == nil || !strings.Contains(err.Error(), "only user of database app_db") {
				t.Fatalf("guard err = %v, want a last-user refusal", err)
			}
		})
	}
}

// TestGuardUserRemovableAllowsAndPicksInheritor is the other half: with a
// second user granted on the database, the owner can go and userDatabasesFor
// names the survivor as the inheriting owner.
func TestGuardUserRemovableAllowsAndPicksInheritor(t *testing.T) {
	for _, engineKey := range []string{"mysql", "postgresql"} {
		t.Run(engineKey, func(t *testing.T) {
			ctx := context.Background()
			h := newTestModule(t)
			h.seedUser(t, engineKey, "user_owner", "app_owner")
			h.seedUser(t, engineKey, "user_reader", "app_reader")
			h.seedDatabase(t, engineKey, "database_app", "app_db", "user_owner")
			h.seedGrant(t, engineKey, "grant_1", "database_app", "user_reader")
			eng := h.engine(t, engineKey)
			if err := h.module.guardUserRemovable(ctx, eng, "user_owner", "app_owner"); err != nil {
				t.Fatalf("guard err = %v, want the delete to be allowed", err)
			}
			userDatabases, err := h.module.userDatabasesFor(ctx, eng, "user_owner")
			if err != nil {
				t.Fatal(err)
			}
			if len(userDatabases) != 1 || userDatabases[0].Name != "app_db" || userDatabases[0].NewOwner != "app_reader" || userDatabases[0].NewOwnerID != "user_reader" {
				t.Fatalf("userDatabases = %+v, want app_db inherited by app_reader", userDatabases)
			}
		})
	}
}

// TestUserDatabasesIncludeGrantOnlyEntanglements: databases a user merely has
// a grant on ride along without a new owner, so the engine can revoke cleanly.
func TestUserDatabasesIncludeGrantOnlyEntanglements(t *testing.T) {
	ctx := context.Background()
	h := newTestModule(t)
	h.seedUser(t, "postgresql", "user_owner", "app_owner")
	h.seedUser(t, "postgresql", "user_reader", "app_reader")
	h.seedDatabase(t, "postgresql", "database_app", "app_db", "user_owner")
	h.seedGrant(t, "postgresql", "grant_1", "database_app", "user_reader")
	eng := h.engine(t, "postgresql")
	userDatabases, err := h.module.userDatabasesFor(ctx, eng, "user_reader")
	if err != nil {
		t.Fatal(err)
	}
	if len(userDatabases) != 1 || userDatabases[0].Name != "app_db" || userDatabases[0].NewOwner != "" {
		t.Fatalf("userDatabases = %+v, want a grant-only entry with no owner transfer", userDatabases)
	}
}
