package databases

import (
	"context"
	"testing"

	"github.com/nexa-panel/nexa-panel/internal/platform/jobs"
)

// TestEngineWorkflows drives the full shared lifecycle once per engine: user
// creation with a client-supplied password, a failed password change that must
// preserve the working credential, database creation, a verified backup, and a
// restore — each a single direct job. The flow is one implementation, so the
// table proves the adapters keep both engines behaviorally identical.
func TestEngineWorkflows(t *testing.T) {
	for _, engineKey := range []string{"mysql", "postgresql"} {
		t.Run(engineKey, func(t *testing.T) {
			ctx := context.Background()
			h := newTestModule(t)
			h.start(t)
			module, serverID := h.module, h.serverID(engineKey)

			user, createJob, err := module.CreateUser(ctx, CreateUserRequest{ServerID: serverID, Name: "app_user", Password: "s3cret-passw0rd"}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if user.Engine != engineKey {
				t.Fatalf("user engine = %q, want %q", user.Engine, engineKey)
			}
			waitJob(t, h.queue, createJob.ID, jobs.StateSucceeded)
			users, _ := module.ListUsers(ctx, serverID)
			if len(users) != 1 || users[0].Status != StatusActive || users[0].CredentialVersion != 1 {
				t.Fatalf("users=%+v", users)
			}
			// The engine received exactly the password the client chose.
			if secrets := h.operatorSecrets(engineKey); len(secrets) != 1 || secrets[0] != "s3cret-passw0rd" {
				t.Fatalf("operator secrets = %v", secrets)
			}
			if _, _, err := module.CreateUser(ctx, CreateUserRequest{ServerID: serverID, Name: "short_pw", Password: "short"}, nil); err == nil {
				t.Fatal("a sub-8-character password was accepted")
			}

			// A failed password change must keep the old working credential.
			h.failNextApply(engineKey)
			_, rotateJob, err := module.SetPassword(ctx, user.ID, "another-passw0rd", nil)
			if err != nil {
				t.Fatal(err)
			}
			waitJob(t, h.queue, rotateJob.ID, jobs.StateFailed)
			row, err := h.engine(t, engineKey).store.GetUser(ctx, user.ID)
			if err != nil || Status(row.Status) != StatusActive || row.CredentialCiphertext == nil || row.PendingCredentialCiphertext != nil || row.Failure == nil {
				t.Fatalf("failed password change did not preserve active credential: %+v, %v", row, err)
			}

			managed, databaseJob, err := module.CreateDatabase(ctx, CreateDatabaseRequest{ServerID: serverID, Name: "app_db", OwnerUserID: user.ID}, nil)
			if err != nil {
				t.Fatal(err)
			}
			waitJob(t, h.queue, databaseJob.ID, jobs.StateSucceeded)
			databases, _ := module.ListDatabases(ctx, serverID)
			if len(databases) != 1 || databases[0].Status != StatusActive {
				t.Fatalf("databases=%+v", databases)
			}

			point, backupJob, err := module.CreateBackup(ctx, managed.ID, nil)
			if err != nil {
				t.Fatal(err)
			}
			waitJob(t, h.queue, backupJob.ID, jobs.StateSucceeded)
			points, _ := module.ListRestorePoints(ctx, managed.ID)
			if len(points) != 1 || points[0].Status != StatusVerified || points[0].SHA256 == "" {
				t.Fatalf("restore points = %+v", points)
			}

			_, restoreJob, err := module.Restore(ctx, point.ID, nil)
			if err != nil {
				t.Fatal(err)
			}
			waitJob(t, h.queue, restoreJob.ID, jobs.StateSucceeded)
			databases, _ = module.ListDatabases(ctx, serverID)
			if len(databases) != 1 || databases[0].Status != StatusActive {
				t.Fatalf("databases after restore = %+v", databases)
			}
		})
	}
}

// TestDropUserTransfersOwnershipAtCommit runs the whole drop path end to end
// for both engines: the dropped owner's database must end up owned by the
// surviving grantee, mirroring the transfers the engine performed.
func TestDropUserTransfersOwnershipAtCommit(t *testing.T) {
	for _, engineKey := range []string{"mysql", "postgresql"} {
		t.Run(engineKey, func(t *testing.T) {
			ctx := context.Background()
			h := newTestModule(t)
			h.start(t)
			module := h.module
			h.seedUser(t, engineKey, "user_owner", "app_owner")
			h.seedUser(t, engineKey, "user_reader", "app_reader")
			h.seedDatabase(t, engineKey, "database_app", "app_db", "user_owner")
			h.seedGrant(t, engineKey, "grant_reader", "database_app", "user_reader")

			dropJob, err := module.DropUser(ctx, "user_owner", nil)
			if err != nil {
				t.Fatal(err)
			}
			waitJob(t, h.queue, dropJob.ID, jobs.StateSucceeded)

			row, err := h.engine(t, engineKey).store.GetDatabase(ctx, "database_app")
			if err != nil {
				t.Fatal(err)
			}
			if row.OwnerUserID != "user_reader" {
				t.Fatalf("owner after drop = %q, want user_reader", row.OwnerUserID)
			}
			users, err := module.ListUsers(ctx, h.serverID(engineKey))
			if err != nil || len(users) != 1 || users[0].ID != "user_reader" {
				t.Fatalf("users after drop = %+v, err %v", users, err)
			}
		})
	}
}

// TestResourceRoutingAcrossEngines proves a bare resource ID reaches the
// engine that owns it when both engines hold data.
func TestResourceRoutingAcrossEngines(t *testing.T) {
	ctx := context.Background()
	h := newTestModule(t)
	h.seedUser(t, "mysql", "user_my", "my_user")
	h.seedUser(t, "postgresql", "user_pg", "pg_user")
	h.seedDatabase(t, "mysql", "database_my", "my_db", "user_my")
	h.seedDatabase(t, "postgresql", "database_pg", "pg_db", "user_pg")

	eng, err := h.module.engineForResource(ctx, resourceDatabase, "database_pg")
	if err != nil || eng.spec.Engine != "postgresql" {
		t.Fatalf("database_pg routed to %v, err %v", eng, err)
	}
	eng, err = h.module.engineForResource(ctx, resourceUser, "user_my")
	if err != nil || eng.spec.Engine != "mysql" {
		t.Fatalf("user_my routed to %v, err %v", eng, err)
	}
	items, err := h.module.ListDatabases(ctx, "")
	if err != nil || len(items) != 2 || items[0].Engine == items[1].Engine {
		t.Fatalf("merged databases = %+v, err %v", items, err)
	}
	if items[0].Name != "my_db" || items[1].Name != "pg_db" {
		t.Fatalf("merged order = %+v, want sorted by name", items)
	}
}
