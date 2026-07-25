package jobs

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/platform/httpapi"
	"github.com/nexa-panel/nexa-panel/internal/platform/identity"
)

func (m *Module) listHTTP(w http.ResponseWriter, r *http.Request) {
	user, ok := identity.UserFromContext(r.Context())
	if !ok {
		httpapi.WriteError(w, http.StatusUnauthorized, "authentication_required", "Sign in to continue.")
		return
	}
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 200 {
			httpapi.WriteError(w, http.StatusBadRequest, "invalid_limit", "Limit must be between 1 and 200.")
			return
		}
		limit = parsed
	}
	items, err := m.ListForUser(r.Context(), user, limit)
	if err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, "jobs_unavailable", "Jobs could not be loaded.")
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"items": redactJobsForAPI(items)})
}

func (m *Module) getHTTP(w http.ResponseWriter, r *http.Request) {
	user, ok := identity.UserFromContext(r.Context())
	if !ok {
		httpapi.WriteError(w, http.StatusUnauthorized, "authentication_required", "Sign in to continue.")
		return
	}
	id, err := parseID(r)
	if err != nil || id < 1 {
		httpapi.WriteError(w, http.StatusBadRequest, "invalid_job_id", "A valid job ID is required.")
		return
	}
	job, err := m.GetForUser(r.Context(), user, id)
	if err != nil {
		httpapi.WriteError(w, http.StatusNotFound, "job_not_found", "The job does not exist.")
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, redactJobForAPI(job))
}

func (m *Module) diagnosticsHTTP(w http.ResponseWriter, r *http.Request) {
	input := diagnosticsRequest{DelayMilliseconds: 100}
	if r.ContentLength != 0 {
		if err := httpapi.DecodeJSONLimit(w, r, &input, 4*1024); err != nil {
			httpapi.WriteError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
	}
	if input.DelayMilliseconds < 10 || input.DelayMilliseconds > 2000 {
		httpapi.WriteError(w, http.StatusUnprocessableEntity, "invalid_delay", "Delay must be between 10 and 2000 milliseconds.")
		return
	}
	user, ok := identity.UserFromContext(r.Context())
	if !ok {
		httpapi.WriteError(w, http.StatusUnauthorized, "authentication_required", "Sign in to continue.")
		return
	}
	job, err := m.SubmitTitled(r.Context(), "platform.diagnostics", "Run diagnostics", input, &user.ID)
	if err != nil {
		httpapi.WriteError(w, http.StatusInternalServerError, "job_submission_failed", "The diagnostic job could not be queued.")
		return
	}
	w.Header().Set("Location", fmt.Sprintf("/api/v1/jobs/%d", job.ID))
	httpapi.WriteJSON(w, http.StatusAccepted, redactJobForAPI(job))
}

func (m *Module) eventsHTTP(w http.ResponseWriter, r *http.Request) {
	user, ok := identity.UserFromContext(r.Context())
	if !ok {
		httpapi.WriteError(w, http.StatusUnauthorized, "authentication_required", "Sign in to continue.")
		return
	}
	id, err := parseID(r)
	if err != nil || id < 1 {
		httpapi.WriteError(w, http.StatusBadRequest, "invalid_job_id", "A valid job ID is required.")
		return
	}
	if _, err := m.GetForUser(r.Context(), user, id); err != nil {
		httpapi.WriteError(w, http.StatusNotFound, "job_not_found", "The job does not exist.")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		httpapi.WriteError(w, http.StatusInternalServerError, "streaming_unavailable", "Progress streaming is unavailable.")
		return
	}
	// A job can run for minutes (e.g. an apt/nvm package install), far longer
	// than the server's WriteTimeout; extend the per-connection write deadline
	// on every flush so the SSE stream is not severed mid-job. Other endpoints
	// keep the timeout. Mirrors the logs live-tail handler.
	controller := http.NewResponseController(w)
	_ = controller.SetWriteDeadline(time.Now().Add(time.Minute))
	sequence := int64(0)
	if raw := r.Header.Get("Last-Event-ID"); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed < 0 {
			httpapi.WriteError(w, http.StatusBadRequest, "invalid_event_id", "Last-Event-ID must be a positive integer.")
			return
		}
		sequence = parsed
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	poll := time.NewTicker(400 * time.Millisecond)
	heartbeat := time.NewTicker(15 * time.Second)
	defer poll.Stop()
	defer heartbeat.Stop()
	// The loop reads with the unscoped accessors on purpose. Authorization was
	// settled above, and neither input to it changes while the stream is open:
	// a job's persisted site scope is fixed at submission, and the grants the
	// scope is checked against are the ones the connection was opened with.
	// Re-running the scoped variants would re-query the grant set — and re-read
	// the job — twice per 400ms tick for one job's progress.
	for {
		events, err := m.EventsAfter(r.Context(), id, sequence)
		if err != nil {
			return
		}
		for _, event := range events {
			encoded, err := json.Marshal(event)
			if err != nil {
				return
			}
			_, _ = fmt.Fprintf(w, "id: %d\nevent: progress\ndata: %s\n\n", event.Sequence, encoded)
			sequence = event.Sequence
		}
		if len(events) > 0 {
			_ = controller.SetWriteDeadline(time.Now().Add(time.Minute))
			flusher.Flush()
		}
		job, err := m.Get(r.Context(), id)
		if err != nil || (job.State.Terminal() && len(events) == 0) {
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-poll.C:
		case <-heartbeat.C:
			_ = controller.SetWriteDeadline(time.Now().Add(time.Minute))
			_, _ = io.WriteString(w, ": keep-alive\n\n")
			flusher.Flush()
		}
	}
}
