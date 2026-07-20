package postgres

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"github.com/nexa-panel/nexa-panel/internal/platform/httpapi"
	"github.com/nexa-panel/nexa-panel/internal/platform/identity"
	"github.com/nexa-panel/nexa-panel/internal/platform/module"
)

func (m *Module) registerHTTP(registry module.Registry) error {
	routes := []struct {
		pattern, permission string
		handler             http.Handler
	}{
		{"GET /api/v1/postgresql/instances", "databases.read", http.HandlerFunc(m.instancesHTTP)},
		{"POST /api/v1/postgresql/instances", "databases.write", http.HandlerFunc(m.createInstanceHTTP)},
		{"GET /api/v1/postgresql/roles", "databases.read", http.HandlerFunc(m.rolesHTTP)},
		{"POST /api/v1/postgresql/roles", "databases.write", http.HandlerFunc(m.createRoleHTTP)},
		{"POST /api/v1/postgresql/roles/{id}/rotate", "databases.write", http.HandlerFunc(m.rotateRoleHTTP)},
		{"POST /api/v1/postgresql/roles/{id}/credential", "operations.apply", http.HandlerFunc(m.credentialHTTP)},
		{"GET /api/v1/postgresql/databases", "databases.read", http.HandlerFunc(m.databasesHTTP)},
		{"POST /api/v1/postgresql/databases", "databases.write", http.HandlerFunc(m.createDatabaseHTTP)},
		{"GET /api/v1/postgresql/grants", "databases.read", http.HandlerFunc(m.grantsHTTP)},
		{"POST /api/v1/postgresql/grants", "databases.write", http.HandlerFunc(m.createGrantHTTP)},
		{"GET /api/v1/postgresql/restore-points", "databases.read", http.HandlerFunc(m.restorePointsHTTP)},
		{"POST /api/v1/postgresql/databases/{id}/backups", "databases.write", http.HandlerFunc(m.createBackupHTTP)},
		{"POST /api/v1/postgresql/restore-points/{id}/restore", "databases.write", http.HandlerFunc(m.prepareRestoreHTTP)},
		{"GET /api/v1/postgresql/{resourceType}/{id}/plan", "databases.read", http.HandlerFunc(m.planHTTP)},
		{"POST /api/v1/postgresql/{resourceType}/{id}/apply", "operations.apply", http.HandlerFunc(m.applyHTTP)},
	}
	for _, route := range routes {
		if err := registry.HandleAuthorized(route.pattern, route.permission, route.handler); err != nil {
			return err
		}
	}
	return nil
}

