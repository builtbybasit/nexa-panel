package backups

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/uptrace/bun"

	backupoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/backups"
)

// syncSchedule installs or removes a plan's systemd timer to match its enabled
// state. It is best-effort: the enabled intent is already persisted, so a timer
// hiccup (agent unreachable, systemd busy) is logged, not fatal — the next edit
// or toggle re-syncs. Scheduling never blocks plan CRUD.
func (m *Module) syncSchedule(ctx context.Context, plan planModel) {
	var err error
	if plan.Enabled {
		err = m.operator.InstallSchedule(ctx, backupoperator.ScheduleSpec{
			PlanID: plan.ID, PlanName: plan.Name, Cron: plan.Schedule, StateDBPath: m.stateDBPath,
		})
	} else {
		err = m.operator.RemoveSchedule(ctx, plan.ID)
	}
	if err != nil {
		m.logger.Warn("backup schedule sync failed", "plan", plan.ID, "enabled", plan.Enabled, "error", err)
	}
}

func (m *Module) removeSchedule(ctx context.Context, planID string) {
	if err := m.operator.RemoveSchedule(ctx, planID); err != nil {
		m.logger.Warn("backup schedule removal failed", "plan", planID, "error", err)
	}
}

// plansSchema is migration #2 for the "backups" module (appended after
// accountsSchema — never reordered). A plan names what to back up (sites and/or
// databases), where (account_id), how many copies to keep (copies_limit), and
// when (schedule, a 5-field cron expression). Execution and the systemd timer
// that fires it arrive in Phase 3.
const plansSchema = `
	CREATE TABLE backup_plans (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		account_id TEXT NOT NULL REFERENCES backup_accounts(id),
		copies_limit INTEGER NOT NULL,
		site_ids TEXT NOT NULL,
		database_ids TEXT NOT NULL,
		schedule TEXT NOT NULL,
		enabled INTEGER NOT NULL,
		created_at TIMESTAMP NOT NULL,
		updated_at TIMESTAMP NOT NULL
	);
	CREATE INDEX idx_backup_plans_account ON backup_plans(account_id);
`

const maxCopiesLimit = 1000

