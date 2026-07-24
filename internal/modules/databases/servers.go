package databases

import (
	"context"

	"github.com/nexa-panel/nexa-panel/internal/platform/jobs"
)

// SyncServers observes every engine and returns the merged server list. A
// failed discovery degrades to that engine's stored rows rather than failing
// the whole listing: one engine being down must not blank the other's servers.
// The error is non-nil only when no engine could report anything at all.
func (m *Module) SyncServers(ctx context.Context) ([]Server, error) {
	items := []Server{}
	var firstErr error
	served := false
	for _, eng := range m.engines {
		servers, err := eng.adapter.SyncServers(ctx)
		if err != nil {
			servers, err = eng.adapter.ListServers(ctx)
		}
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		served = true
		items = append(items, servers...)
	}
	if !served && firstErr != nil {
		return nil, firstErr
	}
	return items, nil
}

// CreateServer provisions a new server instance on an engine that supports it
// and queues the single job that realizes it.
func (m *Module) CreateServer(ctx context.Context, request CreateServerRequest, actor *string) (Server, jobs.Job, error) {
	eng, err := m.engineByKey(request.Engine)
	if err != nil {
		return Server{}, jobs.Job{}, err
	}
	server, err := eng.adapter.CreateServer(ctx, request)
	if err != nil {
		return Server{}, jobs.Job{}, err
	}
	job, err := m.submitExecute(ctx, eng, resourceServer, server.ID, ActionProvisionServer, actor)
	if err != nil {
		m.failResource(ctx, eng, resourceServer, server.ID, err)
		return server, jobs.Job{}, err
	}
	server.LastJobID = &job.ID
	return server, job, nil
}
