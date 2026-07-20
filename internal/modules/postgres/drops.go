package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/nexa-panel/nexa-panel/internal/platform/jobs"
	postgresoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/postgres"
)

// isDropOperation reports whether a stored plan removes a resource rather than
// creating or changing one, which decides whether commit deletes the row.
func isDropOperation(op postgresoperator.Action) bool {
	switch op {
	case postgresoperator.ActionDropDatabase, postgresoperator.ActionDropRole, postgresoperator.ActionRevokeGrant:
		return true
	default:
		return false
	}
}

// DropDatabase removes a managed database through the plan → review → apply path;
// commit deletes the row once the host confirms the database is gone.
func (m *Module) DropDatabase(ctx context.Context, databaseID string, actor *string) (jobs.Job, error) {
	database, err := m.getDatabaseModel(ctx, databaseID)
	if err != nil {
		return jobs.Job{}, errors.New("database not found")
	}
	if err := ensureRemovable(Status(database.Status)); err != nil {
		return jobs.Job{}, err
	}
	return m.beginDrop(ctx, resourceDatabase, database.ID, postgresoperator.ActionDropDatabase, actor)
}

// DropRole removes a login role, but refuses if that would strand a database it
// solely owns; a database must always keep at least one managed role. When
// another role remains for that database, ownership is handed to it.
func (m *Module) DropRole(ctx context.Context, roleID string, actor *string) (jobs.Job, error) {
	role, err := m.getRoleModel(ctx, roleID)
	if err != nil {
		return jobs.Job{}, errors.New("role not found")
	}
	if err := ensureRemovable(Status(role.Status)); err != nil {
		return jobs.Job{}, err
	}
	if err := m.guardRoleRemovable(ctx, role.ID, role.Name); err != nil {
		return jobs.Job{}, err
	}
	return m.beginDrop(ctx, resourceRole, role.ID, postgresoperator.ActionDropRole, actor)
}

// DropGrant revokes one role's access to one database, leaving both in place.
func (m *Module) DropGrant(ctx context.Context, grantID string, actor *string) (jobs.Job, error) {
	grant, err := m.getGrantModel(ctx, grantID)
	if err != nil {
		return jobs.Job{}, errors.New("grant not found")
	}
	if err := ensureRemovable(Status(grant.Status)); err != nil {
		return jobs.Job{}, err
	}
	return m.beginDrop(ctx, resourceGrant, grant.ID, postgresoperator.ActionRevokeGrant, actor)
}

// beginDrop transitions a resource into planning and submits the drop plan.
func (m *Module) beginDrop(ctx context.Context, resourceType, resourceID string, action postgresoperator.Action, actor *string) (jobs.Job, error) {
	if _, err := m.setResourceStatus(ctx, resourceType, resourceID, StatusPlanning, nil); err != nil {
		return jobs.Job{}, err
	}
	job, err := m.submitPlan(ctx, resourceType, resourceID, action, actor)
	if err != nil {
		m.failResource(ctx, resourceType, resourceID, err)
		return jobs.Job{}, err
	}
	if _, err := m.attachJob(ctx, resourceType, resourceID, job.ID, StatusPlanning); err != nil {
		return jobs.Job{}, err
	}
	return job, nil
}

// ensureRemovable rejects a drop while the resource is mid-operation.
func ensureRemovable(status Status) error {
	switch status {
	case StatusActive, StatusFailed, StatusPlanReady:
		return nil
	default:
		return errors.New("resource is busy; wait for the current operation to finish")
	}
}

// guardRoleRemovable enforces the last-role rule: a role that solely owns a
// database cannot be deleted, because the database would be left with no managed
// role. A database with another grantee is fine — that role inherits it.
func (m *Module) guardRoleRemovable(ctx context.Context, roleID, roleName string) error {
	owned := []databaseModel{}
	if err := m.database.NewSelect().Model(&owned).Where("owner_role_id = ?", roleID).Scan(ctx); err != nil {
		return err
	}
	for _, db := range owned {
		count, err := m.database.NewSelect().Model((*grantModel)(nil)).Where("database_id = ?", db.ID).Where("role_id != ?", roleID).Count(ctx)
		if err != nil {
			return err
		}
		if count == 0 {
			return fmt.Errorf("cannot delete %s: it is the only role of database %s — delete the database or add another role first", roleName, db.Name)
		}
	}
	return nil
}

