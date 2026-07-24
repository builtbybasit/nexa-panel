package schedules

import (
	"fmt"
	"net/http"

	"github.com/nexa-panel/nexa-panel/internal/modules/sites"
	"github.com/nexa-panel/nexa-panel/internal/platform/httpapi"
	"github.com/nexa-panel/nexa-panel/internal/platform/identity"
	"github.com/nexa-panel/nexa-panel/internal/platform/operators/sitefs"
	"github.com/nexa-panel/nexa-panel/internal/platform/webhandler"
)

// resolveSite loads the site and hides it from unauthorized users; an
// inaccessible site reads as 404 so existence never leaks. Mutations require
// an active site; reads only require the site to have left draft.
func (m *Module) resolveSite(w http.ResponseWriter, r *http.Request, mutation bool) (sites.Site, sitefs.Scope, identity.User, bool) {
	user, ok := identity.UserFromContext(r.Context())
	if !ok {
		httpapi.WriteError(w, http.StatusUnauthorized, "authentication_required", "Sign in to continue.")
		return sites.Site{}, sitefs.Scope{}, identity.User{}, false
	}
	site, err := m.sites.Get(r.Context(), r.PathValue("id"))
	if isNoRows(err) {
		httpapi.WriteError(w, http.StatusNotFound, "site_not_found", "The requested site does not exist.")
		return sites.Site{}, sitefs.Scope{}, identity.User{}, false
	}
	if err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, "site_unavailable", "The site could not be loaded.")
		return sites.Site{}, sitefs.Scope{}, identity.User{}, false
	}
	accessible, err := m.access.SiteAccessible(r.Context(), user, site.ID)
	if err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, "site_unavailable", "The site could not be loaded.")
		return sites.Site{}, sitefs.Scope{}, identity.User{}, false
	}
	if !accessible {
		httpapi.WriteError(w, http.StatusNotFound, "site_not_found", "The requested site does not exist.")
		return sites.Site{}, sitefs.Scope{}, identity.User{}, false
	}
	if mutation && site.Status != sites.StatusActive {
		httpapi.WriteError(w, http.StatusConflict, "site_not_active", "Scheduled tasks can only be changed while the site is active.")
		return sites.Site{}, sitefs.Scope{}, identity.User{}, false
	}
	if !mutation && site.Status == sites.StatusDraft {
		httpapi.WriteError(w, http.StatusConflict, "site_not_active", "Scheduled tasks are not available for draft sites.")
		return sites.Site{}, sitefs.Scope{}, identity.User{}, false
	}
	scope := sitefs.Scope{SiteID: site.ID, Slug: site.Slug, RootPath: site.RootPath, UnixUser: site.UnixUser}
	return site, scope, user, true
}

// resolveTask loads the task scoped to the resolved site; a task from another
// site reads as 404.
func (m *Module) resolveTask(w http.ResponseWriter, r *http.Request, siteID string) (Task, bool) {
	task, err := m.task(r.Context(), siteID, r.PathValue("taskId"))
	if isNoRows(err) {
		httpapi.WriteError(w, http.StatusNotFound, "schedule_not_found", "The requested scheduled task does not exist.")
		return Task{}, false
	}
	if err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, "schedule_unavailable", "The scheduled task could not be loaded.")
		return Task{}, false
	}
	return task, true
}

// busy reports whether a lifecycle job is still in flight; desired-state
// changes wait for it so plans and rows never race the worker.
func busy(task Task) bool {
	switch task.Status {
	case StatusPlanning, StatusActivating, StatusRollingBack:
		return true
	}
	return false
}

