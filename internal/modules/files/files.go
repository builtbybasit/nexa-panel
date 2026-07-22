package files

import (
	"context"
	"errors"
	"net/http"

	"github.com/nexa-panel/nexa-panel/internal/modules/sites"
	"github.com/nexa-panel/nexa-panel/internal/platform/audit"
	"github.com/nexa-panel/nexa-panel/internal/platform/identity"
	"github.com/nexa-panel/nexa-panel/internal/platform/jobs"
	"github.com/nexa-panel/nexa-panel/internal/platform/module"
	filesoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/files"
)

type SiteCatalog interface {
	Get(ctx context.Context, id string) (sites.Site, error)
}

// AccessPolicy mirrors the sites module scoping; the identity module provides
// the concrete implementation.
type AccessPolicy interface {
	SiteAccessible(ctx context.Context, user identity.User, siteID string) (bool, error)
}

type Module struct {
	jobs     *jobs.Module
	sites    SiteCatalog
	access   AccessPolicy
	operator filesoperator.Operator
	audit    audit.Recorder
}

func New(queue *jobs.Module, catalog SiteCatalog, access AccessPolicy, operator filesoperator.Operator, recorder audit.Recorder) (*Module, error) {
	if queue == nil || catalog == nil || access == nil || operator == nil || recorder == nil {
		return nil, errors.New("files jobs, site catalog, access policy, operator, and audit recorder are required")
	}
	m := &Module{jobs: queue, sites: catalog, access: access, operator: operator, audit: recorder}
	if err := queue.RegisterHandler("files.archive", m.archiveJob); err != nil {
		return nil, err
	}
	if err := queue.RegisterHandler("files.extract", m.extractJob); err != nil {
		return nil, err
	}
	if err := queue.RegisterHandler("files.size", m.sizeJob); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *Module) Descriptor() module.Descriptor {
	return module.Descriptor{
		ID: "files", Name: "Files", Version: "0.1.0",
		Description:  "Site-confined file management brokered through the privileged node agent.",
		Dependencies: []string{"identity", "jobs", "sites"}, EstimatedIdleBytes: 256 * 1024,
	}
}

func (m *Module) Register(registry module.Registry) error {
	routes := []struct {
		pattern    string
		permission string
		handler    http.Handler
	}{
		{"GET /api/v1/sites/{id}/files", "files.read", http.HandlerFunc(m.listHTTP)},
		{"GET /api/v1/sites/{id}/files/stat", "files.read", http.HandlerFunc(m.statHTTP)},
		{"GET /api/v1/sites/{id}/files/content", "files.read", http.HandlerFunc(m.readHTTP)},
		{"PUT /api/v1/sites/{id}/files/content", "files.write", http.HandlerFunc(m.writeHTTP)},
		{"GET /api/v1/sites/{id}/files/download", "files.read", http.HandlerFunc(m.downloadHTTP)},
		{"POST /api/v1/sites/{id}/files/mkdir", "files.write", http.HandlerFunc(m.mkdirHTTP)},
		{"POST /api/v1/sites/{id}/files/move", "files.write", http.HandlerFunc(m.moveHTTP)},
		{"POST /api/v1/sites/{id}/files/copy", "files.write", http.HandlerFunc(m.copyHTTP)},
		{"POST /api/v1/sites/{id}/files/chmod", "files.write", http.HandlerFunc(m.chmodHTTP)},
		{"POST /api/v1/sites/{id}/files/delete", "files.write", http.HandlerFunc(m.deleteHTTP)},
		{"POST /api/v1/sites/{id}/files/uploads", "files.write", http.HandlerFunc(m.uploadBeginHTTP)},
		{"PUT /api/v1/sites/{id}/files/uploads/{uploadId}", "files.write", http.HandlerFunc(m.uploadChunkHTTP)},
		{"POST /api/v1/sites/{id}/files/uploads/{uploadId}/commit", "files.write", http.HandlerFunc(m.uploadCommitHTTP)},
		{"DELETE /api/v1/sites/{id}/files/uploads/{uploadId}", "files.write", http.HandlerFunc(m.uploadAbortHTTP)},
		{"POST /api/v1/sites/{id}/files/archive", "files.write", http.HandlerFunc(m.archiveHTTP)},
		{"POST /api/v1/sites/{id}/files/extract", "files.write", http.HandlerFunc(m.extractHTTP)},
		{"POST /api/v1/sites/{id}/files/size", "files.read", http.HandlerFunc(m.sizeHTTP)},
	}
	for _, route := range routes {
		if err := registry.HandleAuthorized(route.pattern, route.permission, route.handler); err != nil {
			return err
		}
	}
	return nil
}
