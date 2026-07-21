package system

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/nexa-panel/nexa-panel/internal/platform/httpapi"
	"github.com/nexa-panel/nexa-panel/internal/platform/identity"
	"github.com/nexa-panel/nexa-panel/internal/platform/jobs"
	"github.com/nexa-panel/nexa-panel/internal/platform/module"
	selfupdateoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/selfupdate"
)

// updateJobKind is the durable job that performs a self-update through the
// privileged agent.
const updateJobKind = "system.update"

// updates holds the dependencies behind the self-update routes. It is only set
// when WithUpdates is supplied, so a control plane running without a self-update
// operator simply omits the feature.
type updates struct {
	jobs     *jobs.Module
	operator selfupdateoperator.Operator
}

// WithUpdates enables the panel self-update feature on the system module. It
// registers the durable "system.update" job on the shared queue; any
// registration error is deferred to Register so New keeps its simple signature.
func WithUpdates(queue *jobs.Module, operator selfupdateoperator.Operator) Option {
	return func(m *Module) {
		if queue == nil || operator == nil {
			m.initErr = errors.New("system updates require a jobs module and self-update operator")
			return
		}
		m.updates = &updates{jobs: queue, operator: operator}
		if err := queue.RegisterHandler(updateJobKind, m.applyUpdateJob); err != nil {
			m.initErr = err
		}
	}
}

func (m *Module) registerUpdates(registry module.Registry) error {
	if m.updates == nil {
		return nil
	}
	if err := registry.HandleAuthorized("GET /api/v1/system/updates", "system.read", http.HandlerFunc(m.availableUpdateHTTP)); err != nil {
		return err
	}
	return registry.HandleAuthorized("POST /api/v1/system/updates/apply", "system.update", http.HandlerFunc(m.applyUpdateHTTP))
}

// availableUpdateHTTP reports the installed version and the newest release the
// node could move to. The read gate is system.read; the sensitive step-up is
// reserved for the apply route.
func (m *Module) availableUpdateHTTP(w http.ResponseWriter, r *http.Request) {
	availability, err := m.updates.operator.Latest(r.Context())
	if err != nil {
		httpapi.WriteError(w, http.StatusServiceUnavailable, "system_update_check_failed", "The node could not be reached to check for updates.")
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, availability)
}

type applyUpdateRequest struct {
	// Version targets a specific release; empty applies the latest.
	Version string `json:"version"`
}

// applyUpdateHTTP enqueues a self-update job. The privileged download, swap, and
// restart all happen on the agent inside the job; the response is the 202 job so
// the UI can track it like any other operation.
func (m *Module) applyUpdateHTTP(w http.ResponseWriter, r *http.Request) {
	var request applyUpdateRequest
	if r.Body != nil && r.ContentLength != 0 {
		if httpapi.DecodeJSON(w, r, &request) != nil {
			httpapi.WriteError(w, http.StatusBadRequest, "invalid_request", "Request body must be valid JSON.")
			return
		}
	}
	user, ok := identity.UserFromContext(r.Context())
	if !ok {
		httpapi.WriteError(w, http.StatusUnauthorized, "authentication_required", "Sign in to continue.")
		return
	}
	title := "Update Nexa Panel"
	if request.Version != "" {
		title = "Update Nexa Panel to " + request.Version
	}
	job, err := m.updates.jobs.SubmitTitled(r.Context(), updateJobKind, title, selfupdateoperator.Change{Version: request.Version}, &user.ID)
	if err != nil {
		httpapi.WriteError(w, http.StatusUnprocessableEntity, "system_update_invalid", err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusAccepted, map[string]any{"job": job})
}

// applyUpdateJob proxies the apply to the privileged agent. The agent downloads,
// verifies, and swaps the binary, then arms a detached restart and returns
// before it fires — so this handler reports success and completes the job record
// while the panel is still running, moments before it bounces.
func (m *Module) applyUpdateJob(ctx context.Context, raw json.RawMessage, report func(progress int, message string) error) (any, error) {
	var change selfupdateoperator.Change
	if err := json.Unmarshal(raw, &change); err != nil {
		return nil, errors.New("invalid system update request")
	}
	_ = report(20, "Asking the node to download and verify the release.")
	result, err := m.updates.operator.Apply(ctx, change)
	if err != nil {
		return nil, err
	}
	_ = report(90, "Update installed; the panel will restart momentarily.")
	return result, nil
}
