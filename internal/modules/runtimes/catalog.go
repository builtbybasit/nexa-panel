package runtimes

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/nexa-panel/nexa-panel/internal/platform/httpapi"
	"github.com/nexa-panel/nexa-panel/internal/platform/module"
)

var phpVersionPattern = regexp.MustCompile(`^(?:7\.4|8\.[0-9]{1,2})$`)

type Runtime struct {
	Engine        string `json:"engine"`
	Version       string `json:"version"`
	Installed     bool   `json:"installed"`
	Enabled       bool   `json:"enabled"`
	SupportStatus string `json:"supportStatus"`
	PoolDirectory string `json:"-"`
}

type Discoverer interface {
	Discover(ctx context.Context) ([]Runtime, error)
}

type Module struct {
	discoverer Discoverer
}

func New(discoverer Discoverer) (*Module, error) {
	if discoverer == nil {
		return nil, errors.New("runtime discoverer is required")
	}
	return &Module{discoverer: discoverer}, nil
}

func (m *Module) Descriptor() module.Descriptor {
	return module.Descriptor{
		ID: "runtimes", Name: "Runtimes", Version: "0.1.0",
		Description:  "Installed and policy-allowed PHP-FPM runtime capabilities.",
		Dependencies: []string{"identity"}, EstimatedIdleBytes: 256 * 1024,
	}
}

func (m *Module) Register(registry module.Registry) error {
	return registry.HandleAuthorized("GET /api/v1/runtimes", "runtimes.read", http.HandlerFunc(m.listHTTP))
}

func (m *Module) List(ctx context.Context) ([]Runtime, error) {
	items, err := m.discoverer.Discover(ctx)
	if err != nil {
		return nil, err
	}
	filtered := make([]Runtime, 0, len(items))
	seen := make(map[string]struct{})
	for _, item := range items {
		if item.Engine != "php" || !phpVersionPattern.MatchString(item.Version) || !item.Installed || !item.Enabled {
			continue
		}
		if _, exists := seen[item.Version]; exists {
			continue
		}
		seen[item.Version] = struct{}{}
		filtered = append(filtered, item)
	}
	sort.Slice(filtered, func(i, j int) bool { return filtered[i].Version < filtered[j].Version })
	return filtered, nil
}

func (m *Module) Allowed(ctx context.Context, version string) (bool, error) {
	items, err := m.List(ctx)
	if err != nil {
		return false, err
	}
	for _, item := range items {
		if item.Version == version {
			return true, nil
		}
	}
	return false, nil
}

func (m *Module) listHTTP(w http.ResponseWriter, r *http.Request) {
	items, err := m.List(r.Context())
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"code": "runtime_discovery_failed", "message": "Installed PHP runtimes could not be discovered."})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

type FilesystemDiscoverer struct {
	PHPConfigRoot string
}

func (d FilesystemDiscoverer) Discover(_ context.Context) ([]Runtime, error) {
	root := d.PHPConfigRoot
	if root == "" {
		root = "/etc/php"
	}
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return []Runtime{}, nil
	}
	if err != nil {
		return nil, err
	}
	items := make([]Runtime, 0, len(entries))
	for _, entry := range entries {
		version := strings.TrimSpace(entry.Name())
		if !entry.IsDir() || !phpVersionPattern.MatchString(version) {
			continue
		}
		poolDirectory := filepath.Join(root, version, "fpm", "pool.d")
		info, err := os.Stat(poolDirectory)
		if err != nil || !info.IsDir() {
			continue
		}
		supportStatus := "allowed_by_policy"
		if version == "7.4" {
			supportStatus = "end_of_life_allowed"
		}
		items = append(items, Runtime{
			Engine: "php", Version: version, Installed: true, Enabled: true,
			SupportStatus: supportStatus, PoolDirectory: poolDirectory,
		})
	}
	return items, nil
}

var writeJSON = httpapi.WriteJSON
