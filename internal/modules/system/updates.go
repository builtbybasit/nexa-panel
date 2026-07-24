package system

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/nexa-panel/nexa-panel/internal/platform/httpapi"
	"github.com/nexa-panel/nexa-panel/internal/platform/identity"
	"github.com/nexa-panel/nexa-panel/internal/platform/jobs"
	selfupdateoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/selfupdate"
)

// updateJobKind is the durable job that performs a self-update through the
// privileged agent.
const updateJobKind = "system.update"

var errNoUpdateAvailable = errors.New("no panel update is available")

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
		if err := queue.RegisterHandlerWithOptions(updateJobKind, m.applyUpdateJob, jobs.HandlerOptions{RecoveryPolicy: jobs.RecoveryRetry}); err != nil {
			m.initErr = err
		}
	}
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
	change, err := m.resolveUpdateChange(r.Context(), request.Version)
	if err != nil {
		if errors.Is(err, errNoUpdateAvailable) {
			httpapi.WriteError(w, http.StatusConflict, "system_update_unavailable", "The node is already running the newest available release.")
			return
		}
		httpapi.WriteError(w, http.StatusServiceUnavailable, "system_update_check_failed", "The node could not resolve the requested release.")
		return
	}
	title := "Update Nexa Panel to " + change.Version
	job, err := m.updates.jobs.SubmitTitled(r.Context(), updateJobKind, title, change, &user.ID)
	if err != nil {
		httpapi.WriteError(w, http.StatusUnprocessableEntity, "system_update_invalid", err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusAccepted, map[string]any{"job": job})
}

// resolveUpdateChange turns the convenience "latest" request into an immutable
// version before it enters the durable queue. A restart retry must ask the agent
// about the same transaction, not silently advance to a newer release published
// between attempts.
func (m *Module) resolveUpdateChange(ctx context.Context, requestedVersion string) (selfupdateoperator.Change, error) {
	version := strings.TrimSpace(requestedVersion)
	if version != "" {
		return selfupdateoperator.Change{Version: version}, nil
	}
	availability, err := m.updates.operator.Latest(ctx)
	if err != nil {
		return selfupdateoperator.Change{}, fmt.Errorf("resolve latest panel release: %w", err)
	}
	if !availability.UpdateAvailable || availability.Latest == nil || strings.TrimSpace(availability.Latest.Version) == "" {
		return selfupdateoperator.Change{}, errNoUpdateAvailable
	}
	return selfupdateoperator.Change{Version: strings.TrimSpace(availability.Latest.Version)}, nil
}

// applyUpdateJob proxies the pinned release to the privileged agent. The first
// attempt is interrupted by the API restart; RecoveryRetry invokes it again and
// the agent's transaction journal then returns only the terminal verified result.
func (m *Module) applyUpdateJob(ctx context.Context, raw json.RawMessage, report func(progress int, message string) error) (any, error) {
	var change selfupdateoperator.Change
	if err := json.Unmarshal(raw, &change); err != nil {
		return nil, errors.New("invalid system update request")
	}
	if err := report(20, "Asking the node to apply and verify the release transaction."); err != nil {
		return nil, err
	}
	result, err := m.updates.operator.Apply(ctx, change)
	if err != nil {
		return nil, err
	}
	if err := report(90, "Update transaction reached a verified terminal state."); err != nil {
		return nil, err
	}
	return result, nil
}