func (m *Module) listHTTP(w http.ResponseWriter, r *http.Request) {
	site, _, _, ok := m.resolveSite(w, r, false)
	if !ok {
		return
	}
	models := make([]taskModel, 0)
	if err := m.database.NewSelect().Model(&models).Where("site_id = ?", site.ID).OrderExpr("name ASC, created_at ASC").Scan(r.Context()); err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, "schedules_unavailable", "Scheduled tasks could not be loaded.")
		return
	}
	items := make([]Task, 0, len(models))
	for index := range models {
		items = append(items, models[index].toTask())
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (m *Module) getHTTP(w http.ResponseWriter, r *http.Request) {
	site, _, _, ok := m.resolveSite(w, r, false)
	if !ok {
		return
	}
	task, ok := m.resolveTask(w, r, site.ID)
	if !ok {
		return
	}
	response := map[string]any{"task": task}
	if task.Status == StatusPlanReady {
		if plan, expiresAt, err := m.plan(r.Context(), task.ID); err == nil {
			response["plan"] = plan
			response["planExpiresAt"] = expiresAt
		}
	}
	httpapi.WriteJSON(w, http.StatusOK, response)
}

func (m *Module) createHTTP(w http.ResponseWriter, r *http.Request) {
	request, decodeErr := webhandler.Decode[TaskRequest](w, r)
	if decodeErr != nil {
		webhandler.Fail(w, decodeErr)
		return
	}
	_, scope, user, ok := m.resolveSite(w, r, true)
	if !ok {
		return
	}
	if err := validateRequest(&request); err != nil {
		httpapi.WriteError(w, http.StatusUnprocessableEntity, "schedule_invalid", err.Error())
		return
	}
	now := m.now().UTC()
	model := &taskModel{
		ID: randomID(), SiteID: scope.SiteID, Name: request.Name, CronExpression: request.CronExpression,
		Command: request.Command, TimeoutSeconds: request.TimeoutSeconds, Enabled: request.Enabled,
		Status: string(StatusPlanning), CreatedAt: now, UpdatedAt: now,
	}
	if _, err := m.database.NewInsert().Model(model).Exec(r.Context()); err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, "schedule_create_failed", "The scheduled task could not be stored.")
		return
	}
	m.submitPlan(w, r, model.toTask(), scope, user, false)
}

func (m *Module) updateHTTP(w http.ResponseWriter, r *http.Request) {
	request, decodeErr := webhandler.Decode[TaskRequest](w, r)
	if decodeErr != nil {
		webhandler.Fail(w, decodeErr)
		return
	}
	site, scope, user, ok := m.resolveSite(w, r, true)
	if !ok {
		return
	}
	task, ok := m.resolveTask(w, r, site.ID)
	if !ok {
		return
	}
	if busy(task) || task.PendingRemoval {
		httpapi.WriteError(w, http.StatusConflict, "schedule_conflict", "The scheduled task has an operation in flight; wait for it to finish.")
		return
	}
	if err := validateRequest(&request); err != nil {
		httpapi.WriteError(w, http.StatusUnprocessableEntity, "schedule_invalid", err.Error())
		return
	}
	task.Name = request.Name
	task.CronExpression = request.CronExpression
	task.Command = request.Command
	task.TimeoutSeconds = request.TimeoutSeconds
	task.Enabled = request.Enabled
	now := m.now().UTC()
	if _, err := m.database.NewUpdate().Model((*taskModel)(nil)).
		Set("name = ?", task.Name).Set("cron_expression = ?", task.CronExpression).
		Set("command = ?", task.Command).Set("timeout_seconds = ?", task.TimeoutSeconds).
		Set("enabled = ?", task.Enabled).Set("status = ?", StatusPlanning).
		Set("failure = NULL").Set("updated_at = ?", now).
		Where("id = ?", task.ID).Exec(r.Context()); err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, "schedule_update_failed", "The scheduled task could not be updated.")
		return
	}
	task.Status = StatusPlanning
	task.Failure = ""
	task.UpdatedAt = now
	m.submitPlan(w, r, task, scope, user, false)
}

func (m *Module) deleteHTTP(w http.ResponseWriter, r *http.Request) {
	site, scope, user, ok := m.resolveSite(w, r, true)
	if !ok {
		return
	}
	task, ok := m.resolveTask(w, r, site.ID)
	if !ok {
		return
	}
	if busy(task) {
		httpapi.WriteError(w, http.StatusConflict, "schedule_conflict", "The scheduled task has an operation in flight; wait for it to finish.")
		return
	}
	now := m.now().UTC()
	if _, err := m.database.NewUpdate().Model((*taskModel)(nil)).
		Set("pending_removal = ?", true).Set("status = ?", StatusPlanning).
		Set("failure = NULL").Set("updated_at = ?", now).
		Where("id = ?", task.ID).Exec(r.Context()); err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, "schedule_update_failed", "The scheduled task could not be marked for removal.")
		return
	}
	task.PendingRemoval = true
	task.Status = StatusPlanning
	task.Failure = ""
	task.UpdatedAt = now
	m.submitPlan(w, r, task, scope, user, true)
}

