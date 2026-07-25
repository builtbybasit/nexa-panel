package agent

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/platform/httpapi"
	filesoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/files"
	"github.com/nexa-panel/nexa-panel/internal/platform/operators/sitefs"
)

func WithFilesOperator(operator filesoperator.Operator) Option {
	return func(server *Server) { server.files = operator }
}

// decodeFilesJSON allows larger bodies than decodeJSON: a file write carries
// up to 1 MiB of content inside the JSON envelope.
func decodeFilesJSON(w http.ResponseWriter, r *http.Request, destination any) error {
	return httpapi.DecodeJSONLimit(w, r, destination, 4*1024*1024)
}

func writeFilesFailure(w http.ResponseWriter, err error) {
	var operationErr *filesoperator.OperationError
	if errors.As(err, &operationErr) {
		writeJSON(w, filesoperator.StatusFor(operationErr.Code), operationErr)
		return
	}
	writeError(w, http.StatusInternalServerError, "files_operation_failed", err.Error())
}

func filesScope(r *http.Request) sitefs.Scope {
	query := r.URL.Query()
	return sitefs.Scope{SiteID: query.Get("site"), Slug: query.Get("slug"), RootPath: query.Get("root"), UnixUser: query.Get("user")}
}

func (s *Server) filesListHTTP(w http.ResponseWriter, r *http.Request) {
	var request filesoperator.PathRequest
	if err := decodeFilesJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	listing, err := s.files.List(r.Context(), request.Scope, request.Path)
	if err != nil {
		writeFilesFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, listing)
}

func (s *Server) filesStatHTTP(w http.ResponseWriter, r *http.Request) {
	var request filesoperator.PathRequest
	if err := decodeFilesJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	entry, err := s.files.Stat(r.Context(), request.Scope, request.Path)
	if err != nil {
		writeFilesFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, entry)
}

func (s *Server) filesReadHTTP(w http.ResponseWriter, r *http.Request) {
	var request filesoperator.PathRequest
	if err := decodeFilesJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	result, err := s.files.Read(r.Context(), request.Scope, request.Path)
	if err != nil {
		writeFilesFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) filesWriteHTTP(w http.ResponseWriter, r *http.Request) {
	var request filesoperator.WriteRequest
	if err := decodeFilesJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	result, err := s.files.Write(r.Context(), request.Scope, request.Path, request.Content, request.ExpectedETag)
	if err != nil {
		writeFilesFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) filesMkdirHTTP(w http.ResponseWriter, r *http.Request) {
	var request filesoperator.PathRequest
	if err := decodeFilesJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	entry, err := s.files.Mkdir(r.Context(), request.Scope, request.Path)
	if err != nil {
		writeFilesFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, entry)
}

func (s *Server) filesMoveHTTP(w http.ResponseWriter, r *http.Request) {
	var request filesoperator.MoveRequest
	if err := decodeFilesJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	entry, err := s.files.Move(r.Context(), request.Scope, request.From, request.To, request.Overwrite)
	if err != nil {
		writeFilesFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, entry)
}

func (s *Server) filesChmodHTTP(w http.ResponseWriter, r *http.Request) {
	var request filesoperator.ChmodRequest
	if err := decodeFilesJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	entry, err := s.files.Chmod(r.Context(), request.Scope, request.Path, request.Mode)
	if err != nil {
		writeFilesFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, entry)
}

func (s *Server) filesCopyHTTP(w http.ResponseWriter, r *http.Request) {
	var request filesoperator.CopyRequest
	if err := decodeFilesJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	result, err := s.files.Copy(r.Context(), request.Scope, request.From, request.To)
	if err != nil {
		writeFilesFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) filesDeleteHTTP(w http.ResponseWriter, r *http.Request) {
	var request filesoperator.DeleteRequest
	if err := decodeFilesJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if err := s.files.Delete(r.Context(), request.Scope, request.Path, request.Recursive); err != nil {
		writeFilesFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (s *Server) filesArchiveHTTP(w http.ResponseWriter, r *http.Request) {
	var request filesoperator.ArchiveRequest
	if err := decodeFilesJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	result, err := s.files.Archive(r.Context(), request.Scope, request.Paths, request.Target)
	if err != nil {
		writeFilesFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) filesExtractHTTP(w http.ResponseWriter, r *http.Request) {
	var request filesoperator.ExtractRequest
	if err := decodeFilesJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	result, err := s.files.Extract(r.Context(), request.Scope, request.Path, request.TargetDir)
	if err != nil {
		writeFilesFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) filesSizeHTTP(w http.ResponseWriter, r *http.Request) {
	var request filesoperator.PathRequest
	if err := decodeFilesJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	result, err := s.files.DirectorySize(r.Context(), request.Scope, request.Path)
	if err != nil {
		writeFilesFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) filesUploadBeginHTTP(w http.ResponseWriter, r *http.Request) {
	var request filesoperator.UploadBeginRequest
	if err := decodeFilesJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	upload, err := s.files.UploadBegin(r.Context(), request.Scope, request.Path, request.Size, request.Overwrite)
	if err != nil {
		writeFilesFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, upload)
}

func (s *Server) filesUploadChunkHTTP(w http.ResponseWriter, r *http.Request) {
	offset, err := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
	if err != nil || offset < 0 {
		writeError(w, http.StatusBadRequest, "invalid_request", "offset must be a non-negative integer")
		return
	}
	if r.ContentLength > filesoperator.ChunkMaxBytes {
		writeFilesFailure(w, &filesoperator.OperationError{Code: filesoperator.CodeInvalid, Message: "Upload chunks must be 8 MiB or smaller."})
		return
	}
	// The agent's 15s read timeout is too tight for an 8 MiB chunk over a
	// slow control-plane connection.
	_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(5 * time.Minute))
	body := http.MaxBytesReader(w, r.Body, filesoperator.ChunkMaxBytes+1)
	if err := s.files.UploadChunk(r.Context(), filesScope(r), r.PathValue("id"), offset, body); err != nil {
		writeFilesFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "received"})
}

func (s *Server) filesUploadCommitHTTP(w http.ResponseWriter, r *http.Request) {
	var request filesoperator.UploadScopeRequest
	if err := decodeFilesJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	entry, err := s.files.UploadCommit(r.Context(), request.Scope, r.PathValue("id"))
	if err != nil {
		writeFilesFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, entry)
}

func (s *Server) filesUploadAbortHTTP(w http.ResponseWriter, r *http.Request) {
	if err := s.files.UploadAbort(r.Context(), filesScope(r), r.PathValue("id")); err != nil {
		writeFilesFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "aborted"})
}

func (s *Server) filesDownloadHTTP(w http.ResponseWriter, r *http.Request) {
	reader, entry, err := s.files.Download(r.Context(), filesScope(r), r.URL.Query().Get("path"))
	if err != nil {
		writeFilesFailure(w, err)
		return
	}
	defer reader.Close()
	encodedEntry, err := json.Marshal(entry)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "files_operation_failed", err.Error())
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
