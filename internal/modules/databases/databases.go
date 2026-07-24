package databases

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/platform/audit"
	"github.com/nexa-panel/nexa-panel/internal/platform/jobs"
)

// ListDatabases returns managed databases, optionally scoped to one server,
// merged across engines when unscoped.
func (m *Module) ListDatabases(ctx context.Context, serverID string) ([]Database, error) {
	if serverID != "" {
		eng, err := m.engineForServer(ctx, serverID)
		if err != nil {
			return nil, err
		}
		return m.listEngineDatabases(ctx, eng, serverID)
	}
	items := []Database{}
	for _, eng := range m.engines {
		databases, err := m.listEngineDatabases(ctx, eng, "")
		if err != nil {
			return nil, err
		}
		items = append(items, databases...)
	}
	sortByName(items, func(item Database) string { return item.Name })
	return items, nil
}

func (m *Module) listEngineDatabases(ctx context.Context, eng *engine, serverID string) ([]Database, error) {
	rows, err := eng.store.ListDatabases(ctx, serverID)
	if err != nil {
		return nil, err
	}
	items := make([]Database, 0, len(rows))
	for _, row := range rows {
		items = append(items, row.toDatabase(eng.spec.Engine))
	}
	return items, nil
}

func (m *Module) CreateDatabase(ctx context.Context, request CreateDatabaseRequest, actor *string) (Database, jobs.Job, error) {
	request.ServerID = strings.TrimSpace(request.ServerID)
	request.Name = strings.ToLower(strings.TrimSpace(request.Name))
	eng, err := m.engineForServer(ctx, request.ServerID)
	if err != nil {
		return Database{}, jobs.Job{}, err
	}
	if !resourceNamePattern.MatchString(request.Name) {
		return Database{}, jobs.Job{}, errors.New("database name must contain 2-63 lowercase letters, numbers, or underscores")
	}
	if _, err := m.activeServer(ctx, eng, request.ServerID); err != nil {
		return Database{}, jobs.Job{}, err
	}
	owner, err := eng.store.GetUser(ctx, request.OwnerUserID)
	if err != nil || owner.ServerID != request.ServerID || Status(owner.Status) != StatusActive {
		return Database{}, jobs.Job{}, errors.New("owner user must be active on the selected server")
	}
	site, err := m.owningSiteID(ctx, request.SiteID)
	if err != nil {
		return Database{}, jobs.Job{}, err
	}
	now := m.now().UTC()
	row := databaseRow{ID: randomResourceID("database"), ServerID: request.ServerID, Name: request.Name, OwnerUserID: owner.ID, SiteID: site, Status: string(StatusApplying), CreatedAt: now, UpdatedAt: now}
	if err := eng.store.InsertDatabase(ctx, row); err != nil {
		return Database{}, jobs.Job{}, friendlyUnique(err, "database name is already managed on this server")
	}
	job, err := m.submitExecute(ctx, eng, resourceDatabase, row.ID, ActionCreateDatabase, actor)
	if err != nil {
		m.failResource(ctx, eng, resourceDatabase, row.ID, err)
		return row.toDatabase(eng.spec.Engine), jobs.Job{}, err
	}
	row.LastJobID = &job.ID
	m.jobs.Audit().RecordBestEffort(ctx, audit.Entry{
		ActorUserID: actor, Action: "databases.database_created", Subject: "database:" + row.ID,
		Metadata: map[string]any{"engine": eng.spec.Engine, "name": row.Name, "serverId": row.ServerID, "ownerUserId": owner.ID, "ownerUser": owner.Name, "siteId": request.SiteID},
	})
	return row.toDatabase(eng.spec.Engine), job, nil
}

// owningSiteID resolves the optional site a database is being created for. The
// row is verified to exist here rather than left to the foreign key so the
// caller gets a request error instead of an opaque constraint failure, and the
// sites table is read directly because this module must not depend on the sites
// module (which reads these tables during teardown).
func (m *Module) owningSiteID(ctx context.Context, siteID string) (*string, error) {
	if strings.TrimSpace(siteID) == "" {
		return nil, nil
	}
	exists, err := m.database.NewSelect().TableExpr("sites").Where("id = ?", siteID).Count(ctx)
	if err != nil {
		return nil, err
	}
	if exists == 0 {
		return nil, errors.New("the site this database belongs to does not exist")
	}
	return &siteID, nil
}

