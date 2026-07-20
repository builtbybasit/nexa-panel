package nodeoperations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/nexa-panel/nexa-panel/internal/platform/httpapi"
	"github.com/nexa-panel/nexa-panel/internal/platform/identity"
	"github.com/nexa-panel/nexa-panel/internal/platform/jobs"
	"github.com/nexa-panel/nexa-panel/internal/platform/module"
	nodeoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/nodes"
)

type Module struct {
	operator nodeoperator.Operator
	jobs     *jobs.Module
}

func New(operator nodeoperator.Operator, jobQueue *jobs.Module) (*Module, error) {
	if operator == nil || jobQueue == nil {
		return nil, errors.New("node operator and durable job queue are required")
	}
	m := &Module{operator: operator, jobs: jobQueue}
	if err := jobQueue.RegisterHandler("node.probe.apply", m.applyJob); err != nil {
		return nil, err
	}
	if err := jobQueue.RegisterHandler("node.probe.rollback", m.rollbackJob); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *Module) Descriptor() module.Descriptor {
	return module.Descriptor{
		ID: "nodeoperations", Name: "Node Operations", Version: "0.1.0",
		Description:  "Typed plan, apply, observe, and rollback operations through the privileged node agent.",
		Dependencies: []string{"identity", "jobs"}, EstimatedIdleBytes: 1024 * 1024,
	}
}

func (m *Module) Register(registry module.Registry) error {
	routes := []struct {
		pattern    string
		permission string
		handler    http.Handler
	}{
		{"POST /api/v1/node/probe/plan", "operations.plan", http.HandlerFunc(m.planHTTP)},
		{"POST /api/v1/node/probe/apply", "operations.apply", http.HandlerFunc(m.applyHTTP)},
		{"GET /api/v1/node/probe/observe", "system.read", http.HandlerFunc(m.observeHTTP)},
		{"POST /api/v1/node/probe/rollback", "operations.apply", http.HandlerFunc(m.rollbackHTTP)},
	}
	for _, route := range routes {
		if err := registry.HandleAuthorized(route.pattern, route.permission, route.handler); err != nil {
			return err
		}
	}
	return nil
}

func (m *Module) planHTTP(w http.ResponseWriter, r *http.Request) {
	var change nodeoperator.Change
	if err := decodeJSON(w, r, &change); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	plan, err := m.operator.Plan(r.Context(), change)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "plan_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, plan)
}

func (m *Module) applyHTTP(w http.ResponseWriter, r *http.Request) {
	m.submitPlanJob(w, r, "node.probe.apply")
}

func (m *Module) rollbackHTTP(w http.ResponseWriter, r *http.Request) {
	m.submitPlanJob(w, r, "node.probe.rollback")
}

func (m *Module) submitPlanJob(w http.ResponseWriter, r *http.Request, kind string) {
	var plan nodeoperator.Plan
	if err := decodeJSON(w, r, &plan); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if plan.Kind != nodeoperator.ProbeKind || plan.ID == "" || plan.Signature == "" {
		writeError(w, http.StatusUnprocessableEntity, "invalid_plan", "A valid probe operation plan is required.")
		return
	}
	user, ok := identity.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication_required", "Sign in to continue.")
		return
	}
	job, err := m.jobs.Submit(r.Context(), kind, plan, &user.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "job_submission_failed", "The operation could not be queued.")
		return
	}
	w.Header().Set("Location", fmt.Sprintf("/api/v1/jobs/%d", job.ID))
	writeJSON(w, http.StatusAccepted, job)
}

func (m *Module) observeHTTP(w http.ResponseWriter, r *http.Request) {
	observation, err := m.operator.Observe(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "observation_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, observation)
}

func (m *Module) applyJob(ctx context.Context, request json.RawMessage, report func(int, string) error) (any, error) {
	var plan nodeoperator.Plan
	if err := json.Unmarshal(request, &plan); err != nil {
		return nil, errors.New("invalid persisted operation plan")
	}
	if err := report(20, "Validating the approved node plan."); err != nil {
		return nil, err
	}
	observation, err := m.operator.Apply(ctx, plan)
	if err != nil {
		return nil, err
	}
	if err := report(75, "Observing the managed node state."); err != nil {
		return nil, err
	}
	verified, err := m.operator.Observe(ctx)
	if err != nil || verified.Exists != plan.Desired.Exists || verified.Digest != plan.Desired.Digest {
		_, rollbackErr := m.operator.Rollback(context.WithoutCancel(ctx), plan)
		if rollbackErr != nil {
			return nil, fmt.Errorf("verification failed and rollback failed: %v", rollbackErr)
		}
		return nil, errors.New("verification failed; the operation was rolled back")
	}
	if err := report(95, "Node state verified."); err != nil {
		return nil, err
	}
	return map[string]any{"planId": plan.ID, "observation": observation}, nil
}

func (m *Module) rollbackJob(ctx context.Context, request json.RawMessage, report func(int, string) error) (any, error) {
	var plan nodeoperator.Plan
	if err := json.Unmarshal(request, &plan); err != nil {
		return nil, errors.New("invalid persisted rollback plan")
	}
	if err := report(30, "Validating rollback safety."); err != nil {
		return nil, err
	}
	observation, err := m.operator.Rollback(ctx, plan)
	if err != nil {
		return nil, err
	}
	if err := report(90, "Rollback state verified."); err != nil {
		return nil, err
	}
	return map[string]any{"planId": plan.ID, "observation": observation}, nil
}

var (
	decodeJSON = httpapi.DecodeJSON
	writeJSON  = httpapi.WriteJSON
	writeError = httpapi.WriteError
)
