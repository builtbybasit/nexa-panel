package backups

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/nexa-panel/nexa-panel/internal/platform/identity"
	"github.com/nexa-panel/nexa-panel/internal/platform/jobs"
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
	Sites           []siteSelection `json:"sites"`
	Databases       []dbSelection   `json:"databases"`
	AllowUnverified bool            `json:"allowUnverified"`
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
	if record.IntegrityState != integrityPassed && !request.AllowUnverified {
		writeError(w, http.StatusConflict, "backup_restore_unverified_confirmation_required", "Confirm that you want to restore a copy whose integrity has not passed verification.")
		return
	}
	// Every requested entry must belong to this copy: the entry name reaches the
	// agent as a file path, so it is never taken on trust from the client.
	known := map[string]struct{}{}
	for _, entry := range record.toCopy().Entries {
		known[entry.Name] = struct{}{}
	}
	usedEntries := make(map[string]struct{}, len(request.Sites)+len(request.Databases))
	usedSiteIDs := make(map[string]struct{}, len(request.Sites))
	usedDatabaseRefs := make(map[string]struct{}, len(request.Databases))
	for index := range request.Sites {
		site := &request.Sites[index]
		site.Entry = strings.TrimSpace(site.Entry)
		site.SiteID = strings.TrimSpace(site.SiteID)
		if _, ok := known[site.Entry]; !ok || !strings.HasPrefix(site.Entry, "site-") || !strings.HasSuffix(site.Entry, ".tar.gz") {
			writeError(w, http.StatusUnprocessableEntity, "backup_restore_unknown_entry", "A selected item is not part of this backup copy.")
			return
		}
		if site.SiteID == "" {
			writeError(w, http.StatusUnprocessableEntity, "backup_restore_invalid_destination", "Every selected site needs a destination.")
			return
		}
		if _, duplicate := usedEntries[site.Entry]; duplicate {
			writeError(w, http.StatusUnprocessableEntity, "backup_restore_duplicate_entry", "Each backup item can be restored only once per request.")
			return
		}
		if _, duplicate := usedSiteIDs[site.SiteID]; duplicate {
			writeError(w, http.StatusUnprocessableEntity, "backup_restore_duplicate_destination", "Each site can receive only one item per restore request.")
			return
		}
		usedEntries[site.Entry] = struct{}{}
		usedSiteIDs[site.SiteID] = struct{}{}
	}
	for index := range request.Databases {
		database := &request.Databases[index]
		database.Entry = strings.TrimSpace(database.Entry)
		database.DatabaseRef = strings.TrimSpace(database.DatabaseRef)
		if _, ok := known[database.Entry]; !ok || !strings.HasPrefix(database.Entry, "db-") {
			writeError(w, http.StatusUnprocessableEntity, "backup_restore_unknown_entry", "A selected item is not part of this backup copy.")
			return
		}
		if _, _, ok := strings.Cut(database.DatabaseRef, ":"); !ok {
			writeError(w, http.StatusUnprocessableEntity, "backup_restore_invalid_destination", "Every selected database needs a valid engine-qualified destination.")
			return
		}
		if _, duplicate := usedEntries[database.Entry]; duplicate {
			writeError(w, http.StatusUnprocessableEntity, "backup_restore_duplicate_entry", "Each backup item can be restored only once per request.")
			return
		}
		if _, duplicate := usedDatabaseRefs[database.DatabaseRef]; duplicate {
			writeError(w, http.StatusUnprocessableEntity, "backup_restore_duplicate_destination", "Each database can receive only one item per restore request.")
			return
		}
		usedEntries[database.Entry] = struct{}{}
		usedDatabaseRefs[database.DatabaseRef] = struct{}{}
	}

	var actor *string
	if user, ok := identity.UserFromContext(r.Context()); ok {
		actor = &user.ID
	}
	payload := restorePayload{CopyID: record.ID, Sites: request.Sites, Databases: request.Databases}
	siteIDs := make([]string, 0, len(request.Sites))
	for _, selection := range request.Sites {
		siteIDs = append(siteIDs, selection.SiteID)
	}
	if len(request.Databases) != 0 {
		siteIDs = nil
	}
	job, err := m.jobs.SubmitTitledWithOptions(r.Context(), "backup.restore", "Restore backup "+record.CopyName,
		payload, jobs.SubmitOptions{
			ActorUserID: actor, SiteIDs: siteIDs, IdempotencyKey: r.Header.Get("Idempotency-Key"),
		})
	if err != nil {
		if errors.Is(err, jobs.ErrIdempotencyConflict) {
			writeError(w, http.StatusConflict, "backup_restore_idempotency_conflict", "The idempotency key was already used for a different restore request.")
			return
		}
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
	checksums := make(map[string]string)
	for _, entry := range record.toCopy().Entries {
		checksums[entry.Name] = entry.SHA256
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
		sites = append(sites, backupoperator.SiteRestoreTarget{
			Entry: selection.Entry, SHA256: checksums[selection.Entry], Target: target, Clear: selection.Clear,
		})
	}
	databases := make([]backupoperator.DatabaseRestoreTarget, 0, len(payload.Databases))
	for _, selection := range payload.Databases {
		target, err := m.resolveDatabaseRef(ctx, selection.DatabaseRef)
		if err != nil {
			return nil, err
		}
		databases = append(databases, backupoperator.DatabaseRestoreTarget{
			Entry: selection.Entry, SHA256: checksums[selection.Entry], Target: target, Clear: selection.Clear,
		})
	}

	_ = report(30, "Restoring "+record.CopyName)
	if err := m.operator.Restore(ctx, backupoperator.RestoreRequest{
		Account: account, PlanID: record.PlanID, CopyName: record.CopyName,
		Sites: sites, Databases: databases,
	}); err != nil {
		m.recordRestoreVerification(ctx, record.ID, restoreTestFailed, err)
		return nil, err
	}
	m.recordRestoreVerification(ctx, record.ID, restoreTestPassed, nil)
	return map[string]string{"status": "restored", "copyName": record.CopyName}, nil
}

func (m *Module) recordRestoreVerification(ctx context.Context, copyID, state string, restoreErr error) {
	now := m.now().UTC()
	query := m.database.NewUpdate().Model((*copyModel)(nil)).
		Set("restore_test_state = ?", state).Set("restore_tested_at = ?", now)
	if restoreErr == nil {
		query = query.Set("health_error = NULL")
	} else {
		message := restoreErr.Error()
		if len(message) > 500 {
			message = message[:500]
		}
		query = query.Set("health_error = ?", message)
	}
	if _, err := query.Where("id = ?", copyID).Exec(ctx); err != nil {
		// The restore side effect already happened (or failed). Do not turn a
		// metadata write into a retry of a destructive operation.
		m.logger.Error("record backup restore verification", "copy_id", copyID, "state", state, "error", err)
	}
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
