package databases

import (
	"context"
	"errors"
	"path/filepath"

	"github.com/nexa-panel/nexa-panel/internal/platform/audit"
	"github.com/nexa-panel/nexa-panel/internal/platform/jobs"
)

// ListRestorePoints returns restore points, scoped to one database when given,
// merged across engines otherwise.
func (m *Module) ListRestorePoints(ctx context.Context, databaseID string) ([]RestorePoint, error) {
	if databaseID != "" {
		eng, err := m.engineForResource(ctx, resourceDatabase, databaseID)
		if err != nil {
			return nil, err
		}
		return m.listEngineRestorePoints(ctx, eng, databaseID)
	}
	items := []RestorePoint{}
	for _, eng := range m.engines {
		points, err := m.listEngineRestorePoints(ctx, eng, "")
		if err != nil {
			return nil, err
		}
		items = append(items, points...)
	}
	return items, nil
}

func (m *Module) listEngineRestorePoints(ctx context.Context, eng *engine, databaseID string) ([]RestorePoint, error) {
	rows, err := eng.store.ListRestorePoints(ctx, databaseID)
	if err != nil {
		return nil, err
	}
	items := make([]RestorePoint, 0, len(rows))
	for _, row := range rows {
		items = append(items, row.toRestorePoint(eng.spec.Engine))
	}
	return items, nil
}

func (m *Module) CreateBackup(ctx context.Context, databaseID string, actor *string) (RestorePoint, jobs.Job, error) {
	eng, err := m.engineForResource(ctx, resourceDatabase, databaseID)
	if err != nil {
		return RestorePoint{}, jobs.Job{}, errors.New("database must be active before backup")
	}
	database, err := eng.store.GetDatabase(ctx, databaseID)
	if err != nil || Status(database.Status) != StatusActive {
		return RestorePoint{}, jobs.Job{}, errors.New("database must be active before backup")
	}
	id := randomResourceID("restore")
	now := m.now().UTC()
	path := filepath.Join(eng.spec.BackupRoot, database.ServerID, database.Name, id+".sql")
	row := restorePointRow{ID: id, DatabaseID: database.ID, Status: string(StatusBackingUp), Path: path, CreatedAt: now, UpdatedAt: now}
	if err := eng.store.InsertRestorePoint(ctx, row); err != nil {
		return RestorePoint{}, jobs.Job{}, err
	}
	job, err := m.submitExecute(ctx, eng, resourceRestorePoint, id, ActionCreateBackup, actor)
	if err != nil {
		m.failResource(ctx, eng, resourceRestorePoint, id, err)
		return row.toRestorePoint(eng.spec.Engine), jobs.Job{}, err
	}
	row.LastJobID = &job.ID
	return row.toRestorePoint(eng.spec.Engine), job, nil
}

// Restore replaces the database's contents from a verified restore point in
// one job — the panel's confirm prompt is the approval.
func (m *Module) Restore(ctx context.Context, restorePointID string, actor *string) (RestorePoint, jobs.Job, error) {
	eng, err := m.engineForResource(ctx, resourceRestorePoint, restorePointID)
	if err != nil {
		return RestorePoint{}, jobs.Job{}, err
	}
	row, err := eng.store.GetRestorePoint(ctx, restorePointID)
	if err != nil || Status(row.Status) != StatusVerified || row.SHA256 == nil || row.VerifiedAt == nil {
		return RestorePoint{}, jobs.Job{}, errors.New("only a verified restore point can be restored")
	}
	// A restore overwrites live data, so it is audited fail-closed at the
	// moment of human intent, exactly like a drop.
	if err := m.jobs.Audit().RecordSensitive(ctx, audit.Entry{
		ActorUserID: actor, Action: "databases.database_restored", Subject: "database-restore-point:" + row.ID,
		Metadata: map[string]any{"engine": eng.spec.Engine, "databaseId": row.DatabaseID, "restorePointId": row.ID},
	}); err != nil {
		return RestorePoint{}, jobs.Job{}, err
	}
	job, err := m.submitExecute(ctx, eng, resourceRestorePoint, row.ID, ActionRestoreBackup, actor)
	if err != nil {
		return row.toRestorePoint(eng.spec.Engine), jobs.Job{}, err
	}
	row.LastJobID, row.Status, row.UpdatedAt = &job.ID, string(StatusRestoring), m.now().UTC()
	return row.toRestorePoint(eng.spec.Engine), job, nil
}