// Plan is the JSON-facing view of a backup plan. Database references are
// engine-qualified ("postgres:<id>" / "mysql:<id>") so Phase 3 knows which tool
// to dump each one with.
type Plan struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	AccountID   string    `json:"accountId"`
	CopiesLimit int       `json:"copiesLimit"`
	SiteIDs     []string  `json:"siteIds"`
	DatabaseIDs []string  `json:"databaseIds"`
	Schedule    string    `json:"schedule"`
	Enabled     bool      `json:"enabled"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

type PlanRequest struct {
	Name        string   `json:"name"`
	AccountID   string   `json:"accountId"`
	CopiesLimit int      `json:"copiesLimit"`
	SiteIDs     []string `json:"siteIds"`
	DatabaseIDs []string `json:"databaseIds"`
	Schedule    string   `json:"schedule"`
	Enabled     bool     `json:"enabled"`
}

type toggleRequest struct {
	Enabled bool `json:"enabled"`
}

type planModel struct {
	bun.BaseModel   `bun:"table:backup_plans,alias:backup_plan"`
	ID              string `bun:",pk"`
	Name            string
	AccountID       string
	CopiesLimit     int
	SiteIDsJSON     string `bun:"site_ids"`
	DatabaseIDsJSON string `bun:"database_ids"`
	Schedule        string
	Enabled         bool
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

func (model planModel) toPlan() Plan {
	return Plan{
		ID: model.ID, Name: model.Name, AccountID: model.AccountID, CopiesLimit: model.CopiesLimit,
		SiteIDs: decodeStringSlice(model.SiteIDsJSON), DatabaseIDs: decodeStringSlice(model.DatabaseIDsJSON),
		Schedule: model.Schedule, Enabled: model.Enabled, CreatedAt: model.CreatedAt, UpdatedAt: model.UpdatedAt,
	}
}

func decodeStringSlice(encoded string) []string {
	values := []string{}
	if encoded != "" {
		_ = json.Unmarshal([]byte(encoded), &values)
	}
	return values
}

// cronFieldPattern accepts the token vocabulary a 5-field cron entry uses: the
// wildcard, numbers, and lists/ranges/steps built from them. It is deliberately
// permissive about ranges rather than range-checking each field's bounds.
var cronFieldPattern = regexp.MustCompile(`^[0-9*/,\-]+$`)

func validatePlan(request PlanRequest) error {
	if name := strings.TrimSpace(request.Name); name == "" || len(name) > 80 {
		return errors.New("name must contain 1-80 characters")
	}
	if strings.TrimSpace(request.AccountID) == "" {
		return errors.New("a backup account is required")
	}
	if request.CopiesLimit < 1 || request.CopiesLimit > maxCopiesLimit {
		return errors.New("the number of backup copies to keep must be between 1 and 1000")
	}
	if len(request.SiteIDs) == 0 && len(request.DatabaseIDs) == 0 {
		return errors.New("select at least one site or database to back up")
	}
	if err := validateCron(request.Schedule); err != nil {
		return err
	}
	return nil
}

func validateCron(expression string) error {
	fields := strings.Fields(strings.TrimSpace(expression))
	if len(fields) != 5 {
		return errors.New("the schedule must be a 5-field cron expression (minute hour day month weekday)")
	}
	for _, field := range fields {
		if !cronFieldPattern.MatchString(field) {
			return errors.New("the schedule contains an invalid cron field")
		}
	}
	return nil
}

// --- lifecycle -------------------------------------------------------------

func (m *Module) CreatePlan(ctx context.Context, request PlanRequest) (Plan, error) {
	if err := validatePlan(request); err != nil {
		return Plan{}, validationError{err.Error()}
	}
	if err := m.assertAccountExists(ctx, request.AccountID); err != nil {
		return Plan{}, err
	}
	siteIDs, databaseIDs := encodeSlice(request.SiteIDs), encodeSlice(request.DatabaseIDs)
	now := m.now().UTC()
	model := &planModel{
		ID: "bkplan_" + randomToken(), Name: strings.TrimSpace(request.Name), AccountID: request.AccountID,
		CopiesLimit: request.CopiesLimit, SiteIDsJSON: siteIDs, DatabaseIDsJSON: databaseIDs,
		Schedule: strings.TrimSpace(request.Schedule), Enabled: request.Enabled, CreatedAt: now, UpdatedAt: now,
	}
	if _, err := m.database.NewInsert().Model(model).Exec(ctx); err != nil {
		return Plan{}, err
	}
	m.syncSchedule(ctx, *model)
	return model.toPlan(), nil
}

func (m *Module) UpdatePlan(ctx context.Context, id string, request PlanRequest) (Plan, error) {
	model := new(planModel)
	if err := m.database.NewSelect().Model(model).Where("id = ?", strings.TrimSpace(id)).Scan(ctx); err != nil {
		return Plan{}, err
	}
	if err := validatePlan(request); err != nil {
		return Plan{}, validationError{err.Error()}
	}
	if err := m.assertAccountExists(ctx, request.AccountID); err != nil {
		return Plan{}, err
	}
	model.Name = strings.TrimSpace(request.Name)
	model.AccountID = request.AccountID
	model.CopiesLimit = request.CopiesLimit
	model.SiteIDsJSON = encodeSlice(request.SiteIDs)
	model.DatabaseIDsJSON = encodeSlice(request.DatabaseIDs)
	model.Schedule = strings.TrimSpace(request.Schedule)
	model.Enabled = request.Enabled
	model.UpdatedAt = m.now().UTC()
	if _, err := m.database.NewUpdate().Model(model).WherePK().Exec(ctx); err != nil {
		return Plan{}, err
	}
	m.syncSchedule(ctx, *model)
	return model.toPlan(), nil
}

func (m *Module) assertAccountExists(ctx context.Context, accountID string) error {
	exists, err := m.database.NewSelect().Model((*accountModel)(nil)).Where("id = ?", accountID).Exists(ctx)
	if err != nil {
		return err
	}
	if !exists {
		return validationError{"the selected backup account no longer exists"}
	}
	return nil
}

// --- handlers --------------------------------------------------------------

func (m *Module) listPlansHTTP(w http.ResponseWriter, r *http.Request) {
	var models []planModel
	if err := m.database.NewSelect().Model(&models).Order("name ASC").Scan(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "backup_plans_unavailable", "The backup plans could not be loaded.")
		return
	}
	plans := make([]Plan, 0, len(models))
	for _, model := range models {
		plans = append(plans, model.toPlan())
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": plans})
}

func (m *Module) createPlanHTTP(w http.ResponseWriter, r *http.Request) {
	var request PlanRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	plan, err := m.CreatePlan(r.Context(), request)
	if err != nil {
		writePlanError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, plan)
}

func (m *Module) getPlanHTTP(w http.ResponseWriter, r *http.Request) {
	model := new(planModel)
	err := m.database.NewSelect().Model(model).Where("id = ?", r.PathValue("id")).Scan(r.Context())
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "backup_plan_not_found", "The requested backup plan does not exist.")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "backup_plan_unavailable", "The backup plan could not be loaded.")
		return
	}
	writeJSON(w, http.StatusOK, model.toPlan())
}

func (m *Module) updatePlanHTTP(w http.ResponseWriter, r *http.Request) {
	var request PlanRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	plan, err := m.UpdatePlan(r.Context(), r.PathValue("id"), request)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "backup_plan_not_found", "The requested backup plan does not exist.")
		return
	}
	if err != nil {
		writePlanError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, plan)
}

func (m *Module) togglePlanHTTP(w http.ResponseWriter, r *http.Request) {
	var request toggleRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	result, err := m.database.NewUpdate().Model((*planModel)(nil)).
		Set("enabled = ?", request.Enabled).Set("updated_at = ?", m.now().UTC()).
		Where("id = ?", r.PathValue("id")).Exec(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "backup_plan_update_failed", "The backup plan could not be updated.")
		return
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		writeError(w, http.StatusNotFound, "backup_plan_not_found", "The requested backup plan does not exist.")
		return
	}
	// Reflect the new enabled state in the installed timer.
	plan := new(planModel)
	if err := m.database.NewSelect().Model(plan).Where("id = ?", r.PathValue("id")).Scan(r.Context()); err == nil {
		m.syncSchedule(r.Context(), *plan)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (m *Module) deletePlanHTTP(w http.ResponseWriter, r *http.Request) {
	result, err := m.database.NewDelete().Model((*planModel)(nil)).Where("id = ?", r.PathValue("id")).Exec(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "backup_plan_delete_failed", "The backup plan could not be removed.")
		return
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		writeError(w, http.StatusNotFound, "backup_plan_not_found", "The requested backup plan does not exist.")
		return
	}
	m.removeSchedule(r.Context(), r.PathValue("id"))
	w.WriteHeader(http.StatusNoContent)
}

func writePlanError(w http.ResponseWriter, err error) {
	var invalid validationError
	if errors.As(err, &invalid) {
		writeError(w, http.StatusUnprocessableEntity, "backup_plan_invalid", invalid.message)
		return
	}
	writeError(w, http.StatusInternalServerError, "backup_plan_failed", "The backup plan could not be saved.")
}

func encodeSlice(values []string) string {
	if values == nil {
		values = []string{}
	}
	encoded, _ := json.Marshal(values)
	return string(encoded)
}
