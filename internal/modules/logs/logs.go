package logs

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/modules/sites"
	"github.com/nexa-panel/nexa-panel/internal/platform/identity"
	"github.com/nexa-panel/nexa-panel/internal/platform/module"
	logsoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/logs"
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
	sites    SiteCatalog
	access   AccessPolicy
	operator logsoperator.Operator

	// Live-tail cadence; tests shorten these to keep the SSE loop fast.
	streamPollInterval      time.Duration
	streamKeepAliveInterval time.Duration
	streamSessionLimit      time.Duration
}

func New(catalog SiteCatalog, access AccessPolicy, operator logsoperator.Operator) (*Module, error) {
	if catalog == nil || access == nil || operator == nil {
		return nil, errors.New("logs site catalog, access policy, and operator are required")
	}
	return &Module{
		sites: catalog, access: access, operator: operator,
		streamPollInterval:      1500 * time.Millisecond,
		streamKeepAliveInterval: 10 * time.Second,
		streamSessionLimit:      30 * time.Minute,
	}, nil
}

func (m *Module) Descriptor() module.Descriptor {
	return module.Descriptor{
		ID: "logs", Name: "Logs", Version: "0.1.0",
		Description:  "Site log discovery, bounded reads, and live tailing brokered through the privileged node agent.",
		Dependencies: []string{"identity", "sites"}, EstimatedIdleBytes: 128 * 1024,
	}
}

// Register binds every handler to the route and permission its operationId
// declares in the OpenAPI contract. Method, path, and required permission come
// from the embedded spec (internal/platform/httpapi/apispec), so this map is the
// whole routing table and a renamed or missing operation fails startup instead
// of drifting from the published contract.
func (m *Module) Register(registry module.Registry) error {
	return webhandler.Register(registry, map[string]http.HandlerFunc{
		"listSiteLogs":    m.listHTTP,
		"readSiteLog":     m.readHTTP,
		"streamSiteLog":   m.streamHTTP,
		"downloadSiteLog": m.downloadHTTP,
	})
}
