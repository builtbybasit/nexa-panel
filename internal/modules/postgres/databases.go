package postgres

import (
	"context"

	"errors"

	"strings"

	"github.com/nexa-panel/nexa-panel/internal/platform/jobs"

	postgresoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/postgres"
)

func (m *Module) ListDatabases(ctx context.Context, instanceID string) ([]Database, error) {
	models := []databaseModel{}
	query := m.database.NewSelect().Model(&models).OrderExpr("name ASC")
	if instanceID != "" {
		query = query.Where("instance_id = ?", instanceID)
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
	if _, err := m.activeInstance(ctx, request.InstanceID); err != nil {
		return Database{}, jobs.Job{}, err
	}
	owner, err := m.getRoleModel(ctx, request.OwnerRoleID)
	if err != nil || owner.InstanceID != request.InstanceID || Status(owner.Status) != StatusActive {
		return Database{}, jobs.Job{}, errors.New("owner role must be active on the selected instance")
	}
	now := m.now().UTC()
	model := &databaseModel{ID: randomResourceID("database"), InstanceID: request.InstanceID, Name: request.Name, OwnerRoleID: owner.ID, Status: string(StatusPlanning), CreatedAt: now, UpdatedAt: now}
	if _, err := m.database.NewInsert().Model(model).Exec(ctx); err != nil {
		return Database{}, jobs.Job{}, friendlyUnique(err, "database name is already managed on this instance")
	}
	job, err := m.submitPlan(ctx, resourceDatabase, model.ID, postgresoperator.ActionCreateDatabase, actor)
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
