package admintools

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/nexa-panel/nexa-panel/internal/platform/identity"
	"github.com/nexa-panel/nexa-panel/internal/platform/module"
	admintooloperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/admintools"
)

func (m *Module) registerHTTP(registry module.Registry) error {
	routes := []struct {
		pattern, permission string
		handler             http.Handler
	}{
		{"GET /api/v1/admin-tools", "databases.read", http.HandlerFunc(m.listHTTP)},
		{"POST /api/v1/admin-tools/{kind}/plan", "databases.write", http.HandlerFunc(m.changeHTTP)},
		{"GET /api/v1/admin-tools/{kind}/plan", "databases.read", http.HandlerFunc(m.planHTTP)},
		{"POST /api/v1/admin-tools/{kind}/apply", "operations.apply", http.HandlerFunc(m.applyHTTP)},
		{"POST /api/v1/admin-tools/{kind}/launch", "operations.apply", http.HandlerFunc(m.launchHTTP)},
	}
	for _, route := range routes {
		if err := registry.HandleAuthorized(route.pattern, route.permission, route.handler); err != nil {
			return err
		}
	}
	return registry.HandleAuthenticated("/tools/{kind}/{path...}", http.HandlerFunc(m.proxyHTTP))
}
func (m *Module) listHTTP(w http.ResponseWriter, r *http.Request) {
	items, err := m.Sync(r.Context())
	if err != nil {
		writeError(w, 503, "admin_tools_unavailable", "Admin tools could not be inspected.")
		return
	}
	writeJSON(w, 200, map[string]any{"items": items})
}
func (m *Module) changeHTTP(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Action admintooloperator.Action `json:"action"`
	}
	if decodeJSON(w, r, &request) != nil {
		writeError(w, 400, "invalid_request", "Request body must be valid JSON.")
		return
	}
	actor, ok := actorID(r)
	if !ok {
		writeError(w, 401, "authentication_required", "Sign in to continue.")
		return
	}
	tool, job, err := m.RequestChange(r.Context(), admintooloperator.Kind(r.PathValue("kind")), request.Action, actor)
	if err != nil {
		writeError(w, 422, "admin_tool_change_invalid", err.Error())
		return
	}
	writeJSON(w, 202, map[string]any{"tool": tool, "job": job})
}
func (m *Module) planHTTP(w http.ResponseWriter, r *http.Request) {
	plan, err := m.StoredPlan(r.Context(), admintooloperator.Kind(r.PathValue("kind")))
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, 404, "admin_tool_plan_not_found", "An admin tool plan is not ready.")
		return
	}
	if err != nil {
		writeError(w, 500, "admin_tool_plan_unavailable", "Admin tool plan could not be loaded.")
		return
	}
	writeJSON(w, 200, map[string]any{"plan": plan})
}
func (m *Module) applyHTTP(w http.ResponseWriter, r *http.Request) {
	actor, ok := actorID(r)
	if !ok {
		writeError(w, 401, "authentication_required", "Sign in to continue.")
		return
	}
	job, err := m.ApplyPlan(r.Context(), admintooloperator.Kind(r.PathValue("kind")), actor)
	if err != nil {
		writeError(w, 409, "admin_tool_plan_not_applicable", err.Error())
		return
	}
	writeJSON(w, 202, job)
}
func actorID(r *http.Request) (*string, bool) {
	user, ok := identity.UserFromContext(r.Context())
	if !ok {
		return nil, false
	}
	return &user.ID, true
}
func decodeJSON(w http.ResponseWriter, r *http.Request, target any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 16*1024)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("one object required")
	}
	return nil
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]string{"code": code, "message": message})
}
