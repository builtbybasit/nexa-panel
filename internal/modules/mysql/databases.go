package mysql

import (
	"context"
	"strings"

	"github.com/nexa-panel/nexa-panel/internal/platform/jobs"

	"errors"
	mysqloperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/mysql"
)

func (m *Module) ListDatabases(ctx context.Context, engineID string) ([]Database, error) {
	models := []databaseModel{}
	query := m.database.NewSelect().Model(&models).OrderExpr("name ASC")
	if engineID != "" {
		query = query.Where("engine_id = ?", engineID)
	}
	if err := query.Scan(ctx); err != nil {
		return nil, err
	}
	items := make([]Database, 0, len(models))
	for _, model := range models {
		items = append(items, model.toDatabase())
	}
	return items, nil
}

func (m *Module) CreateDatabase(ctx context.Context, request CreateDatabaseRequest, actor *string) (Database, jobs.Job, error) {
	request.Name = strings.ToLower(strings.TrimSpace(request.Name))
	if !resourceNamePattern.MatchString(request.Name) {
		return Database{}, jobs.Job{}, errors.New("database name must contain 2-63 lowercase letters, numbers, or underscores")
	}
	if _, err := m.activeEngine(ctx, request.EngineID); err != nil {
		return Database{}, jobs.Job{}, err
	}
	owner, err := m.getAccountModel(ctx, request.OwnerAccountID)
	if err != nil || owner.EngineID != request.EngineID || Status(owner.Status) != StatusActive {
		return Database{}, jobs.Job{}, errors.New("owner account must be active on the selected engine")
	}
	now := m.now().UTC()
	model := &databaseModel{ID: randomResourceID("database"), EngineID: request.EngineID, Name: request.Name, OwnerAccountID: owner.ID, Status: string(StatusPlanning), CreatedAt: now, UpdatedAt: now}
	if _, err := m.database.NewInsert().Model(model).Exec(ctx); err != nil {
		return Database{}, jobs.Job{}, friendlyUnique(err, "database name is already managed on this engine")
	}
	job, err := m.submitPlan(ctx, resourceDatabase, model.ID, mysqloperator.ActionCreateDatabase, actor)
	if err != nil {
		m.failResource(ctx, resourceDatabase, model.ID, err)
		return model.toDatabase(), jobs.Job{}, err
	}
	model.LastJobID = &job.ID
	_, err = m.attachJob(ctx, resourceDatabase, model.ID, job.ID, StatusPlanning)
	return model.toDatabase(), job, err
}

func (m *Module) getDatabaseModel(ctx context.Context, id string) (databaseModel, error) {
	var model databaseModel
	err := m.database.NewSelect().Model(&model).Where("id = ?", id).Scan(ctx)
	return model, err
}
