package backups

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/nexa-panel/nexa-panel/internal/platform/identity"
	backupoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/backups"
)

// siteSelection / dbSelection are the client's restore choices for one copy:
// which archived entry goes to which live destination, and whether to clear it
// first. Destinations are chosen at restore time (FastPanel-style From→To), so
// a copy can be restored onto a different site or database than it came from.
type siteSelection struct {
	Entry  string `json:"entry"`
	SiteID string `json:"siteId"`
	Clear  bool   `json:"clear"`
}

type dbSelection struct {
	Entry       string `json:"entry"`
	DatabaseRef string `json:"databaseRef"`
	Clear       bool   `json:"clear"`
}

type restoreRequest struct {
	Sites     []siteSelection `json:"sites"`
	Databases []dbSelection   `json:"databases"`
}

// restorePayload is what the job carries: the copy plus the (validated)
// selections. Destinations are resolved to targets inside the job so it stays
// self-contained and re-runnable.
type restorePayload struct {
	CopyID    string          `json:"copyId"`
	Sites     []siteSelection `json:"sites"`
	Databases []dbSelection   `json:"databases"`
}

func (m *Module) restoreCopyHTTP(w http.ResponseWriter, r *http.Request) {
	record, err := m.getCopyModel(r.Context(), r.PathValue("copyId"))
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "backup_copy_not_found", "The requested backup copy does not exist.")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "backup_copy_unavailable", "The backup copy could not be loaded.")
		return
	}
	var request restoreRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if len(request.Sites) == 0 && len(request.Databases) == 0 {
		writeError(w, http.StatusUnprocessableEntity, "backup_restore_empty", "Choose at least one item to restore.")
		return
	}
	// Every requested entry must belong to this copy: the entry name reaches the
	// agent as a file path, so it is never taken on trust from the client.
	known := map[string]struct{}{}
	for _, entry := range record.toCopy().Entries {
		known[entry.Name] = struct{}{}
	}
	for _, site := range request.Sites {
		if _, ok := known[site.Entry]; !ok {
			writeError(w, http.StatusUnprocessableEntity, "backup_restore_unknown_entry", "A selected item is not part of this backup copy.")
			return
		}
	}
	for _, database := range request.Databases {
		if _, ok := known[database.Entry]; !ok {
			writeError(w, http.StatusUnprocessableEntity, "backup_restore_unknown_entry", "A selected item is not part of this backup copy.")
			return
		}
	}

	var actor *string
	if user, ok := identity.UserFromContext(r.Context()); ok {
		actor = &user.ID
	}
	payload := restorePayload{CopyID: record.ID, Sites: request.Sites, Databases: request.Databases}
	job, err := m.jobs.SubmitTitled(r.Context(), "backup.restore", "Restore backup "+record.CopyName, payload, actor)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "backup_restore_failed", "The restore could not be queued.")
		return
	}
	w.Header().Set("Location", fmt.Sprintf("/api/v1/jobs/%d", job.ID))
	writeJSON(w, http.StatusAccepted, job)
}

func (m *Module) restoreJob(ctx context.Context, request json.RawMessage, report func(int, string) error) (any, error) {
	var payload restorePayload
	if err := json.Unmarshal(request, &payload); err != nil {
		return nil, err
	}
	record, err := m.getCopyModel(ctx, payload.CopyID)
	if err != nil {
		return nil, fmt.Errorf("load backup copy: %w", err)
	}
	plan := new(planModel)
	if err := m.database.NewSelect().Model(plan).Where("id = ?", record.PlanID).Scan(ctx); err != nil {
		return nil, fmt.Errorf("load backup plan: %w", err)
	}
	accountModel, err := m.getAccountModel(ctx, record.AccountID)
	if err != nil {
		return nil, fmt.Errorf("load backup account: %w", err)
	}
	account, err := m.resolveAccount(*accountModel)
	if err != nil {
		return nil, err
	}

	_ = report(10, "Resolving restore destinations")
	sites := make([]backupoperator.SiteRestoreTarget, 0, len(payload.Sites))
	for _, selection := range payload.Sites {
		if m.sites == nil {
			return nil, errors.New("site restores are unavailable on this node")
		}
		target, err := m.sites.BackupSite(ctx, selection.SiteID)
		if err != nil {
			return nil, fmt.Errorf("resolve site %s: %w", selection.SiteID, err)
		}
		sites = append(sites, backupoperator.SiteRestoreTarget{Entry: selection.Entry, Target: target, Clear: selection.Clear})
	}
	databases := make([]backupoperator.DatabaseRestoreTarget, 0, len(payload.Databases))
	for _, selection := range payload.Databases {
		target, err := m.resolveDatabaseRef(ctx, selection.DatabaseRef)
		if err != nil {
			return nil, err
		}
		databases = append(databases, backupoperator.DatabaseRestoreTarget{Entry: selection.Entry, Target: target, Clear: selection.Clear})
	}

	_ = report(30, "Restoring "+record.CopyName)
	if err := m.operator.Restore(ctx, backupoperator.RestoreRequest{
		Account: account, PlanID: record.PlanID, CopyName: record.CopyName,
		Sites: sites, Databases: databases, StagingRoot: m.stagingRoot,
	}); err != nil {
		return nil, err
	}
	return map[string]string{"status": "restored", "copyName": record.CopyName}, nil
}

func (m *Module) deleteCopyHTTP(w http.ResponseWriter, r *http.Request) {
	record, err := m.getCopyModel(r.Context(), r.PathValue("copyId"))
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "backup_copy_not_found", "The requested backup copy does not exist.")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "backup_copy_unavailable", "The backup copy could not be loaded.")
		return
	}
	accountModel, err := m.getAccountModel(r.Context(), record.AccountID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "backup_account_unavailable", "The backup account could not be loaded.")
		return
	}
	account, err := m.resolveAccount(*accountModel)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "backup_account_unavailable", "The backup account could not be resolved.")
		return
	}
	if err := m.operator.DeleteCopy(r.Context(), backupoperator.DeleteRequest{
		Account: account, PlanID: record.PlanID, CopyName: record.CopyName,
	}); err != nil {
		writeError(w, http.StatusBadGateway, "backup_copy_delete_failed", "The backup copy could not be removed from storage.")
		return
	}
	if _, err := m.database.NewDelete().Model((*copyModel)(nil)).Where("id = ?", record.ID).Exec(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "backup_copy_delete_failed", "The backup copy was removed from storage but its record could not be cleared.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
