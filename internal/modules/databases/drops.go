package databases

import (
	"context"
	"errors"
	"fmt"

	"github.com/nexa-panel/nexa-panel/internal/platform/audit"
	"github.com/nexa-panel/nexa-panel/internal/platform/jobs"
)

// isDropOperation reports whether a stored plan removes a resource rather than
// creating or changing one, which decides whether commit deletes the row.
func isDropOperation(op string) bool {
	switch op {
	case ActionDropDatabase, ActionDropUser, ActionRevokeGrant:
		return true
	default:
		return false
	}
}

// DropDatabase removes a managed database through the plan → review → apply
// path; commit deletes the row once the host confirms the database is gone.
func (m *Module) DropDatabase(ctx context.Context, databaseID string, actor *string) (jobs.Job, error) {
	eng, err := m.engineForResource(ctx, resourceDatabase, databaseID)
	if err != nil {
		return jobs.Job{}, errors.New("database not found")
	}
	database, err := eng.store.GetDatabase(ctx, databaseID)
	if err != nil {
		return jobs.Job{}, errors.New("database not found")
	}
	if err := ensureRemovable(Status(database.Status)); err != nil {
		return jobs.Job{}, err
	}
	if err := m.recordDropIntent(ctx, actor, "databases.database_dropped", "database:"+database.ID, Status(database.Status), map[string]any{
		"engine": eng.spec.Engine, "name": database.Name, "serverId": database.ServerID, "ownerUserId": database.OwnerUserID,
	}); err != nil {
		return jobs.Job{}, err
	}
	return m.submitExecute(ctx, eng, resourceDatabase, database.ID, ActionDropDatabase, actor)
}

// DropUser removes a database user, but refuses if that would strand a
// database it solely owns; a database must always keep at least one managed
// user. When another user remains for that database, ownership is handed to it.
func (m *Module) DropUser(ctx context.Context, userID string, actor *string) (jobs.Job, error) {
	eng, err := m.engineForResource(ctx, resourceUser, userID)
	if err != nil {
		return jobs.Job{}, errors.New("user not found")
	}
	user, err := eng.store.GetUser(ctx, userID)
	if err != nil {
		return jobs.Job{}, errors.New("user not found")
	}
	if err := ensureRemovable(Status(user.Status)); err != nil {
		return jobs.Job{}, err
	}
	if err := m.guardUserRemovable(ctx, eng, user.ID, user.Name); err != nil {
		return jobs.Job{}, err
	}
	if err := m.recordDropIntent(ctx, actor, "databases.user_dropped", "database-user:"+user.ID, Status(user.Status), map[string]any{
		"engine": eng.spec.Engine, "name": user.Name, "host": user.Host, "serverId": user.ServerID,
	}); err != nil {
		return jobs.Job{}, err
	}
	return m.submitExecute(ctx, eng, resourceUser, user.ID, ActionDropUser, actor)
}

// DropGrant revokes one user's access to one database, leaving both in place.
func (m *Module) DropGrant(ctx context.Context, grantID string, actor *string) (jobs.Job, error) {
	eng, err := m.engineForResource(ctx, resourceGrant, grantID)
	if err != nil {
		return jobs.Job{}, errors.New("grant not found")
	}
	grant, err := eng.store.GetGrant(ctx, grantID)
	if err != nil {
		return jobs.Job{}, errors.New("grant not found")
	}
	if err := ensureRemovable(Status(grant.Status)); err != nil {
		return jobs.Job{}, err
	}
	if err := m.recordDropIntent(ctx, actor, "databases.grant_revoked", "database-grant:"+grant.ID, Status(grant.Status), map[string]any{
		"engine": eng.spec.Engine, "databaseId": grant.DatabaseID, "userId": grant.UserID, "access": grant.Access,
	}); err != nil {
		return jobs.Job{}, err
	}
	return m.submitExecute(ctx, eng, resourceGrant, grant.ID, ActionRevokeGrant, actor)
}

