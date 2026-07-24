package backups

import (
	"database/sql"
	"errors"
	"net/http"

	"github.com/nexa-panel/nexa-panel/internal/platform/httpapi"
	"github.com/nexa-panel/nexa-panel/internal/platform/identity"
)

func (m *Module) listAccountsHTTP(w http.ResponseWriter, r *http.Request) {
	user, ok := identity.UserFromContext(r.Context())
	if !ok {
		httpapi.WriteError(w, http.StatusUnauthorized, "authentication_required", "Sign in to continue.")
		return
	}
	models, err := m.listAccountsForUser(r.Context(), user)
	if err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, "backup_accounts_unavailable", "The backup accounts could not be loaded.")
		return
	}
	accounts := make([]Account, 0, len(models))
	for _, model := range models {
		accounts = append(accounts, model.toAccount())
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"items": accounts})
}

func (m *Module) createAccountHTTP(w http.ResponseWriter, r *http.Request) {
	var request AccountRequest
	if err := decodeJSON(w, r, &request); err != nil {
		httpapi.WriteError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	account, err := m.CreateAccount(r.Context(), request)
	if err != nil {
		writeAccountError(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusCreated, account)
}

func (m *Module) getAccountHTTP(w http.ResponseWriter, r *http.Request) {
	user, ok := identity.UserFromContext(r.Context())
	if !ok {
		httpapi.WriteError(w, http.StatusUnauthorized, "authentication_required", "Sign in to continue.")
		return
	}
	model, err := m.getAccountForUser(r.Context(), user, r.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) {
		httpapi.WriteError(w, http.StatusNotFound, "backup_account_not_found", "The requested backup account does not exist.")
		return
	}
	if err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, "backup_account_unavailable", "The backup account could not be loaded.")
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, model.toAccount())
}

func (m *Module) updateAccountHTTP(w http.ResponseWriter, r *http.Request) {
	var request AccountRequest
	if err := decodeJSON(w, r, &request); err != nil {
		httpapi.WriteError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	account, err := m.UpdateAccount(r.Context(), r.PathValue("id"), request)
	if errors.Is(err, sql.ErrNoRows) {
		httpapi.WriteError(w, http.StatusNotFound, "backup_account_not_found", "The requested backup account does not exist.")
		return
	}
	if err != nil {
		writeAccountError(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, account)
}

func (m *Module) deleteAccountHTTP(w http.ResponseWriter, r *http.Request) {
	if err := m.DeleteAccount(r.Context(), r.PathValue("id")); err != nil {
		if errors.Is(err, errAccountInUse) {
			httpapi.WriteError(w, http.StatusConflict, "backup_account_in_use", err.Error())
			return
		}
		httpapi.WriteError(w, http.StatusInternalServerError, "backup_account_delete_failed", "The backup account could not be removed.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (m *Module) testAccountHTTP(w http.ResponseWriter, r *http.Request) {
	model, err := m.getAccountModel(r.Context(), r.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) {
		httpapi.WriteError(w, http.StatusNotFound, "backup_account_not_found", "The requested backup account does not exist.")
		return
	}
	if err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, "backup_account_unavailable", "The backup account could not be loaded.")
		return
	}
	result, err := m.TestAccount(r.Context(), *model)
	if err != nil {
		httpapi.WriteError(w, http.StatusBadGateway, "backup_test_failed", "The storage connection could not be tested.")
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, result)
}