// roleDatabasesFor enumerates every database a role about to be dropped touches:
// those it owns (with the role that will inherit them) and those it only has a
// grant on. The operator uses this to clear the role before DROP ROLE.
func (m *Module) roleDatabasesFor(ctx context.Context, roleID string) ([]postgresoperator.RoleDatabase, error) {
	result := []postgresoperator.RoleDatabase{}
	seen := map[string]bool{}
	owned := []databaseModel{}
	if err := m.database.NewSelect().Model(&owned).Where("owner_role_id = ?", roleID).Scan(ctx); err != nil {
		return nil, err
	}
	for _, db := range owned {
		newOwner, err := m.replacementRoleName(ctx, db, roleID)
		if err != nil {
			return nil, err
		}
		result = append(result, postgresoperator.RoleDatabase{Name: db.Name, NewOwner: newOwner})
		seen[db.ID] = true
	}
	grants := []grantModel{}
	if err := m.database.NewSelect().Model(&grants).Where("role_id = ?", roleID).Scan(ctx); err != nil {
		return nil, err
	}
	for _, grant := range grants {
		if seen[grant.DatabaseID] {
			continue
		}
		db, err := m.getDatabaseModel(ctx, grant.DatabaseID)
		if err != nil {
			continue
		}
		result = append(result, postgresoperator.RoleDatabase{Name: db.Name})
		seen[grant.DatabaseID] = true
	}
	return result, nil
}

// replacementRoleName returns another role granted on the database, which will
// inherit ownership. The guard has already ensured one exists.
func (m *Module) replacementRoleName(ctx context.Context, db databaseModel, roleID string) (string, error) {
	grants := []grantModel{}
	if err := m.database.NewSelect().Model(&grants).Where("database_id = ?", db.ID).Where("role_id != ?", roleID).Limit(1).Scan(ctx); err != nil {
		return "", err
	}
	if len(grants) == 0 {
		return "", fmt.Errorf("database %s has no other role to take ownership", db.Name)
	}
	replacement, err := m.getRoleModel(ctx, grants[0].RoleID)
	if err != nil {
		return "", err
	}
	return replacement.Name, nil
}

// commitDrop removes the managed rows once the host has applied a drop. For a
// role it first mirrors the ownership transfers the operator performed, so the
// managed owner pointers match the engine.
func (m *Module) commitDrop(ctx context.Context, stored StoredPlan) error {
	now := m.now().UTC()
	switch stored.ResourceType {
	case resourceDatabase:
		if _, err := m.database.NewDelete().Model((*grantModel)(nil)).Where("database_id = ?", stored.ResourceID).Exec(ctx); err != nil {
			return err
		}
		if _, err := m.database.NewDelete().Model((*restorePointModel)(nil)).Where("database_id = ?", stored.ResourceID).Exec(ctx); err != nil {
			return err
		}
		_, err := m.database.NewDelete().Model((*databaseModel)(nil)).Where("id = ?", stored.ResourceID).Exec(ctx)
		return err
	case resourceRole:
		role, err := m.getRoleModel(ctx, stored.ResourceID)
		if err != nil {
			return err
		}
		for _, item := range stored.AgentPlan.Change.RoleDatabases {
			if item.NewOwner == "" {
				continue
			}
			owner := new(roleModel)
			if err := m.database.NewSelect().Model(owner).Where("instance_id = ?", role.InstanceID).Where("name = ?", item.NewOwner).Limit(1).Scan(ctx); err != nil {
				return err
			}
			if _, err := m.database.NewUpdate().Model((*databaseModel)(nil)).Set("owner_role_id = ?", owner.ID).Set("updated_at = ?", now).Where("instance_id = ?", role.InstanceID).Where("name = ?", item.Name).Exec(ctx); err != nil {
				return err
			}
		}
		if _, err := m.database.NewDelete().Model((*grantModel)(nil)).Where("role_id = ?", stored.ResourceID).Exec(ctx); err != nil {
			return err
		}
		_, err = m.database.NewDelete().Model((*roleModel)(nil)).Where("id = ?", stored.ResourceID).Exec(ctx)
		return err
	case resourceGrant:
		_, err := m.database.NewDelete().Model((*grantModel)(nil)).Where("id = ?", stored.ResourceID).Exec(ctx)
		return err
	default:
		return errors.New("PostgreSQL drop resource is unsupported")
	}
}