// sizeRefreshInterval bounds how often a list request re-measures databases.
// The engine-side size aggregate is not free on a server with many tables and
// the databases page polls this endpoint, so a served size may lag reality by
// up to this long. sizeObservedAt travels with the row so callers can say how
// fresh the number is rather than implying it is live.
const sizeRefreshInterval = time.Minute

// SyncDatabaseSizes re-measures databases whose size has gone stale and then
// reports the stored rows: observe the host, persist, return what was
// persisted. One probe covers every database on a server, so a page showing
// twenty databases across two servers costs two probes, not twenty.
//
// A probe failure is deliberately not fatal: a server can be down or
// restarting while its databases still need to render, so a failed probe keeps
// the last known size and the list still serves.
func (m *Module) SyncDatabaseSizes(ctx context.Context, serverID string) ([]Database, error) {
	if serverID != "" {
		eng, err := m.engineForServer(ctx, serverID)
		if err != nil {
			return nil, err
		}
		if err := m.refreshEngineSizes(ctx, eng, serverID); err != nil {
			return nil, err
		}
		return m.listEngineDatabases(ctx, eng, serverID)
	}
	items := []Database{}
	for _, eng := range m.engines {
		if err := m.refreshEngineSizes(ctx, eng, ""); err != nil {
			return nil, err
		}
		databases, err := m.listEngineDatabases(ctx, eng, "")
		if err != nil {
			return nil, err
		}
		items = append(items, databases...)
	}
	sortByName(items, func(item Database) string { return item.Name })
	return items, nil
}

func (m *Module) refreshEngineSizes(ctx context.Context, eng *engine, serverID string) error {
	rows, err := eng.store.ListDatabases(ctx, serverID)
	if err != nil {
		return err
	}
	now := m.now().UTC()
	for _, staleServer := range staleServerIDs(rows, now) {
		sizes, err := eng.adapter.Sizes(ctx, staleServer)
		if err != nil {
			continue
		}
		for _, row := range rows {
			if row.ServerID != staleServer || Status(row.Status) != StatusActive {
				continue
			}
			// A managed row with no matching database on the host was dropped
			// out of band; leave its last known size rather than recording zero.
			size, measured := sizes[row.Name]
			if !measured {
				continue
			}
			if err := eng.store.SetDatabaseSize(ctx, row.ID, size, now); err != nil {
				return err
			}
		}
	}
	return nil
}

// staleServerIDs reports which servers are worth probing: those with at least
// one active database whose size has never been measured or has aged past the
// refresh interval.
func staleServerIDs(rows []databaseRow, now time.Time) []string {
	ids := []string{}
	seen := map[string]struct{}{}
	for _, row := range rows {
		if Status(row.Status) != StatusActive {
			continue
		}
		if row.SizeObservedAt != nil && now.Sub(*row.SizeObservedAt) < sizeRefreshInterval {
			continue
		}
		if _, exists := seen[row.ServerID]; exists {
			continue
		}
		seen[row.ServerID] = struct{}{}
		ids = append(ids, row.ServerID)
	}
	return ids
}

// BackupTarget resolves a managed database to the engine-neutral details a
// logical backup needs. Used by the backups module to dump a plan's databases.
func (m *Module) BackupTarget(ctx context.Context, databaseID string) (BackupTarget, error) {
	eng, err := m.engineForResource(ctx, resourceDatabase, databaseID)
	if err != nil {
		return BackupTarget{}, err
	}
	database, err := eng.store.GetDatabase(ctx, databaseID)
	if err != nil {
		return BackupTarget{}, err
	}
	server, err := eng.adapter.GetServer(ctx, database.ServerID)
	if err != nil {
		return BackupTarget{}, err
	}
	engineName := server.Kind
	if eng.spec.Engine == "postgresql" {
		// The backups operator predates the unified module and names the
		// PostgreSQL engine "postgres" in dump filenames and restore refs.
		engineName = "postgres"
	}
	return BackupTarget{Engine: engineName, Name: database.Name, Version: server.Version, Port: server.Port, Socket: server.SocketPath}, nil
}
