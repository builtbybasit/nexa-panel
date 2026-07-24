package databases

import (
	"context"
	"database/sql"
	"errors"

	"github.com/nexa-panel/nexa-panel/internal/platform/jobs"
)

// ListGrants returns managed grants, scoped to one database when given,
// merged across engines otherwise.
func (m *Module) ListGrants(ctx context.Context, databaseID string) ([]Grant, error) {
	if databaseID != "" {
		eng, err := m.engineForResource(ctx, resourceDatabase, databaseID)
		if err != nil {
			return nil, err
		}
		return m.listEngineGrants(ctx, eng, databaseID)
	}
	items := []Grant{}
	for _, eng := range m.engines {
		grants, err := m.listEngineGrants(ctx, eng, "")
		if err != nil {
			return nil, err
		}
		items = append(items, grants...)
	}
	return items, nil
}

func (m *Module) listEngineGrants(ctx context.Context, eng *engine, databaseID string) ([]Grant, error) {
	rows, err := eng.store.ListGrants(ctx, databaseID)
	if err != nil {
		return nil, err
	}
	items := make([]Grant, 0, len(rows))
	for _, row := range rows {
		items = append(items, row.toGrant(eng.spec.Engine))
	}
	return items, nil
}

func (m *Module) CreateGrant(ctx context.Context, request CreateGrantRequest, actor *string) (Grant, jobs.Job, error) {
	eng, err := m.engineForResource(ctx, resourceDatabase, request.DatabaseID)
	if err != nil {
		return Grant{}, jobs.Job{}, errors.New("database must be active before assigning access")
	}
	database, err := eng.store.GetDatabase(ctx, request.DatabaseID)
	if err != nil || Status(database.Status) != StatusActive {
		return Grant{}, jobs.Job{}, errors.New("database must be active before assigning access")
	}
	user, err := eng.store.GetUser(ctx, request.UserID)
	if err != nil || Status(user.Status) != StatusActive || user.ServerID != database.ServerID {
		return Grant{}, jobs.Job{}, errors.New("user must be active on the database server")
	}
	if request.Access != AccessConnect && request.Access != AccessReadOnly && request.Access != AccessReadWrite {
		return Grant{}, jobs.Job{}, errors.New("access must be connect, read_only, or read_write")
	}
	now := m.now().UTC()
	row := grantRow{ID: randomResourceID("grant"), DatabaseID: database.ID, UserID: user.ID, Access: request.Access, Status: string(StatusApplying), CreatedAt: now, UpdatedAt: now}
	existing, findErr := eng.store.FindGrant(ctx, database.ID, user.ID)
	switch {
	case findErr == nil:
		row = existing
		row.Access, row.Status, row.Failure, row.UpdatedAt = request.Access, string(StatusApplying), nil, now
		if err := eng.store.ResetGrant(ctx, row.ID, request.Access); err != nil {
			return Grant{}, jobs.Job{}, err
		}
	case errors.Is(findErr, sql.ErrNoRows):
		if err := eng.store.InsertGrant(ctx, row); err != nil {
			return Grant{}, jobs.Job{}, friendlyUnique(err, "user already has managed access to this database")
		}
	default:
		return Grant{}, jobs.Job{}, findErr
	}
	job, err := m.submitExecute(ctx, eng, resourceGrant, row.ID, ActionApplyGrant, actor)
	if err != nil {
		m.failResource(ctx, eng, resourceGrant, row.ID, err)
		return row.toGrant(eng.spec.Engine), jobs.Job{}, err
	}
	row.LastJobID = &job.ID
	return row.toGrant(eng.spec.Engine), job, nil
}
