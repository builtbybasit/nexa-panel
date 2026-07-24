package backups

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/uptrace/bun"

	"github.com/nexa-panel/nexa-panel/internal/platform/httpapi"
	"github.com/nexa-panel/nexa-panel/internal/platform/identity"
	backupoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/backups"
)

const (
	schedulePending   = "pending"
	scheduleInstalled = "installed"
	scheduleDisabled  = "disabled"
	scheduleError     = "error"
)

// syncSchedule converges one persisted desired state and records the observed
// result. Callers never report a plan as installed merely because enabled=true.
func (m *Module) syncSchedule(ctx context.Context, plan *planModel) error {
	m.scheduleSyncMu.Lock()
	defer m.scheduleSyncMu.Unlock()
	current := new(planModel)
	if err := m.database.NewSelect().Model(current).Where("id = ?", plan.ID).Scan(ctx); err != nil {
		return err
	}
	*plan = *current
	var err error
	if plan.Enabled {
		err = m.operator.InstallSchedule(ctx, backupoperator.ScheduleSpec{
			PlanID: plan.ID, PlanName: plan.Name, Cron: plan.Schedule, StateDBPath: m.stateDBPath,
		})
	} else {
		err = m.operator.RemoveSchedule(ctx, plan.ID)
	}
	now := m.now().UTC()
	state := scheduleInstalled
	if !plan.Enabled {
		state = scheduleDisabled
	}
	if err != nil {
		state = scheduleError
		message := err.Error()
		if len(message) > 500 {
			message = message[:500]
		}
		plan.ScheduleError = &message
	} else {
		plan.ScheduleError = nil
		plan.ScheduleSyncedAt = &now
	}
	plan.ScheduleState = state
	plan.UpdatedAt = now
	_, persistErr := m.database.NewUpdate().Model(plan).
		Column("schedule_state", "schedule_error", "schedule_synced_at", "updated_at").WherePK().Exec(ctx)
	if persistErr != nil {
		return errors.Join(err, fmt.Errorf("persist schedule state: %w", persistErr))
	}
	return err
}

func (m *Module) reconcileSchedules(ctx context.Context, force bool) error {
	models := make([]planModel, 0)
	query := m.database.NewSelect().Model(&models).OrderExpr("id")
	if !force {
		query = query.Where("schedule_state IN (?, ?)", schedulePending, scheduleError)
	}
	if err := query.Scan(ctx); err != nil {
		return err
	}
	var failures []error
	for index := range models {
		if err := m.syncSchedule(ctx, &models[index]); errors.Is(err, sql.ErrNoRows) {
			continue
		} else if err != nil {
			failures = append(failures, fmt.Errorf("plan %s: %w", models[index].ID, err))
		}
	}
	return errors.Join(failures...)
}

const maxCopiesLimit = 1000

const maxPlanTargets = 128

var (
	resourceIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)
	errPlanHasCopies  = errors.New("backup plan still has stored copies")
)

type scheduleRemovalError struct{ cause error }

func (err scheduleRemovalError) Error() string { return err.cause.Error() }
func (err scheduleRemovalError) Unwrap() error { return err.cause }

