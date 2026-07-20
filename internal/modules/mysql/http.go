package mysql

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
		{"GET /api/v1/mysql-family/engines", "databases.read", http.HandlerFunc(m.enginesHTTP)},
		{"GET /api/v1/mysql-family/accounts", "databases.read", http.HandlerFunc(m.accountsHTTP)},
		{"POST /api/v1/mysql-family/accounts", "databases.write", http.HandlerFunc(m.createAccountHTTP)},
		{"POST /api/v1/mysql-family/accounts/{id}/rotate", "databases.write", http.HandlerFunc(m.rotateAccountHTTP)},
		{"POST /api/v1/mysql-family/accounts/{id}/credential", "operations.apply", http.HandlerFunc(m.credentialHTTP)},
		{"GET /api/v1/mysql-family/databases", "databases.read", http.HandlerFunc(m.databasesHTTP)},
		{"POST /api/v1/mysql-family/databases", "databases.write", http.HandlerFunc(m.createDatabaseHTTP)},
		{"GET /api/v1/mysql-family/grants", "databases.read", http.HandlerFunc(m.grantsHTTP)},
		{"POST /api/v1/mysql-family/grants", "databases.write", http.HandlerFunc(m.createGrantHTTP)},
		{"GET /api/v1/mysql-family/restore-points", "databases.read", http.HandlerFunc(m.restorePointsHTTP)},
		{"POST /api/v1/mysql-family/databases/{id}/backups", "databases.write", http.HandlerFunc(m.createBackupHTTP)},
		{"POST /api/v1/mysql-family/restore-points/{id}/restore", "databases.write", http.HandlerFunc(m.prepareRestoreHTTP)},
		{"GET /api/v1/mysql-family/{resourceType}/{id}/plan", "databases.read", http.HandlerFunc(m.planHTTP)},
		{"POST /api/v1/mysql-family/{resourceType}/{id}/apply", "operations.apply", http.HandlerFunc(m.applyHTTP)},
	}
	for _, route := range routes {
		if err := registry.HandleAuthorized(route.pattern, route.permission, route.handler); err != nil {
			return err
		}
	}
	return nil
}

func (m *Module) enginesHTTP(w http.ResponseWriter, r *http.Request) {
	items, err := m.SyncEngines(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "mysql_family_discovery_failed", "MySQL-family engines could not be discovered.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (m *Module) accountsHTTP(w http.ResponseWriter, r *http.Request) {
	items, err := m.ListAccounts(r.Context(), r.URL.Query().Get("engineId"))
	if err != nil {
		writeError(w, 500, "mysql_family_accounts_unavailable", "MySQL-family accounts could not be loaded.")
		return
	}
	writeJSON(w, 200, map[string]any{"items": items})
}

func (m *Module) createAccountHTTP(w http.ResponseWriter, r *http.Request) {
	var request CreateAccountRequest
	if decodeJSON(w, r, &request) != nil {
		writeError(w, 400, "invalid_request", "Request body must be valid JSON.")
		return
	}
	actor, ok := actorID(r)
	if !ok {
		writeError(w, 401, "authentication_required", "Sign in to continue.")
		return
	}
	account, job, err := m.CreateAccount(r.Context(), request, actor)
	if err != nil {
		writeError(w, 422, "mysql_family_account_invalid", err.Error())
		return
	}
	writeJSON(w, 202, map[string]any{"account": account, "job": job})
}

func (m *Module) rotateAccountHTTP(w http.ResponseWriter, r *http.Request) {
	actor, ok := actorID(r)
	if !ok {
		writeError(w, 401, "authentication_required", "Sign in to continue.")
		return
	}
	account, job, err := m.RotateAccount(r.Context(), r.PathValue("id"), actor)
	if err != nil {
		writeError(w, 409, "mysql_family_account_not_rotatable", err.Error())
		return
	}
	writeJSON(w, 202, map[string]any{"account": account, "job": job})
}

func (m *Module) credentialHTTP(w http.ResponseWriter, r *http.Request) {
	credential, err := m.RevealCredential(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, 409, "mysql_family_credential_unavailable", err.Error())
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	writeJSON(w, 200, map[string]string{"credential": credential})
}

func (m *Module) databasesHTTP(w http.ResponseWriter, r *http.Request) {
	items, err := m.SyncDatabaseSizes(r.Context(), r.URL.Query().Get("engineId"))
	if err != nil {
		writeError(w, 500, "mysql_family_databases_unavailable", "MySQL-family databases could not be loaded.")
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
		writeError(w, 422, "mysql_family_database_invalid", err.Error())
		return
	}
	writeJSON(w, 202, map[string]any{"database": database, "job": job})
}

func (m *Module) grantsHTTP(w http.ResponseWriter, r *http.Request) {
	items, err := m.ListGrants(r.Context(), r.URL.Query().Get("databaseId"))
	if err != nil {
		writeError(w, 500, "mysql_family_grants_unavailable", "MySQL-family grants could not be loaded.")
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
		writeError(w, 422, "mysql_family_grant_invalid", err.Error())
		return
	}
	writeJSON(w, 202, map[string]any{"grant": grant, "job": job})
}

func (m *Module) restorePointsHTTP(w http.ResponseWriter, r *http.Request) {
	items, err := m.ListRestorePoints(r.Context(), r.URL.Query().Get("databaseId"))
	if err != nil {
		writeError(w, 500, "mysql_family_restore_points_unavailable", "Restore points could not be loaded.")
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
		writeError(w, 409, "mysql_family_backup_invalid", err.Error())
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
		writeError(w, 409, "mysql_family_restore_invalid", err.Error())
		return
	}
	writeJSON(w, 202, map[string]any{"restorePoint": point, "job": job})
}

func (m *Module) planHTTP(w http.ResponseWriter, r *http.Request) {
	resourceType, err := normalizeResourceType(r.PathValue("resourceType"))
	if err != nil {
		writeError(w, 404, "mysql_family_resource_not_found", "MySQL-family resource type does not exist.")
		return
	}
	plan, err := m.StoredPlan(r.Context(), resourceType, r.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, 404, "mysql_family_plan_not_found", "A MySQL-family plan is not ready.")
		return
	}
	if err != nil {
		writeError(w, 500, "mysql_family_plan_unavailable", "MySQL-family plan could not be loaded.")
		return
	}
	writeJSON(w, 200, map[string]any{"plan": plan, "expiresAt": plan.ExpiresAt})
}

func (m *Module) applyHTTP(w http.ResponseWriter, r *http.Request) {
	resourceType, err := normalizeResourceType(r.PathValue("resourceType"))
	if err != nil {
		writeError(w, 404, "mysql_family_resource_not_found", "MySQL-family resource type does not exist.")
		return
	}
	actor, ok := actorID(r)
	if !ok {
		writeError(w, 401, "authentication_required", "Sign in to continue.")
		return
	}
	job, err := m.ApplyPlan(r.Context(), resourceType, r.PathValue("id"), actor)
	if err != nil {
		writeError(w, 409, "mysql_family_plan_not_applicable", err.Error())
		return
	}
	writeJSON(w, 202, job)
}

func normalizeResourceType(value string) (string, error) {
	switch strings.TrimSpace(value) {
	case "accounts":
		return resourceAccount, nil
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
