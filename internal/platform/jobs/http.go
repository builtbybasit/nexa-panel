package jobs

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/platform/identity"
)

func (m *Module) listHTTP(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 200 {
			writeError(w, http.StatusBadRequest, "invalid_limit", "Limit must be between 1 and 200.")
			return
		}
		limit = parsed
	}
	items, err := m.List(r.Context(), limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "jobs_unavailable", "Jobs could not be loaded.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (m *Module) getHTTP(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil || id < 1 {
		writeError(w, http.StatusBadRequest, "invalid_job_id", "A valid job ID is required.")
		return
	}
	job, err := m.Get(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "job_not_found", "The job does not exist.")
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (m *Module) diagnosticsHTTP(w http.ResponseWriter, r *http.Request) {
	input := diagnosticsRequest{DelayMilliseconds: 100}
	if r.ContentLength != 0 {
		r.Body = http.MaxBytesReader(w, r.Body, 4*1024)
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "Request body must be valid JSON.")
			return
		}
		if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
			writeError(w, http.StatusBadRequest, "invalid_request", "Request body must contain one JSON object.")
			return
		}
	}
	if input.DelayMilliseconds < 10 || input.DelayMilliseconds > 2000 {
		writeError(w, http.StatusUnprocessableEntity, "invalid_delay", "Delay must be between 10 and 2000 milliseconds.")
		return
	}
	user, ok := identity.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication_required", "Sign in to continue.")
		return
	}
	job, err := m.Submit(r.Context(), "platform.diagnostics", input, &user.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "job_submission_failed", "The diagnostic job could not be queued.")
		return
	}
	w.Header().Set("Location", fmt.Sprintf("/api/v1/jobs/%d", job.ID))
	writeJSON(w, http.StatusAccepted, job)
}

func (m *Module) eventsHTTP(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil || id < 1 {
		writeError(w, http.StatusBadRequest, "invalid_job_id", "A valid job ID is required.")
		return
	}
	if _, err := m.Get(r.Context(), id); err != nil {
		writeError(w, http.StatusNotFound, "job_not_found", "The job does not exist.")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming_unavailable", "Progress streaming is unavailable.")
		return
	}
	sequence := int64(0)
	if raw := r.Header.Get("Last-Event-ID"); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed < 0 {
			writeError(w, http.StatusBadRequest, "invalid_event_id", "Last-Event-ID must be a positive integer.")
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
			_, _ = io.WriteString(w, ": keep-alive\n\n")
			flusher.Flush()
		}
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]string{"code": code, "message": message})
}
