package mysql

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/platform/audit"
	"github.com/nexa-panel/nexa-panel/internal/platform/jobs"
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
	m.jobs.Audit().RecordBestEffort(ctx, audit.Entry{
		ActorUserID: actor, Action: "mysql.database_created", Subject: "mysql-database:" + model.ID,
		Metadata: map[string]any{"name": model.Name, "engineId": model.EngineID, "ownerAccountId": owner.ID, "ownerAccount": owner.Name},
	})
	return model.toDatabase(), job, err
}

// sizeRefreshInterval bounds how often a list request re-measures databases.
// The information_schema aggregate is not free on an engine with many tables
// and the databases page polls this endpoint, so a served size may lag reality
// by up to this long. sizeObservedAt travels with the row so callers can say
// how fresh the number is rather than implying it is live.
const sizeRefreshInterval = time.Minute

// SyncDatabaseSizes re-measures databases whose size has gone stale and then
// reports the stored rows, following the SyncEngines read-path convention:
// observe the host, persist, return what was persisted.
//
// A probe failure is deliberately not fatal: the engine can be down or
// restarting while its databases still need to render, so a failed probe keeps
// the last known size and the list still serves.
func (m *Module) SyncDatabaseSizes(ctx context.Context, engineID string) ([]Database, error) {
	models := []databaseModel{}
	query := m.database.NewSelect().Model(&models)
	if engineID != "" {
		query = query.Where("engine_id = ?", engineID)
	}
	if err := query.Scan(ctx); err != nil {
		return nil, err
	}
	// One probe covers every schema on the node's single engine, so this is a
	// question of whether to probe at all rather than which engines to probe.
	if anyDatabaseSizeStale(models, m.now().UTC()) {
		if sizes, err := m.operator.Sizes(ctx); err == nil {
			if err := m.storeDatabaseSizes(ctx, models, sizes, m.now().UTC()); err != nil {
				return nil, err
			}
		}
	}
	return m.ListDatabases(ctx, engineID)
}

func anyDatabaseSizeStale(models []databaseModel, now time.Time) bool {
	for _, model := range models {
		if Status(model.Status) != StatusActive {
			continue
		}
		if model.SizeObservedAt == nil || now.Sub(*model.SizeObservedAt) >= sizeRefreshInterval {
			return true
		}
	}
	return false
}

func (m *Module) storeDatabaseSizes(ctx context.Context, models []databaseModel, sizes map[string]int64, now time.Time) error {
	for _, model := range models {
		if Status(model.Status) != StatusActive {
			continue
		}
		// A managed row with no matching schema on the host was dropped out of
		// band; leave its last known size rather than recording a zero.
		size, measured := sizes[model.Name]
		if !measured {
			continue
		}
		_, err := m.database.NewUpdate().Model((*databaseModel)(nil)).
			Set("size_bytes = ?", size).Set("size_observed_at = ?", now).
			Where("id = ?", model.ID).Exec(ctx)
		if err != nil {
			return err
		}
	}
	return nil
}

func (m *Module) getDatabaseModel(ctx context.Context, id string) (databaseModel, error) {
	var model databaseModel
	err := m.database.NewSelect().Model(&model).Where("id = ?", id).Scan(ctx)
	return model, err
}

// BackupTarget resolves a managed database to the details a logical backup
// needs: its name, the owning engine's kind ("mysql"/"mariadb"), and its socket
// path. Used by the backups module to dump a plan's databases.
func (m *Module) BackupTarget(ctx context.Context, databaseID string) (name, kind, socket string, err error) {
	database, err := m.getDatabaseModel(ctx, databaseID)
	if err != nil {
		return "", "", "", err
	}
	engine := new(engineModel)
	if err := m.database.NewSelect().Model(engine).Where("id = ?", database.EngineID).Scan(ctx); err != nil {
		return "", "", "", err
	}
	return database.Name, engine.Kind, engine.SocketPath, nil
}
