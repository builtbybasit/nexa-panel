package schedules

import (
	"context"
	"crypto/rand"

	"database/sql"
	"encoding/hex"

	"encoding/json"
	"errors"

	"io"
	"net/http"

	"strings"
	"time"

	scheduleoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/schedules"
	"github.com/nexa-panel/nexa-panel/internal/platform/operators/sitefs"
)

// validateRequest normalizes and checks the desired task fields against the
// same bounds the agent enforces, so invalid tasks fail here with a 422
// instead of failing later inside a planning job.
func validateRequest(request *TaskRequest) error {
	request.Name = strings.TrimSpace(request.Name)
	request.CronExpression = strings.Join(strings.Fields(request.CronExpression), " ")
	request.Command = strings.TrimSpace(request.Command)
	if err := scheduleoperator.ValidateName(request.Name); err != nil {
		return err
	}
	if err := scheduleoperator.ValidateCron(request.CronExpression); err != nil {
		return err
	}
	if err := scheduleoperator.ValidateCommand(request.Command); err != nil {
		return err
	}
	return scheduleoperator.ValidateTimeout(request.TimeoutSeconds)
}

func (m *Module) markFailed(ctx context.Context, taskID string, failure error) {
	message := failure.Error()
	if len(message) > 300 {
		message = message[:300]
	}
	_, _ = m.database.NewUpdate().Model((*taskModel)(nil)).Set("status = ?", StatusFailed).
		Set("failure = ?", message).Set("updated_at = ?", m.now().UTC()).Where("id = ?", taskID).Exec(ctx)
}

func (m *Module) task(ctx context.Context, siteID, taskID string) (Task, error) {
	model := new(taskModel)
	if err := m.database.NewSelect().Model(model).Where("id = ?", taskID).Where("site_id = ?", siteID).Scan(ctx); err != nil {
		return Task{}, err
	}
	return model.toTask(), nil
}

func (m *Module) plan(ctx context.Context, taskID string) (scheduleoperator.Plan, time.Time, error) {
	model := new(planModel)
	if err := m.database.NewSelect().Model(model).Where("task_id = ?", taskID).Scan(ctx); err != nil {
		return scheduleoperator.Plan{}, time.Time{}, err
	}
	var plan scheduleoperator.Plan
	if err := json.Unmarshal([]byte(model.PlanJSON), &plan); err != nil {
		return scheduleoperator.Plan{}, time.Time{}, errors.New("decode persisted schedule plan")
	}
	return plan, model.ExpiresAt, nil
}

// operatorTask builds the agent-side task from the stored desired state and
// the already-validated site scope.
func (task Task) operatorTask(scope sitefs.Scope) scheduleoperator.Task {
	return scheduleoperator.Task{
		ID: task.ID, Name: task.Name, CronExpression: task.CronExpression, Command: task.Command,
		TimeoutSeconds: task.TimeoutSeconds, Enabled: task.Enabled, Scope: scope,
	}
}

func (model taskModel) toTask() Task {
	failure := ""
	if model.Failure != nil {
		failure = *model.Failure
	}
	return Task{
		ID: model.ID, SiteID: model.SiteID, Name: model.Name, CronExpression: model.CronExpression,
		Command: model.Command, TimeoutSeconds: model.TimeoutSeconds, Enabled: model.Enabled,
		Status: Status(model.Status), PendingRemoval: model.PendingRemoval, LastJobID: model.LastJobID,
		Failure: failure, CreatedAt: model.CreatedAt, UpdatedAt: model.UpdatedAt,
	}
}

// randomID matches the agent's ^[a-f0-9]{32}$ managed task identifier shape.
func randomID() string {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(value)
}

func isNoRows(err error) bool { return errors.Is(err, sql.ErrNoRows) }

func decodeJSON(w http.ResponseWriter, r *http.Request, destination any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 16*1024)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return errors.New("Request body must be valid JSON.")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("Request body must contain one JSON object.")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]string{"code": code, "message": message})
}

// writeOperatorError relays the operator's typed failure with its HTTP
// status; anything else is an agent transport problem and stays generic.
func writeOperatorError(w http.ResponseWriter, err error) {
	var operationErr *scheduleoperator.OperationError
	if errors.As(err, &operationErr) {
		writeJSON(w, scheduleoperator.StatusFor(operationErr.Code), operationErr)
		return
	}
	writeError(w, http.StatusBadGateway, "schedules_agent_unavailable", "The node agent could not complete the schedule operation.")
}
