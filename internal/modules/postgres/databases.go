package postgres

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/platform/audit"
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
	m.jobs.Audit().RecordBestEffort(ctx, audit.Entry{
		ActorUserID: actor, Action: "postgresql.database_created", Subject: "postgresql-database:" + model.ID,
		Metadata: map[string]any{"name": model.Name, "instanceId": model.InstanceID, "ownerRoleId": owner.ID, "ownerRole": owner.Name},
	})
	return model.toDatabase(), job, err
}

// sizeRefreshInterval bounds how often a list request re-measures databases.
// pg_database_size itself is cheap, but the databases page polls this endpoint
// and every probe costs an agent round trip, so a served size may lag reality
// by up to this long. sizeObservedAt travels with the row so callers can say
// how fresh the number is rather than implying it is live.
const sizeRefreshInterval = time.Minute

// SyncDatabaseSizes re-measures databases whose size has gone stale and then
// reports the stored rows, following the SyncInstances read-path convention:
// observe the host, persist, return what was persisted.
//
// A probe failure is deliberately not fatal. An instance can be down, starting,
// or mid-restore while its databases still need to render, so a failed probe
// keeps the last known size and the list still serves. This is why it does not
// mirror instancesHTTP, which 503s when discovery fails.
func (m *Module) SyncDatabaseSizes(ctx context.Context, instanceID string) ([]Database, error) {
	models := []databaseModel{}
	query := m.database.NewSelect().Model(&models)
	if instanceID != "" {
		query = query.Where("instance_id = ?", instanceID)
	}
	if err := query.Scan(ctx); err != nil {
		return nil, err
	}
	now := m.now().UTC()
	for _, id := range staleInstanceIDs(models, now) {
		sizes, err := m.operator.Sizes(ctx, id)
		if err != nil {
			continue
		}
		if err := m.storeDatabaseSizes(ctx, models, id, sizes, now); err != nil {
			return nil, err
		}
	}
	return m.ListDatabases(ctx, instanceID)
}

// staleInstanceIDs reports which instances are worth probing: one probe covers
// every database on an instance, so a page showing twenty databases across two
// instances costs two queries, not twenty.
func staleInstanceIDs(models []databaseModel, now time.Time) []string {
	ids := []string{}
	seen := map[string]struct{}{}
	for _, model := range models {
		if Status(model.Status) != StatusActive {
			continue
		}
		if model.SizeObservedAt != nil && now.Sub(*model.SizeObservedAt) < sizeRefreshInterval {
			continue
		}
		if _, exists := seen[model.InstanceID]; exists {
			continue
		}
		seen[model.InstanceID] = struct{}{}
		ids = append(ids, model.InstanceID)
	}
	return ids
}

func (m *Module) storeDatabaseSizes(ctx context.Context, models []databaseModel, instanceID string, sizes map[string]int64, now time.Time) error {
	for _, model := range models {
		if model.InstanceID != instanceID || Status(model.Status) != StatusActive {
			continue
		}
		// A managed row with no matching database on the host was dropped out
		// of band; leave its last known size rather than recording a zero.
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

// BackupTarget resolves a managed database to the connection details a logical
// backup needs: its name and the owning instance's binary series, port, and
// socket directory. Used by the backups module to dump a plan's databases.
func (m *Module) BackupTarget(ctx context.Context, databaseID string) (name, version string, port int, socket string, err error) {
	database, err := m.getDatabaseModel(ctx, databaseID)
	if err != nil {
		return "", "", 0, "", err
	}
	instance, err := m.getInstanceModel(ctx, database.InstanceID)
	if err != nil {
		return "", "", 0, "", err
	}
	return database.Name, instance.Version, instance.Port, instance.SocketPath, nil
}
