package backups

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/platform/httpapi"
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
	PreviewToken    string          `json:"previewToken"`
}

type restoreChoices struct {
	Sites           []siteSelection `json:"sites"`
	Databases       []dbSelection   `json:"databases"`
	AllowUnverified bool            `json:"allowUnverified"`
}

func (request restoreRequest) choices() restoreChoices {
	return restoreChoices{Sites: request.Sites, Databases: request.Databases, AllowUnverified: request.AllowUnverified}
}

// restorePayload deliberately carries the reviewed choices rather than
// resolved host paths or credentials. The worker resolves them again and
// compares PreviewDigest before it can call the destructive operator.
type restorePayload struct {
	CopyID          string          `json:"copyId"`
	Sites           []siteSelection `json:"sites"`
	Databases       []dbSelection   `json:"databases"`
	AllowUnverified bool            `json:"allowUnverified"`
	PreviewDigest   string          `json:"previewDigest"`
}

type restorePreviewItem struct {
	Kind             string `json:"kind"`
	SourceEntry      string `json:"sourceEntry"`
	DestinationRef   string `json:"destinationRef"`
	DestinationLabel string `json:"destinationLabel"`
	Clear            bool   `json:"clear"`
	Impact           string `json:"impact"`
}

type restorePreviewResponse struct {
	CopyID         string               `json:"copyId"`
	CopyName       string               `json:"copyName"`
	IntegrityState string               `json:"integrityState"`
	Warnings       []string             `json:"warnings"`
	Items          []restorePreviewItem `json:"items"`
	PreviewToken   string               `json:"previewToken"`
	ExpiresAt      time.Time            `json:"expiresAt"`
}

type resolvedRestorePlan struct {
	choices   restoreChoices
	account   backupoperator.Account
	sites     []backupoperator.SiteRestoreTarget
	databases []backupoperator.DatabaseRestoreTarget
	preview   restorePreviewResponse
	digest    string
}

type restorePlanProblem struct {
	status  int
	code    string
	message string
	cause   error
}

func (problem *restorePlanProblem) Error() string {
	if problem.cause != nil {
		return problem.message + ": " + problem.cause.Error()
	}
	return problem.message
}

func (problem *restorePlanProblem) Unwrap() error { return problem.cause }

func restoreProblem(status int, code, message string, cause ...error) *restorePlanProblem {
	problem := &restorePlanProblem{status: status, code: code, message: message}
	if len(cause) != 0 {
		problem.cause = cause[0]
	}
	return problem
}

func writeRestoreProblem(w http.ResponseWriter, problem *restorePlanProblem) {
	httpapi.WriteError(w, problem.status, problem.code, problem.message)
}

func (m *Module) loadRestoreCopy(w http.ResponseWriter, r *http.Request) (copyModel, bool) {
	record, err := m.getCopyModel(r.Context(), r.PathValue("copyId"))
	if errors.Is(err, sql.ErrNoRows) {
		httpapi.WriteError(w, http.StatusNotFound, "backup_copy_not_found", "The requested backup copy does not exist.")
		return copyModel{}, false
	}
	if err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, "backup_copy_unavailable", "The backup copy could not be loaded.")
		return copyModel{}, false
	}
	return record, true
}

