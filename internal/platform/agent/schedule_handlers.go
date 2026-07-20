package agent

import (
	"errors"
	"net/http"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/platform/httpapi"
	scheduleoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/schedules"
)

func WithScheduleOperator(operator scheduleoperator.Operator) Option {
	return func(server *Server) { server.schedules = operator }
}

// decodeScheduleJSON allows 256 KiB bodies: a schedule plan carries the
// rendered wrapper script plus its before-state snapshots inside the JSON.
func decodeScheduleJSON(w http.ResponseWriter, r *http.Request, destination any) error {
	return httpapi.DecodeJSONLimit(w, r, destination, 256*1024)
}

func writeScheduleFailure(w http.ResponseWriter, err error) {
	var operationErr *scheduleoperator.OperationError
	if errors.As(err, &operationErr) {
		writeJSON(w, scheduleoperator.StatusFor(operationErr.Code), operationErr)
		return
	}
	writeError(w, http.StatusConflict, "schedule_operation_failed", err.Error())
}

func (s *Server) schedulePlanHTTP(w http.ResponseWriter, r *http.Request) {
	var request scheduleoperator.PlanRequest
	if err := decodeScheduleJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	plan, err := s.schedules.Plan(r.Context(), request.Task, request.Removal)
	if err != nil {
		writeScheduleFailure(w, err)
		return
	}
	plan.Signature = s.signSchedulePlan(plan)
	writeJSON(w, http.StatusOK, plan)
}

func (s *Server) scheduleApplyHTTP(w http.ResponseWriter, r *http.Request) {
	s.schedulePlanMutation(w, r, false)
}

func (s *Server) scheduleRollbackHTTP(w http.ResponseWriter, r *http.Request) {
	s.schedulePlanMutation(w, r, true)
}

func (s *Server) schedulePlanMutation(w http.ResponseWriter, r *http.Request, rollback bool) {
	var plan scheduleoperator.Plan
	if err := decodeScheduleJSON(w, r, &plan); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if !s.verifySchedulePlan(plan) {
		writeError(w, http.StatusUnprocessableEntity, "invalid_schedule_plan_signature", "The schedule plan was not issued by this agent.")
		return
	}
	if !rollback && time.Now().UTC().After(plan.ExpiresAt) {
		writeError(w, http.StatusConflict, "schedule_plan_expired", "The schedule plan has expired; create a new plan.")
		return
	}
	var observation scheduleoperator.Observation
	var err error
	if rollback {
		observation, err = s.schedules.Rollback(r.Context(), plan)
	} else {
		observation, err = s.schedules.Apply(r.Context(), plan)
	}
	if err != nil {
		writeScheduleFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, observation)
}

func (s *Server) scheduleRunHTTP(w http.ResponseWriter, r *http.Request) {
	var request scheduleoperator.RunRequest
	if err := decodeScheduleJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	result, err := s.schedules.Run(r.Context(), request.Task)
	// A long task outlives the server's write timeout, whose deadline was set
	// when the request arrived; refresh it before writing the response.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(time.Minute))
	if err != nil {
		writeScheduleFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) scheduleRunsHTTP(w http.ResponseWriter, r *http.Request) {
	var request scheduleoperator.RunsRequest
	if err := decodeScheduleJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	records, err := s.schedules.Runs(r.Context(), request.Scope, request.TaskID)
	if err != nil {
		writeScheduleFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, scheduleoperator.RunsResult{Items: records})
}

func (s *Server) signSchedulePlan(plan scheduleoperator.Plan) string {
	plan.Signature = ""
	return signPayload(s.token, "schedule.plan.v1", plan)
}

func (s *Server) verifySchedulePlan(plan scheduleoperator.Plan) bool {
	provided := plan.Signature
	plan.Signature = ""
	return verifyPayload(s.token, "schedule.plan.v1", plan, provided)
}
