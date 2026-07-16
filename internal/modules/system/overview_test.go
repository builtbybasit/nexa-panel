package system

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nexa-panel/nexa-panel/internal/adapters/podman"
	"github.com/nexa-panel/nexa-panel/internal/platform/capacity"
)

type memoryReader struct {
	snapshot capacity.Snapshot
	err      error
}

func (r memoryReader) Read(context.Context) (capacity.Snapshot, error) {
	return r.snapshot, r.err
}

type containerInspector struct {
	status podman.Status
	err    error
}

func (i containerInspector) Inspect(context.Context) (podman.Status, error) {
	return i.status, i.err
}

func TestOverviewReportsCapacityAndPodman(t *testing.T) {
	feature := New(
		memoryReader{snapshot: capacity.Snapshot{
			Supported:      true,
			TotalBytes:     2 * 1024 * 1024 * 1024,
			AvailableBytes: 1024 * 1024 * 1024,
			Profile:        capacity.ProfileCompact,
		}},
		containerInspector{status: podman.Status{Available: true, Version: "6.0.1"}},
	)

	response := httptest.NewRecorder()
	feature.overview(response, httptest.NewRequest(http.MethodGet, "/api/v1/system/overview", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.Code)
	}
	var body overviewResponse
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Memory == nil || body.Memory.Profile != capacity.ProfileCompact {
		t.Fatalf("unexpected memory response: %+v", body.Memory)
	}
	if !body.Podman.Available {
		t.Fatal("Podman should be available")
	}
	if len(body.Warnings) != 0 {
		t.Fatalf("warnings = %v, want none", body.Warnings)
	}
}

func TestOverviewDegradesWhenCapabilitiesAreUnavailable(t *testing.T) {
	feature := New(
		memoryReader{err: errors.New("unsupported")},
		containerInspector{err: podman.ErrUnavailable},
	)

	response := httptest.NewRecorder()
	feature.overview(response, httptest.NewRequest(http.MethodGet, "/api/v1/system/overview", nil))

	var body overviewResponse
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.Warnings) != 2 {
		t.Fatalf("warnings = %v, want two warnings", body.Warnings)
	}
}
