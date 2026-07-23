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

const (
	copyStatusUploaded   = "uploaded"
	integrityUnverified  = "unverified"
	integrityPassed      = "passed"
	integrityFailed      = "failed"
	restoreTestNotTested = "not_tested"
	restoreTestPassed    = "passed"
	restoreTestFailed    = "failed"
)

// Copy is the JSON-facing view of a stored backup copy.
type Copy struct {
	ID                 string                     `json:"id"`
	PlanID             string                     `json:"planId"`
	AccountID          string                     `json:"accountId"`
	CopyName           string                     `json:"copyName"`
	RemotePath         string                     `json:"remotePath"`
	SizeBytes          int64                      `json:"sizeBytes"`
	Entries            []backupoperator.CopyEntry `json:"entries"`
	Status             string                     `json:"status"`
	IntegrityState     string                     `json:"integrityState"`
	IntegrityCheckedAt *time.Time                 `json:"integrityCheckedAt,omitempty"`
	RestoreTestState   string                     `json:"restoreTestState"`
	RestoreTestedAt    *time.Time                 `json:"restoreTestedAt,omitempty"`
	Healthy            bool                       `json:"healthy"`
	HealthError        string                     `json:"healthError,omitempty"`
	CreatedAt          time.Time                  `json:"createdAt"`
}

type copyModel struct {
	bun.BaseModel      `bun:"table:backup_copies,alias:backup_copy"`
	ID                 string `bun:",pk"`
	PlanID             string
	AccountID          string
	CopyName           string
	RemotePath         string
	SizeBytes          int64
	EntriesJSON        string `bun:"entries"`
	Status             string
	IntegrityState     string
	IntegrityCheckedAt *time.Time
	RestoreTestState   string
	RestoreTestedAt    *time.Time
	HealthError        *string
	CreatedAt          time.Time
}

func (model copyModel) toCopy() Copy {
	entries := []backupoperator.CopyEntry{}
	if model.EntriesJSON != "" {
		_ = json.Unmarshal([]byte(model.EntriesJSON), &entries)
	}
	result := Copy{
		ID: model.ID, PlanID: model.PlanID, AccountID: model.AccountID, CopyName: model.CopyName,
		RemotePath: model.RemotePath, SizeBytes: model.SizeBytes, Entries: entries, Status: model.Status,
		IntegrityState: model.IntegrityState, IntegrityCheckedAt: model.IntegrityCheckedAt,
		RestoreTestState: model.RestoreTestState, RestoreTestedAt: model.RestoreTestedAt,
		Healthy:   model.IntegrityState == integrityPassed && model.RestoreTestState == restoreTestPassed,
		CreatedAt: model.CreatedAt,
	}
	if model.HealthError != nil {
		result.HealthError = *model.HealthError
	}
	return result
}

type runPayload struct {
	PlanID string `json:"planId"`
}

func jobSiteScope(plan *planModel) []string {
	if len(decodeStringSlice(plan.DatabaseIDsJSON)) != 0 {
		return nil
	}
	return decodeStringSlice(plan.SiteIDsJSON)
}

// runJob executes a plan: resolve its account and targets, ask the agent to
// build and ship a copy, then record it. It is the single execution path shared
// by the manual "Back up now" button and the scheduled `nexa backup trigger`.
func (m *Module) runJob(ctx context.Context, request json.RawMessage, report func(int, string) error) (any, error) {
	var payload runPayload
	if err := json.Unmarshal(request, &payload); err != nil {
		return nil, err
	}
	plan := new(planModel)
	if err := m.database.NewSelect().Model(plan).Where("id = ?", payload.PlanID).Scan(ctx); err != nil {
		return nil, fmt.Errorf("load backup plan: %w", err)
	}
	accountModel, err := m.getAccountModel(ctx, plan.AccountID)
	if err != nil {
		return nil, fmt.Errorf("load backup account: %w", err)
	}
	account, err := m.resolveAccount(*accountModel)
	if err != nil {
		return nil, err
	}

	_ = report(10, "Resolving targets")
	sites, err := m.resolveSites(ctx, decodeStringSlice(plan.SiteIDsJSON))
	if err != nil {
		return nil, err
	}
	databases, err := m.resolveDatabases(ctx, decodeStringSlice(plan.DatabaseIDsJSON))
	if err != nil {
		return nil, err
	}

	copyName := m.now().UTC().Format("2006-01-02_150405000000000Z") + "_" + randomToken()[:8]
	_ = report(25, "Backing up "+plan.Name)
	manifest, err := m.operator.Run(ctx, backupoperator.RunRequest{
		Account: account, PlanID: plan.ID, CopyName: copyName, Limit: plan.CopiesLimit,
		Sites: sites, Databases: databases,
	})
	if err != nil {
		return nil, err
	}
	if !manifest.IntegrityChecked {
		return nil, errors.New("backup operator returned an upload without an integrity check")
	}

	_ = report(90, "Recording copy")
	if err := m.recordCopy(ctx, plan, manifest); err != nil {
		return nil, err
	}
	return manifest, nil
}

func (m *Module) resolveSites(ctx context.Context, ids []string) ([]backupoperator.SiteTarget, error) {
	targets := make([]backupoperator.SiteTarget, 0, len(ids))
	for _, id := range ids {
		if m.sites == nil {
			return nil, errors.New("site backups are unavailable on this node")
		}
		target, err := m.sites.BackupSite(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("resolve site %s: %w", id, err)
		}
		targets = append(targets, target)
	}
	return targets, nil
}

