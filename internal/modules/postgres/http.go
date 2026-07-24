package postgres

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"github.com/nexa-panel/nexa-panel/internal/platform/httpapi"
	"github.com/nexa-panel/nexa-panel/internal/platform/module"
	"github.com/nexa-panel/nexa-panel/internal/platform/webhandler"
)

// registerHTTP binds every handler to the route and permission its operationId
// declares in the OpenAPI contract. Method, path, and required permission come
// from the embedded spec (internal/platform/httpapi/apispec), so this map is the
// whole routing table and a renamed or missing operation fails startup instead
// of drifting from the published contract.
func (m *Module) registerHTTP(registry module.Registry) error {
	return webhandler.Register(registry, map[string]http.HandlerFunc{
		"listPostgreSQLInstances":         m.instancesHTTP,
		"createPostgreSQLInstance":        m.createInstanceHTTP,
		"listPostgreSQLRoles":             m.rolesHTTP,
		"createPostgreSQLRole":            m.createRoleHTTP,
		"rotatePostgreSQLRole":            m.rotateRoleHTTP,
		"dropPostgreSQLRole":              m.dropRoleHTTP,
		"revealPostgreSQLCredentialOnce":  m.credentialHTTP,
		"listManagedPostgreSQLDatabases":  m.databasesHTTP,
		"createManagedPostgreSQLDatabase": m.createDatabaseHTTP,
		"dropManagedPostgreSQLDatabase":   m.dropDatabaseHTTP,
		"listPostgreSQLGrants":            m.grantsHTTP,
		"createPostgreSQLGrant":           m.createGrantHTTP,
		"dropPostgreSQLGrant":             m.dropGrantHTTP,
		"listPostgreSQLRestorePoints":     m.restorePointsHTTP,
		"createPostgreSQLRestorePoint":    m.createBackupHTTP,
		"preparePostgreSQLRestore":        m.prepareRestoreHTTP,
		"getPostgreSQLPlan":               m.planHTTP,
		"applyPostgreSQLPlan":             m.applyHTTP,
	})
}

