package mysql

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
		"listMySQLFamilyEngines":          m.enginesHTTP,
		"listMySQLFamilyAccounts":         m.accountsHTTP,
		"createMySQLFamilyAccount":        m.createAccountHTTP,
		"rotateMySQLFamilyAccount":        m.rotateAccountHTTP,
		"dropMySQLFamilyAccount":          m.dropAccountHTTP,
		"revealMySQLFamilyCredentialOnce": m.credentialHTTP,
		"listMySQLFamilyDatabases":        m.databasesHTTP,
		"createMySQLFamilyDatabase":       m.createDatabaseHTTP,
		"dropMySQLFamilyDatabase":         m.dropDatabaseHTTP,
		"listMySQLFamilyGrants":           m.grantsHTTP,
		"createMySQLFamilyGrant":          m.createGrantHTTP,
		"dropMySQLFamilyGrant":            m.dropGrantHTTP,
		"listMySQLFamilyRestorePoints":    m.restorePointsHTTP,
		"createMySQLFamilyRestorePoint":   m.createBackupHTTP,
		"prepareMySQLFamilyRestore":       m.prepareRestoreHTTP,
		"getMySQLFamilyPlan":              m.planHTTP,
		"applyMySQLFamilyPlan":            m.applyHTTP,
	})
}

