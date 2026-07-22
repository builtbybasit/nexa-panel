package backups

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/uptrace/bun"

	"github.com/nexa-panel/nexa-panel/internal/platform/identity"
	"github.com/nexa-panel/nexa-panel/internal/platform/jobs"
	backupoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/backups"
)

// defaultSystemRetention bounds how many panel-state copies are kept per account.
// A system backup has no owning plan (it protects the panel itself), so retention
// is a fixed module policy rather than a per-plan setting.
const defaultSystemRetention = 30

// SystemCopy is the JSON-facing view of a stored panel-state backup copy.
type SystemCopy struct {
	ID         string    `json:"id"`
	AccountID  string    `json:"accountId"`
	CopyName   string    `json:"copyName"`
	RemotePath string    `json:"remotePath"`
	SizeBytes  int64     `json:"sizeBytes"`
	SHA256     string    `json:"sha256"`
	CreatedAt  time.Time `json:"createdAt"`
}

type systemCopyModel struct {
	bun.BaseModel `bun:"table:system_backup_copies,alias:system_backup_copy"`
	ID            string `bun:",pk"`
	AccountID     string
	CopyName      string
	RemotePath    string
	SizeBytes     int64
	SHA256        string
	CreatedAt     time.Time
}

func (model systemCopyModel) toSystemCopy() SystemCopy {
	return SystemCopy{
		ID: model.ID, AccountID: model.AccountID, CopyName: model.CopyName,
		RemotePath: model.RemotePath, SizeBytes: model.SizeBytes, SHA256: model.SHA256,
		CreatedAt: model.CreatedAt,
	}
}

type systemRunPayload struct {
	AccountID string `json:"accountId"`
}

// systemJob backs up the panel's own state: resolve the chosen account, ask the
// agent to snapshot control.db + master.key and ship the single archive off-node,
// then record the copy. It mirrors runJob but selects no sites or databases — the
// source is the agent-owned panel state. No master.key access happens here; the
// only secret work is decrypting the storage account.
func (m *Module) systemJob(ctx context.Context, request json.RawMessage, report func(int, string) error) (any, error) {
	var payload systemRunPayload
	if err := json.Unmarshal(request, &payload); err != nil {
		return nil, err
	}
	accountModel, err := m.getAccountModel(ctx, payload.AccountID)
	if err != nil {
		return nil, fmt.Errorf("load backup account: %w", err)
	}
	account, err := m.resolveAccount(*accountModel)
	if err != nil {
		return nil, err
	}

	copyName := m.now().UTC().Format("2006-01-02_150405000000000Z") + "_" + randomToken()[:8]
	_ = report(25, "Backing up panel state")
	manifest, err := m.operator.RunSystem(ctx, backupoperator.SystemRunRequest{
		Account: account, CopyName: copyName, Limit: defaultSystemRetention,
	})
	if err != nil {
		return nil, err
	}
	if !manifest.IntegrityChecked {
		return nil, errors.New("backup operator returned an upload without an integrity check")
	}

	_ = report(90, "Recording copy")
	if err := m.recordSystemCopy(ctx, accountModel.ID, manifest); err != nil {
		return nil, err
	}
	return manifest, nil
}

// recordSystemCopy inserts the new panel-state copy and drops rows for copies the
// operator pruned remotely, keeping recorded history aligned with what is stored.
func (m *Module) recordSystemCopy(ctx context.Context, accountID string, manifest backupoperator.Manifest) error {
	var checksum string
	if len(manifest.Entries) > 0 {
		checksum = manifest.Entries[0].SHA256
	}
	now := m.now().UTC()
	model := &systemCopyModel{
		ID: "bksys_" + randomToken(), AccountID: accountID,
		CopyName: manifest.CopyName, RemotePath: manifest.RemotePath,
		SizeBytes: manifest.SizeBytes, SHA256: checksum, CreatedAt: now,
	}
	if _, err := m.database.NewInsert().Model(model).Exec(ctx); err != nil {
		return err
	}
	if len(manifest.Pruned) > 0 {
		_, _ = m.database.NewDelete().Model((*systemCopyModel)(nil)).
			Where("account_id = ?", accountID).Where("copy_name IN (?)", bun.List(manifest.Pruned)).Exec(ctx)
	}
	return nil
}

// runSystemHTTP enqueues a panel-state backup against a chosen account. System
// backups are global (no site scope), so they are restricted to non-developer
// roles, consistent with the account/plan access model.
func (m *Module) runSystemHTTP(w http.ResponseWriter, r *http.Request) {
	user, ok := identity.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication_required", "Sign in to continue.")
		return
	}
	if user.Role == "developer" {
		writeError(w, http.StatusForbidden, "backup_system_forbidden", "Panel-state backups require an operator role.")
		return
	}
	var request systemRunPayload
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	model, err := m.getAccountModel(r.Context(), strings.TrimSpace(request.AccountID))
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "backup_account_not_found", "The requested backup account does not exist.")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "backup_account_unavailable", "The backup account could not be loaded.")
		return
	}
	job, err := m.jobs.SubmitTitledWithOptions(r.Context(), "backup.system", "Back up panel state",
		systemRunPayload{AccountID: model.ID}, jobs.SubmitOptions{
			ActorUserID:    &user.ID,
			IdempotencyKey: r.Header.Get("Idempotency-Key"),
		})
	if err != nil {
		if errors.Is(err, jobs.ErrIdempotencyConflict) {
			writeError(w, http.StatusConflict, "backup_system_idempotency_conflict", "The idempotency key was already used for a different backup request.")
			return
		}
		writeError(w, http.StatusInternalServerError, "backup_system_failed", "The panel-state backup could not be queued.")
		return
	}
	w.Header().Set("Location", fmt.Sprintf("/api/v1/jobs/%d", job.ID))
	writeJSON(w, http.StatusAccepted, job)
}

func (m *Module) listSystemCopiesHTTP(w http.ResponseWriter, r *http.Request) {
	user, ok := identity.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication_required", "Sign in to continue.")
		return
	}
	if user.Role == "developer" {
		writeError(w, http.StatusForbidden, "backup_system_forbidden", "Panel-state backups require an operator role.")
		return
	}
	var models []systemCopyModel
	if err := m.database.NewSelect().Model(&models).Order("copy_name DESC").Scan(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "backup_system_copies_unavailable", "The panel-state backups could not be loaded.")
		return
	}
	copies := make([]SystemCopy, 0, len(models))
	for _, model := range models {
		copies = append(copies, model.toSystemCopy())
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": copies})
}