func (m *Module) instancesHTTP(w http.ResponseWriter, r *http.Request) {
	items, err := m.SyncInstances(r.Context())
	if err != nil {
		httpapi.WriteError(w, http.StatusServiceUnavailable, "postgresql_discovery_failed", "PostgreSQL instances could not be discovered.")
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (m *Module) createInstanceHTTP(w http.ResponseWriter, r *http.Request) {
	var request CreateInstanceRequest
	if httpapi.DecodeJSON(w, r, &request) != nil {
		httpapi.WriteError(w, 400, "invalid_request", "Request body must be valid JSON.")
		return
	}
	actor, ok := webhandler.ActorID(r)
	if !ok {
		httpapi.WriteError(w, 401, "authentication_required", "Sign in to continue.")
		return
	}
	instance, job, err := m.CreateInstance(r.Context(), request, actor)
	if err != nil {
		httpapi.WriteError(w, 422, "postgresql_instance_invalid", err.Error())
		return
	}
	httpapi.WriteJSON(w, 202, map[string]any{"instance": instance, "job": job})
}

func (m *Module) rolesHTTP(w http.ResponseWriter, r *http.Request) {
	items, err := m.ListRoles(r.Context(), r.URL.Query().Get("instanceId"))
	if err != nil {
		httpapi.WriteError(w, 500, "postgresql_roles_unavailable", "PostgreSQL roles could not be loaded.")
		return
	}
	httpapi.WriteJSON(w, 200, map[string]any{"items": items})
}

func (m *Module) createRoleHTTP(w http.ResponseWriter, r *http.Request) {
	var request CreateRoleRequest
	if httpapi.DecodeJSON(w, r, &request) != nil {
		httpapi.WriteError(w, 400, "invalid_request", "Request body must be valid JSON.")
		return
	}
	actor, ok := webhandler.ActorID(r)
	if !ok {
		httpapi.WriteError(w, 401, "authentication_required", "Sign in to continue.")
		return
	}
	role, job, err := m.CreateRole(r.Context(), request, actor)
	if err != nil {
		httpapi.WriteError(w, 422, "postgresql_role_invalid", err.Error())
		return
	}
	httpapi.WriteJSON(w, 202, map[string]any{"role": role, "job": job})
}

func (m *Module) rotateRoleHTTP(w http.ResponseWriter, r *http.Request) {
	actor, ok := webhandler.ActorID(r)
	if !ok {
		httpapi.WriteError(w, 401, "authentication_required", "Sign in to continue.")
		return
	}
	role, job, err := m.RotateRole(r.Context(), r.PathValue("id"), actor)
	if err != nil {
		httpapi.WriteError(w, 409, "postgresql_role_not_rotatable", err.Error())
		return
	}
	httpapi.WriteJSON(w, 202, map[string]any{"role": role, "job": job})
}

func (m *Module) credentialHTTP(w http.ResponseWriter, r *http.Request) {
	credential, err := m.RevealCredential(r.Context(), r.PathValue("id"))
	if err != nil {
		httpapi.WriteError(w, 409, "postgresql_credential_unavailable", err.Error())
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	httpapi.WriteJSON(w, 200, map[string]string{"credential": credential})
}

func (m *Module) databasesHTTP(w http.ResponseWriter, r *http.Request) {
	items, err := m.SyncDatabaseSizes(r.Context(), r.URL.Query().Get("instanceId"))
	if err != nil {
		httpapi.WriteError(w, 500, "postgresql_databases_unavailable", "PostgreSQL databases could not be loaded.")
		return
	}
	httpapi.WriteJSON(w, 200, map[string]any{"items": items})
}

func (m *Module) dropRoleHTTP(w http.ResponseWriter, r *http.Request) {
	actor, ok := webhandler.ActorID(r)
	if !ok {
		httpapi.WriteError(w, 401, "authentication_required", "Sign in to continue.")
		return
	}
	job, err := m.DropRole(r.Context(), r.PathValue("id"), actor)
	if err != nil {
		httpapi.WriteError(w, 409, "postgresql_role_not_removable", err.Error())
		return
	}
	httpapi.WriteJSON(w, 202, map[string]any{"job": job})
}

func (m *Module) dropDatabaseHTTP(w http.ResponseWriter, r *http.Request) {
	actor, ok := webhandler.ActorID(r)
	if !ok {
		httpapi.WriteError(w, 401, "authentication_required", "Sign in to continue.")
		return
	}
	job, err := m.DropDatabase(r.Context(), r.PathValue("id"), actor)
	if err != nil {
		httpapi.WriteError(w, 409, "postgresql_database_not_removable", err.Error())
		return
	}
	httpapi.WriteJSON(w, 202, map[string]any{"job": job})
}

func (m *Module) dropGrantHTTP(w http.ResponseWriter, r *http.Request) {
	actor, ok := webhandler.ActorID(r)
	if !ok {
		httpapi.WriteError(w, 401, "authentication_required", "Sign in to continue.")
		return
	}
	job, err := m.DropGrant(r.Context(), r.PathValue("id"), actor)
	if err != nil {
		httpapi.WriteError(w, 409, "postgresql_grant_not_removable", err.Error())
		return
	}
	httpapi.WriteJSON(w, 202, map[string]any{"job": job})
}

func (m *Module) createDatabaseHTTP(w http.ResponseWriter, r *http.Request) {
	var request CreateDatabaseRequest
	if httpapi.DecodeJSON(w, r, &request) != nil {
		httpapi.WriteError(w, 400, "invalid_request", "Request body must be valid JSON.")
		return
	}
	actor, ok := webhandler.ActorID(r)
	if !ok {
		httpapi.WriteError(w, 401, "authentication_required", "Sign in to continue.")
		return
	}
	database, job, err := m.CreateDatabase(r.Context(), request, actor)
	if err != nil {
		httpapi.WriteError(w, 422, "postgresql_database_invalid", err.Error())
		return
	}
	httpapi.WriteJSON(w, 202, map[string]any{"database": database, "job": job})
}

func (m *Module) grantsHTTP(w http.ResponseWriter, r *http.Request) {
	items, err := m.ListGrants(r.Context(), r.URL.Query().Get("databaseId"))
	if err != nil {
		httpapi.WriteError(w, 500, "postgresql_grants_unavailable", "PostgreSQL grants could not be loaded.")
		return
	}
	httpapi.WriteJSON(w, 200, map[string]any{"items": items})
}

func (m *Module) createGrantHTTP(w http.ResponseWriter, r *http.Request) {
	var request CreateGrantRequest
	if httpapi.DecodeJSON(w, r, &request) != nil {
		httpapi.WriteError(w, 400, "invalid_request", "Request body must be valid JSON.")
		return
	}
	actor, ok := webhandler.ActorID(r)
	if !ok {
		httpapi.WriteError(w, 401, "authentication_required", "Sign in to continue.")
		return
	}
	grant, job, err := m.CreateGrant(r.Context(), request, actor)
	if err != nil {
		httpapi.WriteError(w, 422, "postgresql_grant_invalid", err.Error())
		return
	}
	httpapi.WriteJSON(w, 202, map[string]any{"grant": grant, "job": job})
}

func (m *Module) restorePointsHTTP(w http.ResponseWriter, r *http.Request) {
	items, err := m.ListRestorePoints(r.Context(), r.URL.Query().Get("databaseId"))
	if err != nil {
		httpapi.WriteError(w, 500, "postgresql_restore_points_unavailable", "Restore points could not be loaded.")
		return
	}
	httpapi.WriteJSON(w, 200, map[string]any{"items": items})
}

func (m *Module) createBackupHTTP(w http.ResponseWriter, r *http.Request) {
	actor, ok := webhandler.ActorID(r)
	if !ok {
		httpapi.WriteError(w, 401, "authentication_required", "Sign in to continue.")
		return
	}
	point, job, err := m.CreateBackup(r.Context(), r.PathValue("id"), actor)
	if err != nil {
		httpapi.WriteError(w, 409, "postgresql_backup_invalid", err.Error())
		return
	}
	httpapi.WriteJSON(w, 202, map[string]any{"restorePoint": point, "job": job})
}

func (m *Module) prepareRestoreHTTP(w http.ResponseWriter, r *http.Request) {
	actor, ok := webhandler.ActorID(r)
	if !ok {
		httpapi.WriteError(w, 401, "authentication_required", "Sign in to continue.")
		return
	}
	point, job, err := m.PrepareRestore(r.Context(), r.PathValue("id"), actor)
	if err != nil {
		httpapi.WriteError(w, 409, "postgresql_restore_invalid", err.Error())
		return
	}
	httpapi.WriteJSON(w, 202, map[string]any{"restorePoint": point, "job": job})
}

func (m *Module) planHTTP(w http.ResponseWriter, r *http.Request) {
	resourceType, err := normalizeResourceType(r.PathValue("resourceType"))
	if err != nil {
		httpapi.WriteError(w, 404, "postgresql_resource_not_found", "PostgreSQL resource type does not exist.")
		return
	}
	plan, err := m.StoredPlan(r.Context(), resourceType, r.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) {
		httpapi.WriteError(w, 404, "postgresql_plan_not_found", "A PostgreSQL plan is not ready.")
		return
	}
	if err != nil {
		httpapi.WriteError(w, 500, "postgresql_plan_unavailable", "PostgreSQL plan could not be loaded.")
		return
	}
	httpapi.WriteJSON(w, 200, map[string]any{"plan": plan, "expiresAt": plan.ExpiresAt})
}

func (m *Module) applyHTTP(w http.ResponseWriter, r *http.Request) {
	resourceType, err := normalizeResourceType(r.PathValue("resourceType"))
	if err != nil {
		httpapi.WriteError(w, 404, "postgresql_resource_not_found", "PostgreSQL resource type does not exist.")
		return
	}
	actor, ok := webhandler.ActorID(r)
	if !ok {
		httpapi.WriteError(w, 401, "authentication_required", "Sign in to continue.")
		return
	}
	job, err := m.ApplyPlan(r.Context(), resourceType, r.PathValue("id"), actor)
	if err != nil {
		httpapi.WriteError(w, 409, "postgresql_plan_not_applicable", err.Error())
		return
	}
	httpapi.WriteJSON(w, 202, job)
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