// Plan is the JSON-facing view of a backup plan. Database references are
// engine-qualified ("postgres:<id>" / "mysql:<id>") so execution selects the
// correct engine operator without coupling this module to its implementation.
type Plan struct {
	ID               string     `json:"id"`
	Name             string     `json:"name"`
	AccountID        string     `json:"accountId"`
	CopiesLimit      int        `json:"copiesLimit"`
	SiteIDs          []string   `json:"siteIds"`
	DatabaseIDs      []string   `json:"databaseIds"`
	Schedule         string     `json:"schedule"`
	Enabled          bool       `json:"enabled"`
	ScheduleState    string     `json:"scheduleState"`
	ScheduleError    string     `json:"scheduleError,omitempty"`
	ScheduleSyncedAt *time.Time `json:"scheduleSyncedAt,omitempty"`
	CreatedAt        time.Time  `json:"createdAt"`
	UpdatedAt        time.Time  `json:"updatedAt"`
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
	bun.BaseModel    `bun:"table:backup_plans,alias:backup_plan"`
	ID               string `bun:",pk"`
	Name             string
	AccountID        string
	CopiesLimit      int
	SiteIDsJSON      string `bun:"site_ids"`
	DatabaseIDsJSON  string `bun:"database_ids"`
	Schedule         string
	Enabled          bool
	ScheduleState    string
	ScheduleError    *string
	ScheduleSyncedAt *time.Time
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

func (model planModel) toPlan() Plan {
	result := Plan{
		ID: model.ID, Name: model.Name, AccountID: model.AccountID, CopiesLimit: model.CopiesLimit,
		SiteIDs: decodeStringSlice(model.SiteIDsJSON), DatabaseIDs: decodeStringSlice(model.DatabaseIDsJSON),
		Schedule: model.Schedule, Enabled: model.Enabled, ScheduleState: model.ScheduleState,
		ScheduleSyncedAt: model.ScheduleSyncedAt, CreatedAt: model.CreatedAt, UpdatedAt: model.UpdatedAt,
	}
	if model.ScheduleError != nil {
		result.ScheduleError = *model.ScheduleError
	}
	return result
}

func decodeStringSlice(encoded string) []string {
	values := []string{}
	if encoded != "" {
		_ = json.Unmarshal([]byte(encoded), &values)
	}
	return values
}

func validatePlan(request PlanRequest) error {
	if name := strings.TrimSpace(request.Name); name == "" || len(name) > 80 || strings.IndexFunc(name, unicode.IsControl) >= 0 {
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
	if len(request.SiteIDs)+len(request.DatabaseIDs) > maxPlanTargets {
		return fmt.Errorf("a backup plan can select at most %d targets", maxPlanTargets)
	}
	if err := validateCron(request.Schedule); err != nil {
		return err
	}
	return nil
}

func normalizePlanRequest(request PlanRequest) (PlanRequest, error) {
	request.Name = strings.TrimSpace(request.Name)
	request.AccountID = strings.TrimSpace(request.AccountID)
	request.Schedule = strings.TrimSpace(request.Schedule)

	sites, err := normalizeTargetIDs(request.SiteIDs, "site")
	if err != nil {
		return PlanRequest{}, err
	}
	databases := make([]string, 0, len(request.DatabaseIDs))
	seenDatabases := make(map[string]struct{}, len(request.DatabaseIDs))
	for _, raw := range request.DatabaseIDs {
		engine, id, ok := strings.Cut(strings.TrimSpace(raw), ":")
		engine = strings.ToLower(engine)
		if !ok || strings.Contains(id, ":") || (engine != "postgres" && engine != "mysql") || !resourceIDPattern.MatchString(id) {
			return PlanRequest{}, fmt.Errorf("database target %q must be an engine-qualified PostgreSQL or MySQL identifier", raw)
		}
		qualified := engine + ":" + id
		if _, duplicate := seenDatabases[qualified]; duplicate {
			return PlanRequest{}, fmt.Errorf("database target %q is selected more than once", qualified)
		}
		seenDatabases[qualified] = struct{}{}
		databases = append(databases, qualified)
	}
	request.SiteIDs = sites
	request.DatabaseIDs = databases
	return request, nil
}

func normalizeTargetIDs(values []string, label string) ([]string, error) {
	normalized := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, raw := range values {
		value := strings.TrimSpace(raw)
		if !resourceIDPattern.MatchString(value) {
			return nil, fmt.Errorf("%s target %q is not a valid resource identifier", label, raw)
		}
		if _, duplicate := seen[value]; duplicate {
			return nil, fmt.Errorf("%s target %q is selected more than once", label, value)
		}
		seen[value] = struct{}{}
		normalized = append(normalized, value)
	}
	return normalized, nil
}

func validateCron(expression string) error {
	if _, err := backupoperator.ValidateSchedule(expression); err != nil {
		return fmt.Errorf("invalid backup schedule: %w", err)
	}
	return nil
}

// --- lifecycle -------------------------------------------------------------

func (m *Module) CreatePlan(ctx context.Context, request PlanRequest) (Plan, error) {
	var err error
	request, err = normalizePlanRequest(request)
	if err != nil {
		return Plan{}, validationError{err.Error()}
	}
	if err := validatePlan(request); err != nil {
		return Plan{}, validationError{err.Error()}
	}
	if err := m.assertAccountExists(ctx, request.AccountID); err != nil {
		return Plan{}, err
	}
	if err := m.validatePlanTargets(ctx, request); err != nil {
		return Plan{}, validationError{err.Error()}
	}
	siteIDs, databaseIDs := encodeSlice(request.SiteIDs), encodeSlice(request.DatabaseIDs)
	now := m.now().UTC()
	model := &planModel{
		ID: "bkplan_" + randomToken(), Name: strings.TrimSpace(request.Name), AccountID: request.AccountID,
		CopiesLimit: request.CopiesLimit, SiteIDsJSON: siteIDs, DatabaseIDsJSON: databaseIDs,
		Schedule: strings.TrimSpace(request.Schedule), Enabled: request.Enabled,
		ScheduleState: schedulePending, CreatedAt: now, UpdatedAt: now,
	}
	if _, err := m.database.NewInsert().Model(model).Exec(ctx); err != nil {
		return Plan{}, err
	}
	if err := m.syncSchedule(ctx, model); err != nil {
		m.logger.Warn("backup schedule sync failed", "plan", model.ID, "error", err)
	}
	return model.toPlan(), nil
}

func (m *Module) UpdatePlan(ctx context.Context, id string, request PlanRequest) (Plan, error) {
	model := new(planModel)
	if err := m.database.NewSelect().Model(model).Where("id = ?", strings.TrimSpace(id)).Scan(ctx); err != nil {
		return Plan{}, err
	}
	request, err := normalizePlanRequest(request)
	if err != nil {
		return Plan{}, validationError{err.Error()}
	}
	if err := validatePlan(request); err != nil {
		return Plan{}, validationError{err.Error()}
	}
	if err := m.assertAccountExists(ctx, request.AccountID); err != nil {
		return Plan{}, err
	}
	if err := m.validatePlanTargets(ctx, request); err != nil {
		return Plan{}, validationError{err.Error()}
	}
	model.Name = strings.TrimSpace(request.Name)
	model.AccountID = request.AccountID
	model.CopiesLimit = request.CopiesLimit
	model.SiteIDsJSON = encodeSlice(request.SiteIDs)
	model.DatabaseIDsJSON = encodeSlice(request.DatabaseIDs)
	model.Schedule = strings.TrimSpace(request.Schedule)
	model.Enabled = request.Enabled
	model.ScheduleState = schedulePending
	model.ScheduleError = nil
	model.ScheduleSyncedAt = nil
	model.UpdatedAt = m.now().UTC()
	if _, err := m.database.NewUpdate().Model(model).WherePK().Exec(ctx); err != nil {
		return Plan{}, err
	}
	if err := m.syncSchedule(ctx, model); err != nil {
		m.logger.Warn("backup schedule sync failed", "plan", model.ID, "error", err)
	}
	return model.toPlan(), nil
}

func (m *Module) validatePlanTargets(ctx context.Context, request PlanRequest) error {
	if _, err := m.resolveSites(ctx, request.SiteIDs); err != nil {
		return fmt.Errorf("validate backup sites: %w", err)
	}
	if _, err := m.resolveDatabases(ctx, request.DatabaseIDs); err != nil {
		return fmt.Errorf("validate backup databases: %w", err)
	}
	return nil
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
	user, ok := identity.UserFromContext(r.Context())
	if !ok {
		httpapi.WriteError(w, http.StatusUnauthorized, "authentication_required", "Sign in to continue.")
		return
	}
	models, err := m.listPlansForUser(r.Context(), user)
	if err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, "backup_plans_unavailable", "The backup plans could not be loaded.")
		return
	}
	plans := make([]Plan, 0, len(models))
	for _, model := range models {
		plans = append(plans, model.toPlan())
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"items": plans})
}

