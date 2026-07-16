package postgres

import (
	"context"

	"errors"

	"path/filepath"

	"github.com/nexa-panel/nexa-panel/internal/platform/jobs"

	postgresoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/postgres"
)

func (m *Module) ListRestorePoints(ctx context.Context, databaseID string) ([]RestorePoint, error) {
	models := []restorePointModel{}
	query := m.database.NewSelect().Model(&models).OrderExpr("created_at DESC")
	if databaseID != "" {
		query = query.Where("database_id = ?", databaseID)
	}
	if err := query.Scan(ctx); err != nil {
		return nil, err
	}
	items := make([]RestorePoint, 0, len(models))
	for _, model := range models {
		items = append(items, model.toRestorePoint())
	}
	return items, nil
}

func (m *Module) CreateBackup(ctx context.Context, databaseID string, actor *string) (RestorePoint, jobs.Job, error) {
	database, err := m.getDatabaseModel(ctx, databaseID)
	if err != nil || Status(database.Status) != StatusActive {
		return RestorePoint{}, jobs.Job{}, errors.New("database must be active before backup")
	}
	id := randomResourceID("restore")
	now := m.now().UTC()
	path := filepath.Join(m.backupRoot, database.InstanceID, database.Name, id+".dump")
	model := &restorePointModel{ID: id, DatabaseID: database.ID, Status: string(StatusPlanning), Path: path, CreatedAt: now, UpdatedAt: now}
	if _, err := m.database.NewInsert().Model(model).Exec(ctx); err != nil {
		return RestorePoint{}, jobs.Job{}, err
	}
	job, err := m.submitPlan(ctx, resourceRestorePoint, id, postgresoperator.ActionCreateBackup, actor)
	if err != nil {
		m.failResource(ctx, resourceRestorePoint, id, err)
		return model.toRestorePoint(), jobs.Job{}, err
	}
	model.LastJobID = &job.ID
	_, err = m.attachJob(ctx, resourceRestorePoint, id, job.ID, StatusPlanning)
	return model.toRestorePoint(), job, err
}

func (m *Module) PrepareRestore(ctx context.Context, restorePointID string, actor *string) (RestorePoint, jobs.Job, error) {
	model, err := m.getRestorePointModel(ctx, restorePointID)
	if err != nil || Status(model.Status) != StatusVerified || model.SHA256 == nil || model.VerifiedAt == nil {
		return RestorePoint{}, jobs.Job{}, errors.New("only a verified restore point can be restored")
	}
	job, err := m.submitPlan(ctx, resourceRestorePoint, model.ID, postgresoperator.ActionRestoreBackup, actor)
	if err != nil {
		return model.toRestorePoint(), jobs.Job{}, err
	}
	model.LastJobID, model.Status, model.UpdatedAt = &job.ID, string(StatusPlanning), m.now().UTC()
	_, err = m.attachJob(ctx, resourceRestorePoint, model.ID, job.ID, StatusPlanning)
	return model.toRestorePoint(), job, err
}

func (m *Module) getRestorePointModel(ctx context.Context, id string) (restorePointModel, error) {
	var model restorePointModel
	err := m.database.NewSelect().Model(&model).Where("id = ?", id).Scan(ctx)
	return model, err
}
