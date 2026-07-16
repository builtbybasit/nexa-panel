package mysql

import (
	"context"

	"github.com/nexa-panel/nexa-panel/internal/platform/jobs"

	"database/sql"
	mysqloperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/mysql"

	"errors"
)

func (m *Module) ListGrants(ctx context.Context, databaseID string) ([]Grant, error) {
	models := []grantModel{}
	query := m.database.NewSelect().Model(&models).OrderExpr("created_at ASC")
	if databaseID != "" {
		query = query.Where("database_id = ?", databaseID)
	}
	if err := query.Scan(ctx); err != nil {
		return nil, err
	}
	items := make([]Grant, 0, len(models))
	for _, model := range models {
		items = append(items, model.toGrant())
	}
	return items, nil
}

func (m *Module) CreateGrant(ctx context.Context, request CreateGrantRequest, actor *string) (Grant, jobs.Job, error) {
	database, err := m.getDatabaseModel(ctx, request.DatabaseID)
	if err != nil || Status(database.Status) != StatusActive {
		return Grant{}, jobs.Job{}, errors.New("database must be active before assigning access")
	}
	account, err := m.getAccountModel(ctx, request.AccountID)
	if err != nil || Status(account.Status) != StatusActive || account.EngineID != database.EngineID {
		return Grant{}, jobs.Job{}, errors.New("account must be active on the database engine")
	}
	if request.Access != mysqloperator.AccessConnect && request.Access != mysqloperator.AccessReadOnly && request.Access != mysqloperator.AccessReadWrite {
		return Grant{}, jobs.Job{}, errors.New("access must be connect, read_only, or read_write")
	}
	now := m.now().UTC()
	model := &grantModel{ID: randomResourceID("grant"), DatabaseID: database.ID, AccountID: account.ID, Access: string(request.Access), Status: string(StatusPlanning), CreatedAt: now, UpdatedAt: now}
	existing := new(grantModel)
	findErr := m.database.NewSelect().Model(existing).Where("database_id = ?", database.ID).Where("account_id = ?", account.ID).Scan(ctx)
	if findErr == nil {
		model = existing
		model.Access, model.Status, model.Failure, model.UpdatedAt = string(request.Access), string(StatusPlanning), nil, now
		if _, err := m.database.NewUpdate().Model(model).Column("access", "status", "failure", "updated_at").WherePK().Exec(ctx); err != nil {
			return Grant{}, jobs.Job{}, err
		}
	} else if errors.Is(findErr, sql.ErrNoRows) {
		if _, err := m.database.NewInsert().Model(model).Exec(ctx); err != nil {
			return Grant{}, jobs.Job{}, friendlyUnique(err, "account already has managed access to this database")
		}
	} else {
		return Grant{}, jobs.Job{}, findErr
	}
	job, err := m.submitPlan(ctx, resourceGrant, model.ID, mysqloperator.ActionApplyGrant, actor)
	if err != nil {
		m.failResource(ctx, resourceGrant, model.ID, err)
		return model.toGrant(), jobs.Job{}, err
	}
	model.LastJobID = &job.ID
	_, err = m.attachJob(ctx, resourceGrant, model.ID, job.ID, StatusPlanning)
	return model.toGrant(), job, err
}

func (m *Module) getGrantModel(ctx context.Context, id string) (grantModel, error) {
	var model grantModel
	err := m.database.NewSelect().Model(&model).Where("id = ?", id).Scan(ctx)
	return model, err
}