func (m *Module) instancesHTTP(w http.ResponseWriter, r *http.Request) {
	items, err := m.SyncInstances(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "postgresql_discovery_failed", "PostgreSQL instances could not be discovered.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (m *Module) createInstanceHTTP(w http.ResponseWriter, r *http.Request) {
	var request CreateInstanceRequest
	if decodeJSON(w, r, &request) != nil {
		writeError(w, 400, "invalid_request", "Request body must be valid JSON.")
		return
	}
	actor, ok := actorID(r)
	if !ok {
		writeError(w, 401, "authentication_required", "Sign in to continue.")
		return
	}
	instance, job, err := m.CreateInstance(r.Context(), request, actor)
	if err != nil {
		writeError(w, 422, "postgresql_instance_invalid", err.Error())
		return
	}
	writeJSON(w, 202, map[string]any{"instance": instance, "job": job})
}

func (m *Module) rolesHTTP(w http.ResponseWriter, r *http.Request) {
	items, err := m.ListRoles(r.Context(), r.URL.Query().Get("instanceId"))
	if err != nil {
		writeError(w, 500, "postgresql_roles_unavailable", "PostgreSQL roles could not be loaded.")
		return
	}
	writeJSON(w, 200, map[string]any{"items": items})
}

func (m *Module) createRoleHTTP(w http.ResponseWriter, r *http.Request) {
	var request CreateRoleRequest
	if decodeJSON(w, r, &request) != nil {
		writeError(w, 400, "invalid_request", "Request body must be valid JSON.")
		return
	}
	actor, ok := actorID(r)
	if !ok {
		writeError(w, 401, "authentication_required", "Sign in to continue.")
		return
	}
	role, job, err := m.CreateRole(r.Context(), request, actor)
	if err != nil {
		writeError(w, 422, "postgresql_role_invalid", err.Error())
		return
	}
	writeJSON(w, 202, map[string]any{"role": role, "job": job})
}

func (m *Module) rotateRoleHTTP(w http.ResponseWriter, r *http.Request) {
	actor, ok := actorID(r)
	if !ok {
		writeError(w, 401, "authentication_required", "Sign in to continue.")
		return
	}
	role, job, err := m.RotateRole(r.Context(), r.PathValue("id"), actor)
	if err != nil {
		writeError(w, 409, "postgresql_role_not_rotatable", err.Error())
		return
	}
	writeJSON(w, 202, map[string]any{"role": role, "job": job})
}

func (m *Module) credentialHTTP(w http.ResponseWriter, r *http.Request) {
	credential, err := m.RevealCredential(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, 409, "postgresql_credential_unavailable", err.Error())
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	writeJSON(w, 200, map[string]string{"credential": credential})
}

func (m *Module) databasesHTTP(w http.ResponseWriter, r *http.Request) {
	items, err := m.SyncDatabaseSizes(r.Context(), r.URL.Query().Get("instanceId"))
	if err != nil {
		writeError(w, 500, "postgresql_databases_unavailable", "PostgreSQL databases could not be loaded.")
		return
	}
	writeJSON(w, 200, map[string]any{"items": items})
}

func (m *Module) createDatabaseHTTP(w http.ResponseWriter, r *http.Request) {
	var request CreateDatabaseRequest
	if decodeJSON(w, r, &request) != nil {
		writeError(w, 400, "invalid_request", "Request body must be valid JSON.")
		return
	}
	actor, ok := actorID(r)
	if !ok {
		writeError(w, 401, "authentication_required", "Sign in to continue.")
		return
	}
	database, job, err := m.CreateDatabase(r.Context(), request, actor)
	if err != nil {
		writeError(w, 422, "postgresql_database_invalid", err.Error())
		return
	}
	writeJSON(w, 202, map[string]any{"database": database, "job": job})
}

func (m *Module) grantsHTTP(w http.ResponseWriter, r *http.Request) {
	items, err := m.ListGrants(r.Context(), r.URL.Query().Get("databaseId"))
	if err != nil {
		writeError(w, 500, "postgresql_grants_unavailable", "PostgreSQL grants could not be loaded.")
		return
	}
	writeJSON(w, 200, map[string]any{"items": items})
}

func (m *Module) createGrantHTTP(w http.ResponseWriter, r *http.Request) {
	var request CreateGrantRequest
	if decodeJSON(w, r, &request) != nil {
		writeError(w, 400, "invalid_request", "Request body must be valid JSON.")
		return
	}
	actor, ok := actorID(r)
	if !ok {
		writeError(w, 401, "authentication_required", "Sign in to continue.")
		return
	}
	grant, job, err := m.CreateGrant(r.Context(), request, actor)
	if err != nil {
		writeError(w, 422, "postgresql_grant_invalid", err.Error())
		return
	}
	writeJSON(w, 202, map[string]any{"grant": grant, "job": job})
}

func (m *Module) restorePointsHTTP(w http.ResponseWriter, r *http.Request) {
	items, err := m.ListRestorePoints(r.Context(), r.URL.Query().Get("databaseId"))
	if err != nil {
		writeError(w, 500, "postgresql_restore_points_unavailable", "Restore points could not be loaded.")
		return
	}
	writeJSON(w, 200, map[string]any{"items": items})
}

func (m *Module) createBackupHTTP(w http.ResponseWriter, r *http.Request) {
	actor, ok := actorID(r)
	if !ok {
		writeError(w, 401, "authentication_required", "Sign in to continue.")
		return
	}
	point, job, err := m.CreateBackup(r.Context(), r.PathValue("id"), actor)
	if err != nil {
		writeError(w, 409, "postgresql_backup_invalid", err.Error())
		return
	}
	writeJSON(w, 202, map[string]any{"restorePoint": point, "job": job})
}

func (m *Module) prepareRestoreHTTP(w http.ResponseWriter, r *http.Request) {
	actor, ok := actorID(r)
	if !ok {
		writeError(w, 401, "authentication_required", "Sign in to continue.")
		return
	}
	point, job, err := m.PrepareRestore(r.Context(), r.PathValue("id"), actor)
	if err != nil {
		writeError(w, 409, "postgresql_restore_invalid", err.Error())
		return
	}
	writeJSON(w, 202, map[string]any{"restorePoint": point, "job": job})
}

func (m *Module) planHTTP(w http.ResponseWriter, r *http.Request) {
	resourceType, err := normalizeResourceType(r.PathValue("resourceType"))
	if err != nil {
		writeError(w, 404, "postgresql_resource_not_found", "PostgreSQL resource type does not exist.")
		return
	}
	plan, err := m.StoredPlan(r.Context(), resourceType, r.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, 404, "postgresql_plan_not_found", "A PostgreSQL plan is not ready.")
		return
	}
	if err != nil {
		writeError(w, 500, "postgresql_plan_unavailable", "PostgreSQL plan could not be loaded.")
		return
	}
	writeJSON(w, 200, map[string]any{"plan": plan, "expiresAt": plan.ExpiresAt})
}

func (m *Module) applyHTTP(w http.ResponseWriter, r *http.Request) {
	resourceType, err := normalizeResourceType(r.PathValue("resourceType"))
	if err != nil {
		writeError(w, 404, "postgresql_resource_not_found", "PostgreSQL resource type does not exist.")
		return
	}
	actor, ok := actorID(r)
	if !ok {
		writeError(w, 401, "authentication_required", "Sign in to continue.")
		return
	}
	job, err := m.ApplyPlan(r.Context(), resourceType, r.PathValue("id"), actor)
	if err != nil {
		writeError(w, 409, "postgresql_plan_not_applicable", err.Error())
		return
	}
	writeJSON(w, 202, job)
}

func normalizeResourceType(value string) (string, error) {
	switch strings.TrimSpace(value) {
	case "instances":
		return resourceInstance, nil
	case "roles":
		return resourceRole, nil
	case "databases":
		return resourceDatabase, nil
	case "grants":
		return resourceGrant, nil
	case "restore-points":
		return resourceRestorePoint, nil
	default:
		return "", errors.New("unsupported resource type")
	}
}

func actorID(r *http.Request) (*string, bool) {
	user, ok := identity.UserFromContext(r.Context())
	if !ok {
		return nil, false
	}
	return &user.ID, true
}

var (
	decodeJSON = httpapi.DecodeJSON
	writeJSON  = httpapi.WriteJSON
	writeError = httpapi.WriteError
)
