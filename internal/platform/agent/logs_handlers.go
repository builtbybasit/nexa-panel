package agent

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	logsoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/logs"
	"github.com/nexa-panel/nexa-panel/internal/platform/operators/sitefs"
)

func WithLogsOperator(operator logsoperator.Operator) Option {
	return func(server *Server) { server.logs = operator }
}

func writeLogsFailure(w http.ResponseWriter, err error) {
	var operationErr *logsoperator.OperationError
	if errors.As(err, &operationErr) {
		writeJSON(w, logsoperator.StatusFor(operationErr.Code), operationErr)
		return
	}
	writeError(w, http.StatusInternalServerError, "logs_operation_failed", err.Error())
}

func logsScope(r *http.Request) sitefs.Scope {
	query := r.URL.Query()
	return sitefs.Scope{SiteID: query.Get("site"), Slug: query.Get("slug"), RootPath: query.Get("root"), UnixUser: query.Get("user")}
}

func (s *Server) logsListHTTP(w http.ResponseWriter, r *http.Request) {
	var request logsoperator.ListRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	listing, err := s.logs.List(r.Context(), request.Scope)
	if err != nil {
		writeLogsFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, listing)
}

func (s *Server) logsReadHTTP(w http.ResponseWriter, r *http.Request) {
	var request logsoperator.ReadCall
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	result, err := s.logs.Read(r.Context(), request.Scope, request.Request)
	if err != nil {
		writeLogsFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) logsDownloadHTTP(w http.ResponseWriter, r *http.Request) {
	reader, entry, err := s.logs.Download(r.Context(), logsScope(r), r.URL.Query().Get("name"))
	if err != nil {
		writeLogsFailure(w, err)
		return
	}
	defer reader.Close()
	encodedEntry, err := json.Marshal(entry)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "logs_operation_failed", err.Error())
		return
	}
	w.Header().Set("X-Nexa-Entry", string(encodedEntry))
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(entry.Size, 10))
	w.WriteHeader(http.StatusOK)
	controller := http.NewResponseController(w)
	buffer := make([]byte, 32*1024)
	for {
		// Keep extending the write deadline so a large download outlives the
		// server's 6 minute write timeout.
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
