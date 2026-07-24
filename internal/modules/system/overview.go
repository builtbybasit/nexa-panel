package system

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/adapters/podman"
	"github.com/nexa-panel/nexa-panel/internal/platform/capacity"
	"github.com/nexa-panel/nexa-panel/internal/platform/module"
	"github.com/nexa-panel/nexa-panel/internal/platform/webhandler"
)

type podmanInspector interface {
	Inspect(ctx context.Context) (podman.Status, error)
}

type Module struct {
	memory capacity.Reader
	podman podmanInspector
	// updates is nil unless WithUpdates configured self-update support; when nil
	// the module serves only capacity/podman discovery and no update routes.
	updates *updates
	// initErr captures a job-handler registration failure from an option so it can
	// surface from Register, keeping New signature-compatible with its callers.
	initErr error
}

// Option configures optional system-module capabilities.
type Option func(*Module)

func New(memory capacity.Reader, containerRuntime podmanInspector, options ...Option) *Module {
	m := &Module{memory: memory, podman: containerRuntime}
	for _, option := range options {
		option(m)
	}
	return m
}

func (m *Module) Descriptor() module.Descriptor {
	descriptor := module.Descriptor{
		ID:                 "system",
		Name:               "System",
		Version:            "0.1.0",
		Description:        "Node capacity and platform capability discovery.",
		EstimatedIdleBytes: 2 * 1024 * 1024,
	}
	if m.updates != nil {
		descriptor.Description = "Node capacity, platform capability discovery, and panel self-update."
		descriptor.Dependencies = []string{"jobs"}
	}
	return descriptor
}

// Register binds every handler to the route and permission its operationId
// declares in the OpenAPI contract. Method, path, and required permission come
// from the embedded spec (internal/platform/httpapi/apispec), so this map is the
// whole routing table and a renamed or missing operation fails startup instead
// of drifting from the published contract. The self-update routes only appear
// when WithUpdates configured the feature.
func (m *Module) Register(registry module.Registry) error {
	if m.initErr != nil {
		return m.initErr
	}
	routes := map[string]http.HandlerFunc{
		"getSystemOverview": m.overview,
	}
	if m.updates != nil {
		routes["getSystemUpdates"] = m.availableUpdateHTTP
		routes["applySystemUpdate"] = m.applyUpdateHTTP
	}
	return webhandler.Register(registry, routes)
}

type overviewResponse struct {
	ObservedAt time.Time          `json:"observedAt"`
	Memory     *capacity.Snapshot `json:"memory,omitempty"`
	Podman     podman.Status      `json:"podman"`
	Warnings   []string           `json:"warnings"`
}

func (m *Module) overview(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	response := overviewResponse{ObservedAt: time.Now().UTC(), Warnings: make([]string, 0)}
	if memory, err := m.memory.Read(ctx); err != nil {
		response.Warnings = append(response.Warnings, "Memory information is unavailable on this development host.")
	} else {
		response.Memory = &memory
		if memory.Profile == capacity.ProfileUnsupported {
			response.Warnings = append(response.Warnings, "This node has less than the supported 2 GiB minimum.")
		}
	}

	containerRuntime, err := m.podman.Inspect(ctx)
	if err != nil {
		if !errors.Is(err, podman.ErrUnavailable) {
			response.Warnings = append(response.Warnings, "Podman was found but could not be inspected.")
		} else {
			response.Warnings = append(response.Warnings, "Podman is not installed; container-managed tools are unavailable.")
		}
	}
	response.Podman = containerRuntime

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(response)
}