func (m *Module) resolveDatabases(ctx context.Context, refs []string) ([]backupoperator.DatabaseTarget, error) {
	targets := make([]backupoperator.DatabaseTarget, 0, len(refs))
	for _, ref := range refs {
		target, err := m.resolveDatabaseRef(ctx, ref)
		if err != nil {
			return nil, err
		}
		targets = append(targets, target)
	}
	return targets, nil
}

// resolveDatabaseRef maps one "engine:id" reference to a dump/restore target.
func (m *Module) resolveDatabaseRef(ctx context.Context, ref string) (backupoperator.DatabaseTarget, error) {
	engine, id, ok := strings.Cut(ref, ":")
	if !ok {
		return backupoperator.DatabaseTarget{}, fmt.Errorf("invalid database reference %q", ref)
	}
	var resolver DatabaseResolver
	switch engine {
	case "postgres":
		resolver = m.postgres
	case "mysql":
		resolver = m.mysql
	default:
		return backupoperator.DatabaseTarget{}, fmt.Errorf("unknown database engine %q", engine)
	}
	if resolver == nil {
		return backupoperator.DatabaseTarget{}, fmt.Errorf("%s backups are unavailable on this node", engine)
	}
	target, err := resolver.BackupDatabase(ctx, id)
	if err != nil {
		return backupoperator.DatabaseTarget{}, fmt.Errorf("resolve database %s: %w", ref, err)
	}
	return target, nil
}

func (m *Module) getCopyModel(ctx context.Context, id string) (copyModel, error) {
	var model copyModel
	err := m.database.NewSelect().Model(&model).Where("id = ?", strings.TrimSpace(id)).Scan(ctx)
	return model, err
}

// recordCopy inserts the new copy and drops rows for copies the operator pruned
// remotely, keeping the recorded history aligned with what is actually stored.
func (m *Module) recordCopy(ctx context.Context, plan *planModel, manifest backupoperator.Manifest) error {
	entries, err := json.Marshal(manifest.Entries)
	if err != nil {
		return err
	}
	now := m.now().UTC()
	model := &copyModel{
		ID: "bkcopy_" + randomToken(), PlanID: plan.ID, AccountID: plan.AccountID,
		CopyName: manifest.CopyName, RemotePath: manifest.RemotePath, SizeBytes: manifest.SizeBytes,
		EntriesJSON: string(entries), Status: copyStatusUploaded,
		IntegrityState: integrityPassed, IntegrityCheckedAt: &now,
		RestoreTestState: restoreTestNotTested, CreatedAt: now,
	}
	if _, err := m.database.NewInsert().Model(model).Exec(ctx); err != nil {
		return err
	}
	if len(manifest.Pruned) > 0 {
		_, _ = m.database.NewDelete().Model((*copyModel)(nil)).
			Where("plan_id = ?", plan.ID).Where("copy_name IN (?)", bun.List(manifest.Pruned)).Exec(ctx)
	}
	return nil
}

func (m *Module) runPlanHTTP(w http.ResponseWriter, r *http.Request) {
	plan := new(planModel)
	err := m.database.NewSelect().Model(plan).Where("id = ?", r.PathValue("id")).Scan(r.Context())
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "backup_plan_not_found", "The requested backup plan does not exist.")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "backup_plan_unavailable", "The backup plan could not be loaded.")
		return
	}
	var actor *string
	if user, ok := identity.UserFromContext(r.Context()); ok {
		actor = &user.ID
	}
	job, err := m.jobs.SubmitTitledWithOptions(r.Context(), "backup.run", "Back up "+plan.Name,
		runPayload{PlanID: plan.ID}, jobs.SubmitOptions{
			ActorUserID: actor, SiteIDs: jobSiteScope(plan),
			IdempotencyKey: r.Header.Get("Idempotency-Key"),
		})
	if err != nil {
		if errors.Is(err, jobs.ErrIdempotencyConflict) {
			writeError(w, http.StatusConflict, "backup_run_idempotency_conflict", "The idempotency key was already used for a different backup request.")
			return
		}
		writeError(w, http.StatusInternalServerError, "backup_run_failed", "The backup could not be queued.")
		return
	}
	w.Header().Set("Location", fmt.Sprintf("/api/v1/jobs/%d", job.ID))
	writeJSON(w, http.StatusAccepted, job)
}

func (m *Module) listCopiesHTTP(w http.ResponseWriter, r *http.Request) {
	user, ok := identity.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication_required", "Sign in to continue.")
		return
	}
	if _, err := m.getPlanForUser(r.Context(), user, r.PathValue("id")); errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "backup_plan_not_found", "The requested backup plan does not exist.")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "backup_plan_unavailable", "The backup plan could not be loaded.")
		return
	}
	var models []copyModel
	if err := m.database.NewSelect().Model(&models).Where("plan_id = ?", r.PathValue("id")).Order("copy_name DESC").Scan(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "backup_copies_unavailable", "The backup copies could not be loaded.")
		return
	}
	copies := make([]Copy, 0, len(models))
	for _, model := range models {
		copies = append(copies, model.toCopy())
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": copies})
}
