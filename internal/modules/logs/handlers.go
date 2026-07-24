package logs

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path"
	"strconv"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/modules/sites"
	"github.com/nexa-panel/nexa-panel/internal/platform/httpapi"
	"github.com/nexa-panel/nexa-panel/internal/platform/identity"
	logsoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/logs"
	"github.com/nexa-panel/nexa-panel/internal/platform/operators/sitefs"
)

// resolveSite loads the site, hides it from unauthorized users, and requires
// it to be provisioned. Logs stay readable for failed or rolled-back sites —
// that is when they matter most — but not before the site directories exist.
func (m *Module) resolveSite(w http.ResponseWriter, r *http.Request) (sites.Site, sitefs.Scope, identity.User, bool) {
	user, ok := identity.UserFromContext(r.Context())
	if !ok {
		httpapi.WriteError(w, http.StatusUnauthorized, "authentication_required", "Sign in to continue.")
		return sites.Site{}, sitefs.Scope{}, identity.User{}, false
	}
	site, err := m.sites.Get(r.Context(), r.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) {
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
	switch site.Status {
	case sites.StatusDraft, sites.StatusPlanning, sites.StatusPlanReady:
		httpapi.WriteError(w, http.StatusConflict, "site_not_provisioned", "Logs are not available until the site has been provisioned.")
		return sites.Site{}, sitefs.Scope{}, identity.User{}, false
	}
	scope := sitefs.Scope{SiteID: site.ID, Slug: site.Slug, RootPath: site.RootPath, UnixUser: site.UnixUser}
	return site, scope, user, true
}

func (m *Module) listHTTP(w http.ResponseWriter, r *http.Request) {
	_, scope, _, ok := m.resolveSite(w, r)
	if !ok {
		return
	}
	listing, err := m.operator.List(r.Context(), scope)
	if err != nil {
		// A site whose directories have not materialized on the node yet
		// simply has no logs to discover.
		var operationErr *logsoperator.OperationError
		if errors.As(err, &operationErr) && operationErr.Code == logsoperator.CodeNotFound {
			httpapi.WriteJSON(w, http.StatusOK, logsoperator.Listing{Items: []logsoperator.Entry{}})
			return
		}
		writeOperatorError(w, err)
		return
	}
	if listing.Items == nil {
		listing.Items = []logsoperator.Entry{}
	}
	httpapi.WriteJSON(w, http.StatusOK, listing)
}

// parseReadRequest maps the read/poll query parameters onto the operator
// request, applying the documented defaults and caps.
func parseReadRequest(r *http.Request) (logsoperator.ReadRequest, error) {
	query := r.URL.Query()
	request := logsoperator.ReadRequest{
		Name: query.Get("name"), Filter: query.Get("filter"),
		MaxBytes: logsoperator.ReadDefaultMaxBytes, TailLines: logsoperator.ReadDefaultTail,
	}
	if raw := query.Get("tail"); raw != "" {
		tail, err := strconv.Atoi(raw)
		if err != nil || tail < 1 {
			return request, errors.New("The tail parameter must be a positive integer.")
		}
		request.TailLines = min(tail, logsoperator.ReadMaxTail)
	}
	if raw := query.Get("max"); raw != "" {
		maxBytes, err := strconv.Atoi(raw)
		if err != nil || maxBytes < 1 {
			return request, errors.New("The max parameter must be a positive integer.")
		}
		request.MaxBytes = min(maxBytes, logsoperator.ReadMaxBytes)
	}
	if raw := query.Get("offset"); raw != "" {
		offset, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || offset < 0 {
			return request, errors.New("The offset parameter must be a non-negative integer.")
		}
		request.FromOffset = &offset
	}
	return request, nil
}

func (m *Module) readHTTP(w http.ResponseWriter, r *http.Request) {
	request, err := parseReadRequest(r)
	if err != nil {
		httpapi.WriteError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	_, scope, _, ok := m.resolveSite(w, r)
	if !ok {
		return
	}
	result, err := m.operator.Read(r.Context(), scope, request)
	if err != nil {
		writeOperatorError(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, result)
}

type streamEvent struct {
	Lines   []string `json:"lines"`
	Rotated bool     `json:"rotated"`
}

// streamHTTP live-tails one log file over SSE: an initial tail, then
// incremental polls against the agent. Event IDs carry the byte offset so a
// reconnecting client resumes exactly where it left off via Last-Event-ID.
func (m *Module) streamHTTP(w http.ResponseWriter, r *http.Request) {
	_, scope, _, ok := m.resolveSite(w, r)
	if !ok {
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		httpapi.WriteError(w, http.StatusInternalServerError, "streaming_unavailable", "Log streaming is unavailable.")
		return
	}
	name := r.URL.Query().Get("name")
	filter := r.URL.Query().Get("filter")
	request := logsoperator.ReadRequest{Name: name, Filter: filter, TailLines: logsoperator.ReadDefaultTail}
	if raw := r.Header.Get("Last-Event-ID"); raw != "" {
		resumeFrom, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || resumeFrom < 0 {
			httpapi.WriteError(w, http.StatusBadRequest, "invalid_event_id", "Last-Event-ID must be a non-negative integer.")
			return
		}
		request = logsoperator.ReadRequest{Name: name, Filter: filter, FromOffset: &resumeFrom}
	}

	// The first read happens before the stream is committed so a bad name or
	// a missing file still surfaces as a regular JSON error.
	result, err := m.operator.Read(r.Context(), scope, request)
	if err != nil {
		writeOperatorError(w, err)
		return
	}

	controller := http.NewResponseController(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	_ = controller.SetWriteDeadline(time.Now().Add(time.Minute))
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	emit := func(update logsoperator.ReadResult) bool {
		lines := update.Lines
		if lines == nil {
			lines = []string{}
		}
		encoded, err := json.Marshal(streamEvent{Lines: lines, Rotated: update.Rotated})
		if err != nil {
			return false
		}
		_ = controller.SetWriteDeadline(time.Now().Add(time.Minute))
		if _, err := fmt.Fprintf(w, "id: %d\nevent: lines\ndata: %s\n\n", update.NextOffset, encoded); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	offset := result.NextOffset
	if len(result.Lines) > 0 || result.Rotated {
		if !emit(result) {
			return
		}
	}

	poll := time.NewTicker(m.streamPollInterval)
	keepAlive := time.NewTicker(m.streamKeepAliveInterval)
	session := time.NewTimer(m.streamSessionLimit)
	defer poll.Stop()
	defer keepAlive.Stop()
	defer session.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-session.C:
			return
		case <-keepAlive.C:
			_ = controller.SetWriteDeadline(time.Now().Add(time.Minute))
			if _, err := io.WriteString(w, ": keep-alive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case <-poll.C:
			from := offset
			update, err := m.operator.Read(r.Context(), scope, logsoperator.ReadRequest{Name: name, Filter: filter, FromOffset: &from})
			if err != nil {
				return
			}
			offset = update.NextOffset
			if len(update.Lines) > 0 || update.Rotated {
				if !emit(update) {
					return
				}
			}
		}
	}
}

func (m *Module) downloadHTTP(w http.ResponseWriter, r *http.Request) {
	_, scope, _, ok := m.resolveSite(w, r)
	if !ok {
		return
	}
	reader, entry, err := m.operator.Download(r.Context(), scope, r.URL.Query().Get("name"))
	if err != nil {
		writeOperatorError(w, err)
		return
	}
	defer reader.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(entry.Size, 10))
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": path.Base(entry.Name)}))
	w.WriteHeader(http.StatusOK)
	controller := http.NewResponseController(w)
	buffer := make([]byte, 32*1024)
	for {
		// Keep extending the write deadline so a large download outlives the
		// API server's 30 second write timeout.
		_ = controller.SetWriteDeadline(time.Now().Add(time.Minute))
		read, readErr := reader.Read(buffer)
		if read > 0 {
			if _, writeErr := w.Write(buffer[:read]); writeErr != nil {
				return
			}
		}
		if readErr != nil {
			return
		}
	}
}