// recordDropIntent audits a destructive change fail-closed, before the job is
// queued. This is the point the human's intent is accepted — the job runs
// later on a worker with no actor of its own — so it is where the delete is
// attributable, and a delete that cannot be attributed is refused rather than
// performed.
func (m *Module) recordDropIntent(ctx context.Context, actor *string, action, subject string, before Status, metadata map[string]any) error {
	metadata["before"] = map[string]any{"status": string(before)}
	metadata["after"] = map[string]any{"status": "dropped"}
	return m.jobs.Audit().RecordSensitive(ctx, audit.Entry{
		ActorUserID: actor, Action: action, Subject: subject, Metadata: metadata,
	})
}

// ensureRemovable rejects a drop while the resource is mid-operation, so a
// delete cannot interleave with an in-flight change.
func ensureRemovable(status Status) error {
	switch status {
	case StatusActive, StatusFailed, StatusPlanReady:
		return nil
	default:
		return errors.New("resource is busy; wait for the current operation to finish")
	}
}

// guardUserRemovable enforces the last-user rule: a user that solely owns a
// database cannot be deleted, because the database would be left with no
// managed user. A database with another grantee is fine — that user inherits it.
func (m *Module) guardUserRemovable(ctx context.Context, eng *engine, userID, userName string) error {
	owned, err := eng.store.ListDatabasesOwnedBy(ctx, userID)
	if err != nil {
		return err
	}
	for _, db := range owned {
		count, err := eng.store.CountOtherGrants(ctx, db.ID, userID)
		if err != nil {
			return err
		}
		if count == 0 {
			return fmt.Errorf("cannot delete %s: it is the only user of database %s — delete the database or add another user first", userName, db.Name)
		}
	}
	return nil
}

// userDatabasesFor enumerates every database a user about to be dropped
// touches: those it owns (with the user that will inherit them) and those it
// only has a grant on. The engine operator clears the user's objects and
// privileges from each before dropping it, and commit mirrors the same
// ownership transfers into the managed rows.
func (m *Module) userDatabasesFor(ctx context.Context, eng *engine, userID string) ([]UserDatabase, error) {
	result := []UserDatabase{}
	seen := map[string]bool{}
	owned, err := eng.store.ListDatabasesOwnedBy(ctx, userID)
	if err != nil {
		return nil, err
	}
	for _, db := range owned {
		replacement, err := eng.store.FindReplacementGrant(ctx, db.ID, userID)
		if err != nil {
			return nil, fmt.Errorf("database %s has no other user to take ownership", db.Name)
		}
		newOwner, err := eng.store.GetUser(ctx, replacement.UserID)
		if err != nil {
			return nil, err
		}
		result = append(result, UserDatabase{DatabaseID: db.ID, Name: db.Name, NewOwnerID: newOwner.ID, NewOwner: newOwner.Name})
		seen[db.ID] = true
	}
	grants, err := eng.store.ListGrantsForUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	for _, grant := range grants {
		if seen[grant.DatabaseID] {
			continue
		}
		db, err := eng.store.GetDatabase(ctx, grant.DatabaseID)
		if err != nil {
			continue
		}
		result = append(result, UserDatabase{DatabaseID: db.ID, Name: db.Name})
		seen[grant.DatabaseID] = true
	}
	return result, nil
}

// commitDrop removes the managed rows once the host has applied a drop. For a
// user it first mirrors the ownership transfers the executed change promised,
// so the managed owner pointers match the engine.
func (m *Module) commitDrop(ctx context.Context, eng *engine, resourceType, resourceID string, change Change) error {
	switch resourceType {
	case resourceDatabase:
		return eng.store.DeleteDatabaseCascade(ctx, resourceID)
	case resourceUser:
		for _, item := range change.UserDatabases {
			if item.NewOwnerID == "" {
				continue
			}
			if err := eng.store.SetDatabaseOwner(ctx, item.DatabaseID, item.NewOwnerID); err != nil {
				return err
			}
		}
		if err := eng.store.DeleteGrantsForUser(ctx, resourceID); err != nil {
			return err
		}
		return eng.store.DeleteUser(ctx, resourceID)
	case resourceGrant:
		return eng.store.DeleteGrant(ctx, resourceID)
	default:
		return errors.New("database drop resource is unsupported")
	}
}
