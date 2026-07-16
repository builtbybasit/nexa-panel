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
)

type podmanInspector interface {
	Inspect(ctx context.Context) (podman.Status, error)
}

type Module struct {
	memory capacity.Reader
	podman podmanInspector
}

func New(memory capacity.Reader, containerRuntime podmanInspector) *Module {
	return &Module{memory: memory, podman: containerRuntime}
}

func (m *Module) Descriptor() module.Descriptor {
	return module.Descriptor{
		ID:                 "system",
		Name:               "System",
		Version:            "0.1.0",
		Description:        "Node capacity and platform capability discovery.",
		EstimatedIdleBytes: 2 * 1024 * 1024,
	}
}

func (m *Module) Register(registry module.Registry) error {
	return registry.HandleAuthorized("GET /api/v1/system/overview", "system.read", http.HandlerFunc(m.overview))
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