// submitPlan queues the planning job with the validated scope embedded, so
// the job worker never needs the requesting user.
func (m *Module) submitPlan(w http.ResponseWriter, r *http.Request, task Task, scope sitefs.Scope, user identity.User, removal bool) {
	job, err := m.jobs.SubmitTitled(r.Context(), "schedule.plan", "Update scheduled task", planPayload{Task: task.operatorTask(scope), Removal: removal}, &user.ID)
	if err != nil {
		m.markFailed(r.Context(), task.ID, err)
		httpapi.WriteError(w, http.StatusInternalServerError, "job_submission_failed", "Schedule planning could not be queued.")
		return
	}
	now := m.now().UTC()
	_, _ = m.database.NewUpdate().Model((*taskModel)(nil)).Set("last_job_id = ?", job.ID).Set("updated_at = ?", now).Where("id = ?", task.ID).Exec(r.Context())
	task.LastJobID = &job.ID
	task.UpdatedAt = now
	w.Header().Set("Location", fmt.Sprintf("/api/v1/jobs/%d", job.ID))
	httpapi.WriteJSON(w, http.StatusAccepted, map[string]any{"task": task, "job": job})
}

func (m *Module) applyHTTP(w http.ResponseWriter, r *http.Request) {
	site, _, user, ok := m.resolveSite(w, r, true)
	if !ok {
		return
	}
	task, ok := m.resolveTask(w, r, site.ID)
	if !ok {
		return
	}
	if task.Status != StatusPlanReady {
		httpapi.WriteError(w, http.StatusConflict, "schedule_not_ready", "Only a reviewed, current schedule plan can be applied.")
		return
	}
	plan, expiresAt, err := m.plan(r.Context(), task.ID)
	if isNoRows(err) {
		httpapi.WriteError(w, http.StatusNotFound, "schedule_plan_not_found", "A signed schedule plan is not ready.")
		return
	}
	if err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, "schedule_plan_unavailable", "The schedule plan could not be loaded.")
		return
	}
	if m.now().UTC().After(expiresAt) {
		httpapi.WriteError(w, http.StatusConflict, "schedule_plan_expired", "The schedule plan has expired; plan the task again.")
		return
	}
	m.submitPlanMutation(w, r, "schedule.apply", StatusActivating, task, plan, user)
}

func (m *Module) rollbackHTTP(w http.ResponseWriter, r *http.Request) {
	site, _, user, ok := m.resolveSite(w, r, true)
	if !ok {
		return
	}
	task, ok := m.resolveTask(w, r, site.ID)
	if !ok {
		return
	}
	if task.Status != StatusActive {
		httpapi.WriteError(w, http.StatusConflict, "schedule_not_active", "Only an applied scheduled task can be rolled back.")
		return
	}
	plan, _, err := m.plan(r.Context(), task.ID)
	if isNoRows(err) {
		httpapi.WriteError(w, http.StatusNotFound, "schedule_plan_not_found", "A signed schedule plan is not ready.")
		return
	}
	if err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, "schedule_plan_unavailable", "The schedule plan could not be loaded.")
		return
	}
	m.submitPlanMutation(w, r, "schedule.rollback", StatusRollingBack, task, plan, user)
}

func (m *Module) runHTTP(w http.ResponseWriter, r *http.Request) {
	_, scope, user, ok := m.resolveSite(w, r, true)
	if !ok {
		return
	}
	task, ok := m.resolveTask(w, r, scope.SiteID)
	if !ok {
		return
	}
	if task.Status != StatusActive || !task.Enabled {
		httpapi.WriteError(w, http.StatusConflict, "schedule_not_runnable", "Only an applied, enabled scheduled task can be run.")
		return
	}
	job, err := m.jobs.SubmitTitled(r.Context(), "schedule.run", "Run scheduled task", runPayload{Task: task.operatorTask(scope)}, &user.ID)
	if err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, "job_submission_failed", "The task run could not be queued.")
		return
	}
	w.Header().Set("Location", fmt.Sprintf("/api/v1/jobs/%d", job.ID))
	httpapi.WriteJSON(w, http.StatusAccepted, job)
}

func (m *Module) runsHTTP(w http.ResponseWriter, r *http.Request) {
	_, scope, _, ok := m.resolveSite(w, r, false)
	if !ok {
		return
	}
	task, ok := m.resolveTask(w, r, scope.SiteID)
	if !ok {
		return
	}
	records, err := m.operator.Runs(r.Context(), scope, task.ID)
	if err != nil {
		writeOperatorError(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"items": records})
}
