package databases

import (
	"net/http"

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
		"listDatabaseServers":        m.serversHTTP,
		"createDatabaseServer":       m.createServerHTTP,
		"listDatabaseUsers":          m.usersHTTP,
		"createDatabaseUser":         m.createUserHTTP,
		"setDatabaseUserPassword":    m.setPasswordHTTP,
		"dropDatabaseUser":           m.dropUserHTTP,
		"listManagedDatabases":       m.databasesHTTP,
		"createManagedDatabase":      m.createDatabaseHTTP,
		"dropManagedDatabase":        m.dropDatabaseHTTP,
		"listDatabaseGrants":         m.grantsHTTP,
		"createDatabaseGrant":        m.createGrantHTTP,
		"dropDatabaseGrant":          m.dropGrantHTTP,
		"listDatabaseRestorePoints":  m.restorePointsHTTP,
		"createDatabaseRestorePoint": m.createBackupHTTP,
		"restoreDatabaseBackup":      m.restoreHTTP,
	})
}

func (m *Module) serversHTTP(w http.ResponseWriter, r *http.Request) {
	items, err := m.SyncServers(r.Context())
	if err != nil {
		httpapi.WriteError(w, http.StatusServiceUnavailable, "databases_discovery_failed", "Database servers could not be discovered.")
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (m *Module) createServerHTTP(w http.ResponseWriter, r *http.Request) {
	request, decodeErr := webhandler.Decode[CreateServerRequest](w, r)
	if decodeErr != nil {
		webhandler.Fail(w, decodeErr)
		return
	}
	actor, authErr := webhandler.Actor(r)
	if authErr != nil {
		webhandler.Fail(w, authErr)
		return
	}
	server, job, err := m.CreateServer(r.Context(), request, &actor.ID)
	if err != nil {
		httpapi.WriteError(w, http.StatusUnprocessableEntity, "databases_server_invalid", err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusAccepted, map[string]any{"server": server, "job": job})
}

func (m *Module) usersHTTP(w http.ResponseWriter, r *http.Request) {
	items, err := m.ListUsers(r.Context(), r.URL.Query().Get("serverId"))
	if err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, "databases_users_unavailable", "Database users could not be loaded.")
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (m *Module) createUserHTTP(w http.ResponseWriter, r *http.Request) {
	request, decodeErr := webhandler.Decode[CreateUserRequest](w, r)
	if decodeErr != nil {
		webhandler.Fail(w, decodeErr)
		return
	}
	actor, authErr := webhandler.Actor(r)
	if authErr != nil {
		webhandler.Fail(w, authErr)
		return
	}
	user, job, err := m.CreateUser(r.Context(), request, &actor.ID)
	if err != nil {
		httpapi.WriteError(w, http.StatusUnprocessableEntity, "databases_user_invalid", err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusAccepted, map[string]any{"user": user, "job": job})
}

func (m *Module) setPasswordHTTP(w http.ResponseWriter, r *http.Request) {
	request, decodeErr := webhandler.Decode[SetPasswordRequest](w, r)
	if decodeErr != nil {
		webhandler.Fail(w, decodeErr)
		return
	}
	actor, authErr := webhandler.Actor(r)
	if authErr != nil {
		webhandler.Fail(w, authErr)
		return
	}
	user, job, err := m.SetPassword(r.Context(), r.PathValue("id"), request.Password, &actor.ID)
	if err != nil {
		httpapi.WriteError(w, http.StatusConflict, "databases_password_not_settable", err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusAccepted, map[string]any{"user": user, "job": job})
}

func (m *Module) dropUserHTTP(w http.ResponseWriter, r *http.Request) {
	actor, authErr := webhandler.Actor(r)
	if authErr != nil {
		webhandler.Fail(w, authErr)
		return
	}
	job, err := m.DropUser(r.Context(), r.PathValue("id"), &actor.ID)
	if err != nil {
		httpapi.WriteError(w, http.StatusConflict, "databases_user_not_removable", err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusAccepted, map[string]any{"job": job})
}

func (m *Module) databasesHTTP(w http.ResponseWriter, r *http.Request) {
	items, err := m.SyncDatabaseSizes(r.Context(), r.URL.Query().Get("serverId"))
	if err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, "databases_unavailable", "Managed databases could not be loaded.")
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (m *Module) createDatabaseHTTP(w http.ResponseWriter, r *http.Request) {
	request, decodeErr := webhandler.Decode[CreateDatabaseRequest](w, r)
	if decodeErr != nil {
		webhandler.Fail(w, decodeErr)
		return
	}
	actor, authErr := webhandler.Actor(r)
	if authErr != nil {
		webhandler.Fail(w, authErr)
		return
	}
	database, job, err := m.CreateDatabase(r.Context(), request, &actor.ID)
	if err != nil {
		httpapi.WriteError(w, http.StatusUnprocessableEntity, "databases_database_invalid", err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusAccepted, map[string]any{"database": database, "job": job})
}

func (m *Module) dropDatabaseHTTP(w http.ResponseWriter, r *http.Request) {
	actor, authErr := webhandler.Actor(r)
	if authErr != nil {
		webhandler.Fail(w, authErr)
		return
	}
	job, err := m.DropDatabase(r.Context(), r.PathValue("id"), &actor.ID)
	if err != nil {
		httpapi.WriteError(w, http.StatusConflict, "databases_database_not_removable", err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusAccepted, map[string]any{"job": job})
}

func (m *Module) grantsHTTP(w http.ResponseWriter, r *http.Request) {
	items, err := m.ListGrants(r.Context(), r.URL.Query().Get("databaseId"))
	if err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, "databases_grants_unavailable", "Database grants could not be loaded.")
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (m *Module) createGrantHTTP(w http.ResponseWriter, r *http.Request) {
	request, decodeErr := webhandler.Decode[CreateGrantRequest](w, r)
	if decodeErr != nil {
		webhandler.Fail(w, decodeErr)
		return
	}
	actor, authErr := webhandler.Actor(r)
	if authErr != nil {
		webhandler.Fail(w, authErr)
		return
	}
	grant, job, err := m.CreateGrant(r.Context(), request, &actor.ID)
	if err != nil {
		httpapi.WriteError(w, http.StatusUnprocessableEntity, "databases_grant_invalid", err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusAccepted, map[string]any{"grant": grant, "job": job})
}

func (m *Module) dropGrantHTTP(w http.ResponseWriter, r *http.Request) {
	actor, authErr := webhandler.Actor(r)
	if authErr != nil {
		webhandler.Fail(w, authErr)
		return
	}
	job, err := m.DropGrant(r.Context(), r.PathValue("id"), &actor.ID)
	if err != nil {
		httpapi.WriteError(w, http.StatusConflict, "databases_grant_not_removable", err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusAccepted, map[string]any{"job": job})
}

func (m *Module) restorePointsHTTP(w http.ResponseWriter, r *http.Request) {
	items, err := m.ListRestorePoints(r.Context(), r.URL.Query().Get("databaseId"))
	if err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, "databases_restore_points_unavailable", "Restore points could not be loaded.")
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (m *Module) createBackupHTTP(w http.ResponseWriter, r *http.Request) {
	actor, authErr := webhandler.Actor(r)
	if authErr != nil {
		webhandler.Fail(w, authErr)
		return
	}
	request, decodeErr := webhandler.Decode[CreateRestorePointRequest](w, r)
	if decodeErr != nil {
		webhandler.Fail(w, decodeErr)
		return
	}
	point, job, err := m.CreateBackup(r.Context(), request.DatabaseID, &actor.ID)
	if err != nil {
		httpapi.WriteError(w, http.StatusConflict, "databases_backup_invalid", err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusAccepted, map[string]any{"restorePoint": point, "job": job})
}

func (m *Module) restoreHTTP(w http.ResponseWriter, r *http.Request) {
	actor, authErr := webhandler.Actor(r)
	if authErr != nil {
		webhandler.Fail(w, authErr)
		return
	}
	point, job, err := m.Restore(r.Context(), r.PathValue("id"), &actor.ID)
	if err != nil {
		httpapi.WriteError(w, http.StatusConflict, "databases_restore_invalid", err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusAccepted, map[string]any{"restorePoint": point, "job": job})
}
