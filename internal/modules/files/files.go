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
	"github.com/nexa-panel/nexa-panel/internal/platform/webhandler"
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
	audit    audit.Sink
}

func New(queue *jobs.Module, catalog SiteCatalog, access AccessPolicy, operator filesoperator.Operator, recorder audit.Recorder) (*Module, error) {
	if queue == nil || catalog == nil || access == nil || operator == nil || recorder == nil {
		return nil, errors.New("files jobs, site catalog, access policy, operator, and audit recorder are required")
	}
	m := &Module{jobs: queue, sites: catalog, access: access, operator: operator, audit: audit.NewSink(recorder, nil)}
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

// Register binds every handler to the route and permission its operationId
// declares in the OpenAPI contract. Method, path, and required permission come
// from the embedded spec (internal/platform/httpapi/apispec), so this map is the
// whole routing table and a renamed or missing operation fails startup instead
// of drifting from the published contract.
func (m *Module) Register(registry module.Registry) error {
	return webhandler.Register(registry, map[string]http.HandlerFunc{
		"listSiteFiles":            m.listHTTP,
		"statSiteFile":             m.statHTTP,
		"readSiteFileContent":      m.readHTTP,
		"writeSiteFileContent":     m.writeHTTP,
		"downloadSiteFile":         m.downloadHTTP,
		"createSiteDirectory":      m.mkdirHTTP,
		"moveSiteFile":             m.moveHTTP,
		"copySiteFile":             m.copyHTTP,
		"chmodSiteFile":            m.chmodHTTP,
		"deleteSiteFile":           m.deleteHTTP,
		"beginSiteFileUpload":      m.uploadBeginHTTP,
		"uploadSiteFileChunk":      m.uploadChunkHTTP,
		"commitSiteFileUpload":     m.uploadCommitHTTP,
		"abortSiteFileUpload":      m.uploadAbortHTTP,
		"archiveSiteFiles":         m.archiveHTTP,
		"extractSiteArchive":       m.extractHTTP,
		"measureSiteDirectorySize": m.sizeHTTP,
	})
}