func (m *Module) enginesHTTP(w http.ResponseWriter, r *http.Request) {
	items, err := m.SyncEngines(r.Context())
	if err != nil {
		httpapi.WriteError(w, http.StatusServiceUnavailable, "mysql_family_discovery_failed", "MySQL-family engines could not be discovered.")
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (m *Module) accountsHTTP(w http.ResponseWriter, r *http.Request) {
	items, err := m.ListAccounts(r.Context(), r.URL.Query().Get("engineId"))
	if err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, "mysql_family_accounts_unavailable", "MySQL-family accounts could not be loaded.")
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (m *Module) createAccountHTTP(w http.ResponseWriter, r *http.Request) {
	request, decodeErr := webhandler.Decode[CreateAccountRequest](w, r)
	if decodeErr != nil {
		webhandler.Fail(w, decodeErr)
		return
	}
	actor, authErr := webhandler.Actor(r)
	if authErr != nil {
		webhandler.Fail(w, authErr)
		return
	}
	account, job, err := m.CreateAccount(r.Context(), request, &actor.ID)
	if err != nil {
		httpapi.WriteError(w, http.StatusUnprocessableEntity, "mysql_family_account_invalid", err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusAccepted, map[string]any{"account": account, "job": job})
}

func (m *Module) rotateAccountHTTP(w http.ResponseWriter, r *http.Request) {
	actor, authErr := webhandler.Actor(r)
	if authErr != nil {
		webhandler.Fail(w, authErr)
		return
	}
	account, job, err := m.RotateAccount(r.Context(), r.PathValue("id"), &actor.ID)
	if err != nil {
		httpapi.WriteError(w, http.StatusConflict, "mysql_family_account_not_rotatable", err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusAccepted, map[string]any{"account": account, "job": job})
}

func (m *Module) credentialHTTP(w http.ResponseWriter, r *http.Request) {
	credential, err := m.RevealCredential(r.Context(), r.PathValue("id"))
	if err != nil {
		httpapi.WriteError(w, http.StatusConflict, "mysql_family_credential_unavailable", err.Error())
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	httpapi.WriteJSON(w, http.StatusOK, map[string]string{"credential": credential})
}

func (m *Module) databasesHTTP(w http.ResponseWriter, r *http.Request) {
	items, err := m.SyncDatabaseSizes(r.Context(), r.URL.Query().Get("engineId"))
	if err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, "mysql_family_databases_unavailable", "MySQL-family databases could not be loaded.")
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
		httpapi.WriteError(w, http.StatusUnprocessableEntity, "mysql_family_database_invalid", err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusAccepted, map[string]any{"database": database, "job": job})
}

func (m *Module) grantsHTTP(w http.ResponseWriter, r *http.Request) {
	items, err := m.ListGrants(r.Context(), r.URL.Query().Get("databaseId"))
	if err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, "mysql_family_grants_unavailable", "MySQL-family grants could not be loaded.")
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
		httpapi.WriteError(w, http.StatusUnprocessableEntity, "mysql_family_grant_invalid", err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusAccepted, map[string]any{"grant": grant, "job": job})
}

func (m *Module) dropDatabaseHTTP(w http.ResponseWriter, r *http.Request) {
	actor, authErr := webhandler.Actor(r)
	if authErr != nil {
		webhandler.Fail(w, authErr)
		return
	}
	job, err := m.DropDatabase(r.Context(), r.PathValue("id"), &actor.ID)
	if err != nil {
		httpapi.WriteError(w, http.StatusConflict, "mysql_family_database_not_removable", err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusAccepted, map[string]any{"job": job})
}

func (m *Module) dropAccountHTTP(w http.ResponseWriter, r *http.Request) {
	actor, authErr := webhandler.Actor(r)
	if authErr != nil {
		webhandler.Fail(w, authErr)
		return
	}
	job, err := m.DropAccount(r.Context(), r.PathValue("id"), &actor.ID)
	if err != nil {
		httpapi.WriteError(w, http.StatusConflict, "mysql_family_account_not_removable", err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusAccepted, map[string]any{"job": job})
}

func (m *Module) dropGrantHTTP(w http.ResponseWriter, r *http.Request) {
	actor, authErr := webhandler.Actor(r)
	if authErr != nil {
		webhandler.Fail(w, authErr)
		return
	}
	job, err := m.DropGrant(r.Context(), r.PathValue("id"), &actor.ID)
	if err != nil {
		httpapi.WriteError(w, http.StatusConflict, "mysql_family_grant_not_removable", err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusAccepted, map[string]any{"job": job})
}

func (m *Module) restorePointsHTTP(w http.ResponseWriter, r *http.Request) {
	items, err := m.ListRestorePoints(r.Context(), r.URL.Query().Get("databaseId"))
	if err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, "mysql_family_restore_points_unavailable", "Restore points could not be loaded.")
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
	point, job, err := m.CreateBackup(r.Context(), r.PathValue("id"), &actor.ID)
	if err != nil {
		httpapi.WriteError(w, http.StatusConflict, "mysql_family_backup_invalid", err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusAccepted, map[string]any{"restorePoint": point, "job": job})
}

func (m *Module) prepareRestoreHTTP(w http.ResponseWriter, r *http.Request) {
	actor, authErr := webhandler.Actor(r)
	if authErr != nil {
		webhandler.Fail(w, authErr)
		return
	}
	point, job, err := m.PrepareRestore(r.Context(), r.PathValue("id"), &actor.ID)
	if err != nil {
		httpapi.WriteError(w, http.StatusConflict, "mysql_family_restore_invalid", err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusAccepted, map[string]any{"restorePoint": point, "job": job})
}

func (m *Module) planHTTP(w http.ResponseWriter, r *http.Request) {
	resourceType, err := normalizeResourceType(r.PathValue("resourceType"))
	if err != nil {
		httpapi.WriteError(w, http.StatusNotFound, "mysql_family_resource_not_found", "MySQL-family resource type does not exist.")
		return
	}
	plan, err := m.StoredPlan(r.Context(), resourceType, r.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) {
		httpapi.WriteError(w, http.StatusNotFound, "mysql_family_plan_not_found", "A MySQL-family plan is not ready.")
		return
	}
	if err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, "mysql_family_plan_unavailable", "MySQL-family plan could not be loaded.")
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"plan": plan, "expiresAt": plan.ExpiresAt})
}

func (m *Module) applyHTTP(w http.ResponseWriter, r *http.Request) {
	resourceType, err := normalizeResourceType(r.PathValue("resourceType"))
	if err != nil {
		httpapi.WriteError(w, http.StatusNotFound, "mysql_family_resource_not_found", "MySQL-family resource type does not exist.")
		return
	}
	actor, authErr := webhandler.Actor(r)
	if authErr != nil {
		webhandler.Fail(w, authErr)
		return
	}
	job, err := m.ApplyPlan(r.Context(), resourceType, r.PathValue("id"), &actor.ID)
	if err != nil {
		httpapi.WriteError(w, http.StatusConflict, "mysql_family_plan_not_applicable", err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusAccepted, job)
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