func (m *Module) createPlanHTTP(w http.ResponseWriter, r *http.Request) {
	var request PlanRequest
	if err := decodeJSON(w, r, &request); err != nil {
		httpapi.WriteError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	plan, err := m.CreatePlan(r.Context(), request)
	if err != nil {
		writePlanError(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusCreated, plan)
}

func (m *Module) getPlanHTTP(w http.ResponseWriter, r *http.Request) {
	user, ok := identity.UserFromContext(r.Context())
	if !ok {
		httpapi.WriteError(w, http.StatusUnauthorized, "authentication_required", "Sign in to continue.")
		return
	}
	model, err := m.getPlanForUser(r.Context(), user, r.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) {
		httpapi.WriteError(w, http.StatusNotFound, "backup_plan_not_found", "The requested backup plan does not exist.")
		return
	}
	if err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, "backup_plan_unavailable", "The backup plan could not be loaded.")
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, model.toPlan())
}

func (m *Module) updatePlanHTTP(w http.ResponseWriter, r *http.Request) {
	var request PlanRequest
	if err := decodeJSON(w, r, &request); err != nil {
		httpapi.WriteError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	plan, err := m.UpdatePlan(r.Context(), r.PathValue("id"), request)
	if errors.Is(err, sql.ErrNoRows) {
		httpapi.WriteError(w, http.StatusNotFound, "backup_plan_not_found", "The requested backup plan does not exist.")
		return
	}
	if err != nil {
		writePlanError(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, plan)
}

func (m *Module) togglePlanHTTP(w http.ResponseWriter, r *http.Request) {
	var request toggleRequest
	if err := decodeJSON(w, r, &request); err != nil {
		httpapi.WriteError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	result, err := m.database.NewUpdate().Model((*planModel)(nil)).
		Set("enabled = ?", request.Enabled).Set("schedule_state = ?", schedulePending).
		Set("schedule_error = NULL").Set("schedule_synced_at = NULL").Set("updated_at = ?", m.now().UTC()).
		Where("id = ?", r.PathValue("id")).Exec(r.Context())
	if err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, "backup_plan_update_failed", "The backup plan could not be updated.")
		return
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		httpapi.WriteError(w, http.StatusNotFound, "backup_plan_not_found", "The requested backup plan does not exist.")
		return
	}
	// Reflect the new enabled state in the installed timer and preserve any
	// synchronization error for the client to surface.
	plan := new(planModel)
	if err := m.database.NewSelect().Model(plan).Where("id = ?", r.PathValue("id")).Scan(r.Context()); err == nil {
		plan.ScheduleState = schedulePending
		plan.ScheduleError = nil
		plan.ScheduleSyncedAt = nil
		if err := m.syncSchedule(r.Context(), plan); err != nil {
			m.logger.Warn("backup schedule sync failed", "plan", plan.ID, "error", err)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (m *Module) deletePlanHTTP(w http.ResponseWriter, r *http.Request) {
	err := m.DeletePlan(r.Context(), r.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) {
		httpapi.WriteError(w, http.StatusNotFound, "backup_plan_not_found", "The requested backup plan does not exist.")
		return
	}
	if errors.Is(err, errPlanHasCopies) {
		httpapi.WriteError(w, http.StatusConflict, "backup_plan_has_copies", "Delete every stored copy before deleting this backup plan.")
		return
	}
	var removal scheduleRemovalError
	if errors.As(err, &removal) {
		httpapi.WriteError(w, http.StatusBadGateway, "backup_schedule_remove_failed", "The backup schedule could not be removed; the plan was kept for a safe retry.")
		return
	}
	if err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, "backup_plan_delete_failed", "The backup plan could not be removed.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (m *Module) DeletePlan(ctx context.Context, id string) error {
	m.scheduleSyncMu.Lock()
	defer m.scheduleSyncMu.Unlock()
	plan := new(planModel)
	id = strings.TrimSpace(id)
	if err := m.database.NewSelect().Model(plan).Where("id = ?", id).Scan(ctx); err != nil {
		return err
	}
	hasCopies, err := m.database.NewSelect().Model((*copyModel)(nil)).Where("plan_id = ?", plan.ID).Exists(ctx)
	if err != nil {
		return err
	}
	if hasCopies {
		return errPlanHasCopies
	}
	if err := m.operator.RemoveSchedule(ctx, plan.ID); err != nil {
		message := err.Error()
		if len(message) > 500 {
			message = message[:500]
		}
		plan.ScheduleState = scheduleError
		plan.ScheduleError = &message
		_, _ = m.database.NewUpdate().Model(plan).Column("schedule_state", "schedule_error").WherePK().Exec(ctx)
		return scheduleRemovalError{cause: err}
	}
	// If the database delete fails, pending state makes the reconciler restore
	// the schedule that was just removed instead of silently losing automation.
	if _, err := m.database.NewUpdate().Model((*planModel)(nil)).
		Set("schedule_state = ?", schedulePending).Set("schedule_error = NULL").Set("schedule_synced_at = NULL").
		Where("id = ?", plan.ID).Exec(ctx); err != nil {
		return err
	}
	result, err := m.database.NewDelete().Model((*planModel)(nil)).Where("id = ?", id).Exec(ctx)
	if err != nil {
		hasCopies, lookupErr := m.database.NewSelect().Model((*copyModel)(nil)).Where("plan_id = ?", plan.ID).Exists(ctx)
		if lookupErr == nil && hasCopies {
			return errPlanHasCopies
		}
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func writePlanError(w http.ResponseWriter, err error) {
	var invalid validationError
	if errors.As(err, &invalid) {
		httpapi.WriteError(w, http.StatusUnprocessableEntity, "backup_plan_invalid", invalid.message)
		return
	}
	httpapi.WriteError(w, http.StatusInternalServerError, "backup_plan_failed", "The backup plan could not be saved.")
}

func encodeSlice(values []string) string {
	if values == nil {
		values = []string{}
	}
	encoded, _ := json.Marshal(values)
	return string(encoded)
}