func (m *Module) previewRestoreCopyHTTP(w http.ResponseWriter, r *http.Request) {
	record, ok := m.loadRestoreCopy(w, r)
	if !ok {
		return
	}
	var choices restoreChoices
	if err := decodeJSON(w, r, &choices); err != nil {
		httpapi.WriteError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	plan, problem := m.buildRestorePlan(r.Context(), record, choices)
	if problem != nil {
		writeRestoreProblem(w, problem)
		return
	}
	expiresAt := m.now().UTC().Add(m.restorePreviewTTL).Truncate(time.Second)
	plan.preview.PreviewToken = m.signRestorePreview(restorePreviewBinding{
		CopyID: record.ID, ActorID: restoreActorID(r.Context()), Digest: plan.digest, ExpiresAt: expiresAt.Unix(),
	})
	plan.preview.ExpiresAt = expiresAt
	httpapi.WriteJSON(w, http.StatusOK, plan.preview)
}

func (m *Module) restoreCopyHTTP(w http.ResponseWriter, r *http.Request) {
	record, ok := m.loadRestoreCopy(w, r)
	if !ok {
		return
	}
	var request restoreRequest
	if err := decodeJSON(w, r, &request); err != nil {
		httpapi.WriteError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	plan, problem := m.buildRestorePlan(r.Context(), record, request.choices())
	if problem != nil {
		writeRestoreProblem(w, problem)
		return
	}
	if tokenProblem := m.verifyRestorePreview(request.PreviewToken, restorePreviewBinding{
		CopyID: record.ID, ActorID: restoreActorID(r.Context()), Digest: plan.digest,
	}); tokenProblem != nil {
		writeRestoreProblem(w, tokenProblem)
		return
	}

	var actor *string
	if user, ok := identity.UserFromContext(r.Context()); ok {
		actor = &user.ID
	}
	payload := restorePayload{
		CopyID: record.ID, Sites: plan.choices.Sites, Databases: plan.choices.Databases,
		AllowUnverified: plan.choices.AllowUnverified, PreviewDigest: plan.digest,
	}
	siteIDs := make([]string, 0, len(plan.choices.Sites))
	for _, selection := range plan.choices.Sites {
		siteIDs = append(siteIDs, selection.SiteID)
	}
	if len(plan.choices.Databases) != 0 {
		siteIDs = nil
	}
	idempotencyKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if idempotencyKey == "" {
		tokenDigest := sha256.Sum256([]byte(request.PreviewToken))
		idempotencyKey = "restore-preview-" + base64.RawURLEncoding.EncodeToString(tokenDigest[:])
	}
	job, err := m.jobs.SubmitTitledWithOptions(r.Context(), "backup.restore", "Restore backup "+record.CopyName,
		payload, jobs.SubmitOptions{
			ActorUserID: actor, SiteIDs: siteIDs, IdempotencyKey: idempotencyKey,
		})
	if err != nil {
		if errors.Is(err, jobs.ErrIdempotencyConflict) {
			httpapi.WriteError(w, http.StatusConflict, "backup_restore_idempotency_conflict", "The idempotency key was already used for a different restore request.")
			return
		}
		httpapi.WriteError(w, http.StatusInternalServerError, "backup_restore_failed", "The restore could not be queued.")
		return
	}
	w.Header().Set("Location", fmt.Sprintf("/api/v1/jobs/%d", job.ID))
	httpapi.WriteJSON(w, http.StatusAccepted, job)
}

type restorePreviewBinding struct {
	CopyID    string `json:"copyId"`
	ActorID   string `json:"actorId,omitempty"`
	Digest    string `json:"digest"`
	ExpiresAt int64  `json:"expiresAt"`
}

func restoreActorID(ctx context.Context) string {
	if user, ok := identity.UserFromContext(ctx); ok {
		return user.ID
	}
	return ""
}

func (m *Module) signRestorePreview(binding restorePreviewBinding) string {
	payload, _ := json.Marshal(binding)
	mac := hmac.New(sha256.New, m.restorePreviewKey)
	_, _ = mac.Write([]byte("backup-restore-preview-v1\x00"))
	_, _ = mac.Write(payload)
	return base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (m *Module) verifyRestorePreview(token string, expected restorePreviewBinding) *restorePlanProblem {
	parts := strings.Split(token, ".")
	if token == "" || len(token) > 2048 || len(parts) != 2 {
		return restoreProblem(http.StatusConflict, "backup_restore_preview_required", "Review the current restore plan before restoring.")
	}
	payload, payloadErr := base64.RawURLEncoding.DecodeString(parts[0])
	signature, signatureErr := base64.RawURLEncoding.DecodeString(parts[1])
	if payloadErr != nil || signatureErr != nil {
		return restoreProblem(http.StatusConflict, "backup_restore_preview_required", "Review the current restore plan before restoring.")
	}
	mac := hmac.New(sha256.New, m.restorePreviewKey)
	_, _ = mac.Write([]byte("backup-restore-preview-v1\x00"))
	_, _ = mac.Write(payload)
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return restoreProblem(http.StatusConflict, "backup_restore_preview_required", "Review the current restore plan before restoring.")
	}
	var binding restorePreviewBinding
	if err := json.Unmarshal(payload, &binding); err != nil || binding.CopyID != expected.CopyID || binding.ActorID != expected.ActorID {
		return restoreProblem(http.StatusConflict, "backup_restore_preview_required", "Review the current restore plan before restoring.")
	}
	if binding.ExpiresAt <= 0 || !m.now().UTC().Before(time.Unix(binding.ExpiresAt, 0).UTC()) {
		return restoreProblem(http.StatusConflict, "backup_restore_preview_expired", "The restore preview expired. Review the current plan again.")
	}
	if binding.Digest == "" || !hmac.Equal([]byte(binding.Digest), []byte(expected.Digest)) {
		return restoreProblem(http.StatusConflict, "backup_restore_preview_changed", "The restore plan changed. Review the current plan again.")
	}
	return nil
}

func (m *Module) buildRestorePlan(ctx context.Context, record copyModel, requested restoreChoices) (resolvedRestorePlan, *restorePlanProblem) {
	choices := restoreChoices{
		Sites: append([]siteSelection(nil), requested.Sites...), Databases: append([]dbSelection(nil), requested.Databases...),
		AllowUnverified: requested.AllowUnverified,
	}
	for index := range choices.Sites {
		choices.Sites[index].Entry = strings.TrimSpace(choices.Sites[index].Entry)
		choices.Sites[index].SiteID = strings.TrimSpace(choices.Sites[index].SiteID)
	}
	for index := range choices.Databases {
		choices.Databases[index].Entry = strings.TrimSpace(choices.Databases[index].Entry)
		choices.Databases[index].DatabaseRef = strings.TrimSpace(choices.Databases[index].DatabaseRef)
	}
	sort.Slice(choices.Sites, func(left, right int) bool {
		if choices.Sites[left].Entry == choices.Sites[right].Entry {
			return choices.Sites[left].SiteID < choices.Sites[right].SiteID
		}
		return choices.Sites[left].Entry < choices.Sites[right].Entry
	})
	sort.Slice(choices.Databases, func(left, right int) bool {
		if choices.Databases[left].Entry == choices.Databases[right].Entry {
			return choices.Databases[left].DatabaseRef < choices.Databases[right].DatabaseRef
		}
		return choices.Databases[left].Entry < choices.Databases[right].Entry
	})
	if len(choices.Sites) == 0 && len(choices.Databases) == 0 {
		return resolvedRestorePlan{}, restoreProblem(http.StatusUnprocessableEntity, "backup_restore_empty", "Choose at least one item to restore.")
	}
	if len(choices.Sites)+len(choices.Databases) > 128 {
		return resolvedRestorePlan{}, restoreProblem(http.StatusUnprocessableEntity, "backup_restore_too_many_items", "A restore can include at most 128 items.")
	}
	if record.Status != copyStatusUploaded ||
		(record.IntegrityState != integrityPassed && record.IntegrityState != integrityUnverified && record.IntegrityState != integrityFailed) ||
		(record.RestoreTestState != restoreTestNotTested && record.RestoreTestState != restoreTestPassed && record.RestoreTestState != restoreTestFailed) {
		return resolvedRestorePlan{}, restoreProblem(http.StatusConflict, "backup_restore_copy_invalid", "The backup copy state is invalid and cannot be restored.")
	}
	if record.IntegrityState != integrityPassed && !choices.AllowUnverified {
		return resolvedRestorePlan{}, restoreProblem(http.StatusConflict, "backup_restore_unverified_confirmation_required", "Confirm that you want to restore a copy whose integrity has not passed verification.")
	}

	var entries []backupoperator.CopyEntry
	if err := json.Unmarshal([]byte(record.EntriesJSON), &entries); err != nil {
		return resolvedRestorePlan{}, restoreProblem(http.StatusConflict, "backup_restore_copy_invalid", "The backup copy manifest is invalid and cannot be restored.", err)
	}
	known := make(map[string]backupoperator.CopyEntry, len(entries))
	for _, entry := range entries {
		if entry.Name == "" || strings.ContainsAny(entry.Name, "/\\") {
			return resolvedRestorePlan{}, restoreProblem(http.StatusConflict, "backup_restore_copy_invalid", "The backup copy manifest is invalid and cannot be restored.")
		}
		if _, duplicate := known[entry.Name]; duplicate {
			return resolvedRestorePlan{}, restoreProblem(http.StatusConflict, "backup_restore_copy_invalid", "The backup copy manifest contains duplicate entries and cannot be restored.")
		}
		known[entry.Name] = entry
	}

	accountModel, err := m.getAccountModel(ctx, record.AccountID)
	if err != nil {
		return resolvedRestorePlan{}, restoreProblem(http.StatusConflict, "backup_restore_source_unavailable", "The backup source account is unavailable.", err)
	}
	account, err := m.resolveAccount(*accountModel)
	if err != nil {
		return resolvedRestorePlan{}, restoreProblem(http.StatusConflict, "backup_restore_source_unavailable", "The backup source account is unavailable.", err)
	}
	plan := resolvedRestorePlan{
		choices: choices, account: account,
		sites:     make([]backupoperator.SiteRestoreTarget, 0, len(choices.Sites)),
		databases: make([]backupoperator.DatabaseRestoreTarget, 0, len(choices.Databases)),
		preview: restorePreviewResponse{
			CopyID: record.ID, CopyName: record.CopyName, IntegrityState: record.IntegrityState,
			Warnings: []string{}, Items: make([]restorePreviewItem, 0, len(choices.Sites)+len(choices.Databases)),
		},
	}
	if record.IntegrityState != integrityPassed {
		plan.preview.Warnings = append(plan.preview.Warnings, "Integrity verification has not passed for this copy.")
	}
	if record.RestoreTestState != restoreTestPassed {
		plan.preview.Warnings = append(plan.preview.Warnings, "A restore verification has not passed for this copy.")
	}

	usedEntries := make(map[string]struct{}, len(choices.Sites)+len(choices.Databases))
	usedSiteIDs := make(map[string]struct{}, len(choices.Sites))
	usedSiteTargets := make(map[string]struct{}, len(choices.Sites))
	missingChecksum := false
	for _, selection := range choices.Sites {
		entry, found := known[selection.Entry]
		if !found || !strings.HasPrefix(selection.Entry, "site-") || !strings.HasSuffix(selection.Entry, ".tar.gz") {
			return resolvedRestorePlan{}, restoreProblem(http.StatusUnprocessableEntity, "backup_restore_unknown_entry", "A selected item is not a restorable site entry in this backup copy.")
		}
		if selection.SiteID == "" {
			return resolvedRestorePlan{}, restoreProblem(http.StatusUnprocessableEntity, "backup_restore_invalid_destination", "Every selected site needs a destination.")
		}
		if _, duplicate := usedEntries[selection.Entry]; duplicate {
			return resolvedRestorePlan{}, restoreProblem(http.StatusUnprocessableEntity, "backup_restore_duplicate_entry", "Each backup item can be restored only once per request.")
		}
		if _, duplicate := usedSiteIDs[selection.SiteID]; duplicate {
			return resolvedRestorePlan{}, restoreProblem(http.StatusUnprocessableEntity, "backup_restore_duplicate_destination", "Each site can receive only one item per restore request.")
		}
		if m.sites == nil {
			return resolvedRestorePlan{}, restoreProblem(http.StatusConflict, "backup_restore_destination_unavailable", "Site restore destinations are unavailable on this node.")
		}
		target, err := m.sites.BackupSite(ctx, selection.SiteID)
		if err != nil || target.Slug == "" || target.RootPath == "" || target.UnixUser == "" {
			return resolvedRestorePlan{}, restoreProblem(http.StatusConflict, "backup_restore_destination_unavailable", "A selected site destination is unavailable.", err)
		}
		if _, duplicate := usedSiteTargets[target.RootPath]; duplicate {
			return resolvedRestorePlan{}, restoreProblem(http.StatusUnprocessableEntity, "backup_restore_duplicate_destination", "Two selections resolve to the same site destination.")
		}
		usedEntries[selection.Entry] = struct{}{}
		usedSiteIDs[selection.SiteID] = struct{}{}
		usedSiteTargets[target.RootPath] = struct{}{}
		missingChecksum = missingChecksum || entry.SHA256 == ""
		if entry.SHA256 != "" && !validRestoreSHA256(entry.SHA256) {
			return resolvedRestorePlan{}, restoreProblem(http.StatusConflict, "backup_restore_copy_invalid", "A selected backup entry has an invalid checksum and cannot be restored.")
		}
		plan.sites = append(plan.sites, backupoperator.SiteRestoreTarget{
			Entry: selection.Entry, SHA256: entry.SHA256, Target: target, Clear: selection.Clear,
		})
		impact := "Backup files may overwrite existing files; unrelated destination files are retained."
		if selection.Clear {
			impact = "All existing site files at the destination will be removed before the backup is restored."
		}
		plan.preview.Items = append(plan.preview.Items, restorePreviewItem{
			Kind: "site", SourceEntry: selection.Entry, DestinationRef: selection.SiteID,
			DestinationLabel: target.Slug, Clear: selection.Clear, Impact: impact,
		})
	}

	usedDatabaseRefs := make(map[string]struct{}, len(choices.Databases))
	usedDatabaseTargets := make(map[string]struct{}, len(choices.Databases))
	for _, selection := range choices.Databases {
		entry, found := known[selection.Entry]
		sourceFamily, validEntry := restoreDatabaseEntryFamily(selection.Entry)
		if !found || !validEntry {
			return resolvedRestorePlan{}, restoreProblem(http.StatusUnprocessableEntity, "backup_restore_unknown_entry", "A selected item is not a restorable database entry in this backup copy.")
		}
		engine, id, qualified := strings.Cut(selection.DatabaseRef, ":")
		if !qualified || id == "" || (engine != "postgres" && engine != "mysql") || engine != sourceFamily {
			return resolvedRestorePlan{}, restoreProblem(http.StatusUnprocessableEntity, "backup_restore_invalid_destination", "Every selected database needs a compatible engine-qualified destination.")
		}
		if _, duplicate := usedEntries[selection.Entry]; duplicate {
			return resolvedRestorePlan{}, restoreProblem(http.StatusUnprocessableEntity, "backup_restore_duplicate_entry", "Each backup item can be restored only once per request.")
		}
		if _, duplicate := usedDatabaseRefs[selection.DatabaseRef]; duplicate {
			return resolvedRestorePlan{}, restoreProblem(http.StatusUnprocessableEntity, "backup_restore_duplicate_destination", "Each database can receive only one item per restore request.")
		}
		target, err := m.resolveDatabaseRef(ctx, selection.DatabaseRef)
		if err != nil || target.Name == "" || !restoreTargetMatchesFamily(target.Engine, sourceFamily) {
			return resolvedRestorePlan{}, restoreProblem(http.StatusConflict, "backup_restore_destination_unavailable", "A selected database destination is unavailable.", err)
		}
		targetIdentity := fmt.Sprintf("%s:%s:%d:%s", target.Engine, target.Socket, target.Port, target.Name)
		if _, duplicate := usedDatabaseTargets[targetIdentity]; duplicate {
			return resolvedRestorePlan{}, restoreProblem(http.StatusUnprocessableEntity, "backup_restore_duplicate_destination", "Two selections resolve to the same database destination.")
		}
		usedEntries[selection.Entry] = struct{}{}
		usedDatabaseRefs[selection.DatabaseRef] = struct{}{}
		usedDatabaseTargets[targetIdentity] = struct{}{}
		missingChecksum = missingChecksum || entry.SHA256 == ""
		if entry.SHA256 != "" && !validRestoreSHA256(entry.SHA256) {
			return resolvedRestorePlan{}, restoreProblem(http.StatusConflict, "backup_restore_copy_invalid", "A selected backup entry has an invalid checksum and cannot be restored.")
		}
		plan.databases = append(plan.databases, backupoperator.DatabaseRestoreTarget{
			Entry: selection.Entry, SHA256: entry.SHA256, Target: target, Clear: selection.Clear,
		})
		impact := "The backup dump will be replayed into the destination; engine rollback protection is prepared first."
		if selection.Clear {
			impact = "The destination database will be reset before the backup dump is replayed; engine rollback protection is prepared first."
		}
		plan.preview.Items = append(plan.preview.Items, restorePreviewItem{
			Kind: "database", SourceEntry: selection.Entry, DestinationRef: selection.DatabaseRef,
			DestinationLabel: target.Name + " · " + restoreEngineLabel(target.Engine), Clear: selection.Clear, Impact: impact,
		})
	}
	if missingChecksum {
		plan.preview.Warnings = append(plan.preview.Warnings, "At least one selected backup entry has no recorded SHA-256 checksum.")
	}
	if err := backupoperator.ValidateRestoreRequest(backupoperator.RestoreRequest{
		PlanID: record.PlanID, CopyName: record.CopyName, Sites: plan.sites, Databases: plan.databases,
	}); err != nil {
		return resolvedRestorePlan{}, restoreProblem(http.StatusConflict, "backup_restore_plan_invalid", "The resolved restore plan is not safe to execute.", err)
	}

	digest, err := digestRestorePlan(record, *accountModel, plan)
	if err != nil {
		return resolvedRestorePlan{}, restoreProblem(http.StatusInternalServerError, "backup_restore_preview_failed", "The restore preview could not be created.", err)
	}
	plan.digest = digest
	return plan, nil
}

func restoreDatabaseEntryFamily(entry string) (string, bool) {
	switch {
	case strings.HasPrefix(entry, "db-postgres-") && strings.HasSuffix(entry, ".dump"):
		return "postgres", true
	case (strings.HasPrefix(entry, "db-mysql-") || strings.HasPrefix(entry, "db-mariadb-")) && strings.HasSuffix(entry, ".sql"):
		return "mysql", true
	default:
		return "", false
	}
}

func restoreTargetMatchesFamily(engine, family string) bool {
	return (family == "postgres" && engine == "postgres") || (family == "mysql" && (engine == "mysql" || engine == "mariadb"))
}

func restoreEngineLabel(engine string) string {
	switch engine {
	case "postgres":
		return "PostgreSQL"
	case "mariadb":
		return "MariaDB"
	default:
		return "MySQL"
	}
}

func validRestoreSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') && (character < 'A' || character > 'F') {
			return false
		}
	}
	return true
}

