package mysql

import (
	"context"
	"errors"
	"fmt"

	"github.com/nexa-panel/nexa-panel/internal/platform/jobs"
	mysqloperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/mysql"
)

// isDropOperation reports whether a stored plan removes a resource rather than
// creating or changing one, which decides whether commit deletes the row.
func isDropOperation(op mysqloperator.Action) bool {
	switch op {
	case mysqloperator.ActionDropDatabase, mysqloperator.ActionDropAccount, mysqloperator.ActionRevokeGrant:
		return true
	default:
		return false
	}
}

// DropDatabase removes a managed database. It moves through the same
// plan → review → apply path as creation; commit deletes the row once the host
// confirms the schema is gone.
func (m *Module) DropDatabase(ctx context.Context, databaseID string, actor *string) (jobs.Job, error) {
	database, err := m.getDatabaseModel(ctx, databaseID)
	if err != nil {
		return jobs.Job{}, errors.New("database not found")
	}
	if err := ensureRemovable(Status(database.Status)); err != nil {
		return jobs.Job{}, err
	}
	return m.beginDrop(ctx, resourceDatabase, database.ID, mysqloperator.ActionDropDatabase, actor)
}

// DropAccount removes a login account, but refuses if that would strand a
// database it solely owns; a database must always keep at least one managed
// user. When another user remains, ownership is handed to it at commit.
func (m *Module) DropAccount(ctx context.Context, accountID string, actor *string) (jobs.Job, error) {
	account, err := m.getAccountModel(ctx, accountID)
	if err != nil {
		return jobs.Job{}, errors.New("account not found")
	}
	if err := ensureRemovable(Status(account.Status)); err != nil {
		return jobs.Job{}, err
	}
	if err := m.guardAccountRemovable(ctx, account.ID, account.Name); err != nil {
		return jobs.Job{}, err
	}
	return m.beginDrop(ctx, resourceAccount, account.ID, mysqloperator.ActionDropAccount, actor)
}

// DropGrant revokes one account's access to one database, leaving both in place.
func (m *Module) DropGrant(ctx context.Context, grantID string, actor *string) (jobs.Job, error) {
	grant, err := m.getGrantModel(ctx, grantID)
	if err != nil {
		return jobs.Job{}, errors.New("grant not found")
	}
	if err := ensureRemovable(Status(grant.Status)); err != nil {
		return jobs.Job{}, err
	}
	return m.beginDrop(ctx, resourceGrant, grant.ID, mysqloperator.ActionRevokeGrant, actor)
}

// beginDrop transitions a resource into planning and submits the drop plan,
// mirroring the create-path submit sequence.
func (m *Module) beginDrop(ctx context.Context, resourceType, resourceID string, action mysqloperator.Action, actor *string) (jobs.Job, error) {
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

// ensureRemovable rejects a drop while the resource is mid-operation, so a delete
// cannot interleave with an in-flight plan or apply.
func ensureRemovable(status Status) error {
	switch status {
	case StatusActive, StatusFailed, StatusPlanReady:
		return nil
	default:
		return errors.New("resource is busy; wait for the current operation to finish")
	}
}

// guardAccountRemovable enforces the last-user rule: an account that solely owns
// a database cannot be deleted, because the database would be left with no
// managed user. A database with another grantee is fine — that user inherits it.
func (m *Module) guardAccountRemovable(ctx context.Context, accountID, accountName string) error {
	owned := []databaseModel{}
	if err := m.database.NewSelect().Model(&owned).Where("owner_account_id = ?", accountID).Scan(ctx); err != nil {
		return err
	}
	for _, db := range owned {
		count, err := m.database.NewSelect().Model((*grantModel)(nil)).Where("database_id = ?", db.ID).Where("account_id != ?", accountID).Count(ctx)
		if err != nil {
			return err
		}
		if count == 0 {
			return fmt.Errorf("cannot delete %s: it is the only user of database %s — delete the database or add another user first", accountName, db.Name)
		}
	}
	return nil
}

// commitDrop removes the managed rows once the host has applied a drop, along
// with the rows that depend on them.
func (m *Module) commitDrop(ctx context.Context, stored StoredPlan) error {
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
	case resourceAccount:
		if err := m.reassignOwnedDatabases(ctx, stored.ResourceID); err != nil {
			return err
		}
		if _, err := m.database.NewDelete().Model((*grantModel)(nil)).Where("account_id = ?", stored.ResourceID).Exec(ctx); err != nil {
			return err
		}
		_, err := m.database.NewDelete().Model((*accountModel)(nil)).Where("id = ?", stored.ResourceID).Exec(ctx)
		return err
	case resourceGrant:
		_, err := m.database.NewDelete().Model((*grantModel)(nil)).Where("id = ?", stored.ResourceID).Exec(ctx)
		return err
	default:
		return errors.New("MySQL-family drop resource is unsupported")
	}
}

// reassignOwnedDatabases points every database owned by the departing account at
// a remaining grantee, keeping the managed-owner invariant the guard promised.
func (m *Module) reassignOwnedDatabases(ctx context.Context, accountID string) error {
	owned := []databaseModel{}
	if err := m.database.NewSelect().Model(&owned).Where("owner_account_id = ?", accountID).Scan(ctx); err != nil {
		return err
	}
	for _, db := range owned {
		replacements := []grantModel{}
		if err := m.database.NewSelect().Model(&replacements).Where("database_id = ?", db.ID).Where("account_id != ?", accountID).Limit(1).Scan(ctx); err != nil {
			return err
		}
		if len(replacements) == 0 {
			return fmt.Errorf("database %s has no other user to take ownership", db.Name)
		}
		if _, err := m.database.NewUpdate().Model((*databaseModel)(nil)).Set("owner_account_id = ?", replacements[0].AccountID).Set("updated_at = ?", m.now().UTC()).Where("id = ?", db.ID).Exec(ctx); err != nil {
			return err
		}
	}
	return nil
}