func digestRestorePlan(record copyModel, account accountModel, plan resolvedRestorePlan) (string, error) {
	secretCiphertext := ""
	if account.SecretCiphertext != nil {
		secretCiphertext = *account.SecretCiphertext
	}
	canonical := struct {
		Version int `json:"version"`
		Copy    struct {
			ID, PlanID, AccountID, CopyName, RemotePath, EntriesJSON, Status, IntegrityState, RestoreTestState string
			CreatedAt                                                                                          time.Time
		} `json:"copy"`
		Account struct {
			ID, Name, Type, Path, ConfigJSON, SecretCiphertext string
			UpdatedAt                                          time.Time
		} `json:"account"`
		Choices   restoreChoices                         `json:"choices"`
		Sites     []backupoperator.SiteRestoreTarget     `json:"sites"`
		Databases []backupoperator.DatabaseRestoreTarget `json:"databases"`
	}{Version: 1, Choices: plan.choices, Sites: plan.sites, Databases: plan.databases}
	canonical.Copy.ID = record.ID
	canonical.Copy.PlanID = record.PlanID
	canonical.Copy.AccountID = record.AccountID
	canonical.Copy.CopyName = record.CopyName
	canonical.Copy.RemotePath = record.RemotePath
	canonical.Copy.EntriesJSON = record.EntriesJSON
	canonical.Copy.Status = record.Status
	canonical.Copy.IntegrityState = record.IntegrityState
	canonical.Copy.RestoreTestState = record.RestoreTestState
	canonical.Copy.CreatedAt = record.CreatedAt.UTC()
	canonical.Account.ID = account.ID
	canonical.Account.Name = account.Name
	canonical.Account.Type = account.Type
	canonical.Account.Path = account.Path
	canonical.Account.ConfigJSON = account.ConfigJSON
	canonical.Account.SecretCiphertext = secretCiphertext
	canonical.Account.UpdatedAt = account.UpdatedAt.UTC()
	payload, err := json.Marshal(canonical)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(payload)
	return base64.RawURLEncoding.EncodeToString(digest[:]), nil
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
	_ = report(10, "Resolving restore destinations")
	plan, problem := m.buildRestorePlan(ctx, record, restoreChoices{
		Sites: payload.Sites, Databases: payload.Databases, AllowUnverified: payload.AllowUnverified,
	})
	if problem != nil {
		return nil, problem
	}
	if payload.PreviewDigest == "" || !hmac.Equal([]byte(payload.PreviewDigest), []byte(plan.digest)) {
		return nil, errors.New("restore plan changed after preview; review it again before restoring")
	}

	_ = report(30, "Restoring "+record.CopyName)
	if err := m.operator.Restore(ctx, backupoperator.RestoreRequest{
		Account: plan.account, PlanID: record.PlanID, CopyName: record.CopyName,
		Sites: plan.sites, Databases: plan.databases,
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
		httpapi.WriteError(w, http.StatusNotFound, "backup_copy_not_found", "The requested backup copy does not exist.")
		return
	}
	if err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, "backup_copy_unavailable", "The backup copy could not be loaded.")
		return
	}
	accountModel, err := m.getAccountModel(r.Context(), record.AccountID)
	if err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, "backup_account_unavailable", "The backup account could not be loaded.")
		return
	}
	account, err := m.resolveAccount(*accountModel)
	if err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, "backup_account_unavailable", "The backup account could not be resolved.")
		return
	}
	if err := m.operator.DeleteCopy(r.Context(), backupoperator.DeleteRequest{
		Account: account, PlanID: record.PlanID, CopyName: record.CopyName,
	}); err != nil {
		httpapi.WriteError(w, http.StatusBadGateway, "backup_copy_delete_failed", "The backup copy could not be removed from storage.")
		return
	}
	if _, err := m.database.NewDelete().Model((*copyModel)(nil)).Where("id = ?", record.ID).Exec(r.Context()); err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, "backup_copy_delete_failed", "The backup copy was removed from storage but its record could not be cleared.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
