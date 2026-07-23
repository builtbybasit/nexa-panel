package files

import (
	"database/sql"
	"errors"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/modules/sites"
	"github.com/nexa-panel/nexa-panel/internal/platform/identity"
	filesoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/files"
	"github.com/nexa-panel/nexa-panel/internal/platform/operators/sitefs"
)

// resolveSite loads the site, hides it from unauthorized users, and requires
// it to be active. Inaccessible sites read as 404 so existence never leaks.
func (m *Module) resolveSite(w http.ResponseWriter, r *http.Request) (sites.Site, sitefs.Scope, identity.User, bool) {
	user, ok := identity.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication_required", "Sign in to continue.")
		return sites.Site{}, sitefs.Scope{}, identity.User{}, false
	}
	site, err := m.sites.Get(r.Context(), r.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "site_not_found", "The requested site does not exist.")
		return sites.Site{}, sitefs.Scope{}, identity.User{}, false
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "site_unavailable", "The site could not be loaded.")
		return sites.Site{}, sitefs.Scope{}, identity.User{}, false
	}
	accessible, err := m.access.SiteAccessible(r.Context(), user, site.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "site_unavailable", "The site could not be loaded.")
		return sites.Site{}, sitefs.Scope{}, identity.User{}, false
	}
	if !accessible {
		writeError(w, http.StatusNotFound, "site_not_found", "The requested site does not exist.")
		return sites.Site{}, sitefs.Scope{}, identity.User{}, false
	}
	if site.Status != sites.StatusActive {
		writeError(w, http.StatusConflict, "site_not_active", "Files are only available while the site is active.")
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
	listing, err := m.operator.List(r.Context(), scope, r.URL.Query().Get("path"))
	if err != nil {
		writeOperatorError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, listing)
}

func (m *Module) statHTTP(w http.ResponseWriter, r *http.Request) {
	_, scope, _, ok := m.resolveSite(w, r)
	if !ok {
		return
	}
	entry, err := m.operator.Stat(r.Context(), scope, r.URL.Query().Get("path"))
	if err != nil {
		writeOperatorError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, entry)
}

func (m *Module) readHTTP(w http.ResponseWriter, r *http.Request) {
	site, scope, user, ok := m.resolveSite(w, r)
	if !ok {
		return
	}
	path := r.URL.Query().Get("path")
	result, err := m.operator.Read(r.Context(), scope, path)
	if err != nil {
		writeOperatorError(w, err)
		return
	}
	m.recordAuditRead(r, user, "files.read", site, map[string]any{"path": path})
	writeJSON(w, http.StatusOK, result)
}

func (m *Module) writeHTTP(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Path         string `json:"path"`
		Content      string `json:"content"`
		ExpectedETag string `json:"expectedEtag"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	site, scope, user, ok := m.resolveSite(w, r)
	if !ok {
		return
	}
	if !m.recordAudit(w, r, user, "files.write", site, map[string]any{"path": request.Path}) {
		return
	}
	result, err := m.operator.Write(r.Context(), scope, request.Path, request.Content, request.ExpectedETag)
	if err != nil {
		writeOperatorError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (m *Module) downloadHTTP(w http.ResponseWriter, r *http.Request) {
	site, scope, user, ok := m.resolveSite(w, r)
	if !ok {
		return
	}
	path := r.URL.Query().Get("path")
	reader, entry, err := m.operator.Download(r.Context(), scope, path)
	if err != nil {
		writeOperatorError(w, err)
		return
	}
	defer reader.Close()
	m.recordAuditRead(r, user, "files.download", site, map[string]any{"path": path, "size": entry.Size})
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(entry.Size, 10))
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": entry.Name}))
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

func (m *Module) mkdirHTTP(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Path string `json:"path"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	site, scope, user, ok := m.resolveSite(w, r)
	if !ok {
		return
	}
	if !m.recordAudit(w, r, user, "files.mkdir", site, map[string]any{"path": request.Path}) {
		return
	}
	entry, err := m.operator.Mkdir(r.Context(), scope, request.Path)
	if err != nil {
		writeOperatorError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, entry)
}

func (m *Module) moveHTTP(w http.ResponseWriter, r *http.Request) {
	var request struct {
		From      string `json:"from"`
		To        string `json:"to"`
		Overwrite bool   `json:"overwrite"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	site, scope, user, ok := m.resolveSite(w, r)
	if !ok {
		return
	}
	if !m.recordAudit(w, r, user, "files.move", site, map[string]any{"from": request.From, "to": request.To}) {
		return
	}
	entry, err := m.operator.Move(r.Context(), scope, request.From, request.To, request.Overwrite)
	if err != nil {
		writeOperatorError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, entry)
}

func (m *Module) chmodHTTP(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Path string `json:"path"`
		Mode string `json:"mode"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	site, scope, user, ok := m.resolveSite(w, r)
	if !ok {
		return
	}
	if !m.recordAudit(w, r, user, "files.chmod", site, map[string]any{"path": request.Path, "mode": request.Mode}) {
		return
	}
	entry, err := m.operator.Chmod(r.Context(), scope, request.Path, request.Mode)
	if err != nil {
		writeOperatorError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, entry)
}

func (m *Module) copyHTTP(w http.ResponseWriter, r *http.Request) {
	var request struct {
		From string `json:"from"`
		To   string `json:"to"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	site, scope, user, ok := m.resolveSite(w, r)
	if !ok {
		return
	}
	if !m.recordAudit(w, r, user, "files.copy", site, map[string]any{"from": request.From, "to": request.To}) {
		return
	}
	result, err := m.operator.Copy(r.Context(), scope, request.From, request.To)
	if err != nil {
		writeOperatorError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (m *Module) deleteHTTP(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Path      string `json:"path"`
		Recursive bool   `json:"recursive"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	site, scope, user, ok := m.resolveSite(w, r)
	if !ok {
		return
	}
	if !m.recordAudit(w, r, user, "files.delete", site, map[string]any{"path": request.Path, "recursive": request.Recursive}) {
		return
	}
	if err := m.operator.Delete(r.Context(), scope, request.Path, request.Recursive); err != nil {
		writeOperatorError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (m *Module) uploadBeginHTTP(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Path      string `json:"path"`
		Size      int64  `json:"size"`
		Overwrite bool   `json:"overwrite"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	_, scope, _, ok := m.resolveSite(w, r)
	if !ok {
		return
	}
	upload, err := m.operator.UploadBegin(r.Context(), scope, request.Path, request.Size, request.Overwrite)
	if err != nil {
		writeOperatorError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, upload)
}

func (m *Module) uploadChunkHTTP(w http.ResponseWriter, r *http.Request) {
	offset, err := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
	if err != nil || offset < 0 {
		writeError(w, http.StatusBadRequest, "invalid_offset", "The offset query parameter must be a non-negative integer.")
		return
	}
	if r.ContentLength > filesoperator.ChunkMaxBytes {
		writeError(w, http.StatusUnprocessableEntity, "files_invalid", "Upload chunks must be 8 MiB or smaller.")
		return
	}
	_, scope, _, ok := m.resolveSite(w, r)
	if !ok {
		return
	}
	// The API server's 15 second read timeout is too tight for an 8 MiB
	// chunk on a slow uplink; the body streams straight through to the agent.
	_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(5 * time.Minute))
	body := http.MaxBytesReader(w, r.Body, filesoperator.ChunkMaxBytes+1)
	if err := m.operator.UploadChunk(r.Context(), scope, r.PathValue("uploadId"), offset, body); err != nil {
		writeOperatorError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "received"})
}

func (m *Module) uploadCommitHTTP(w http.ResponseWriter, r *http.Request) {
	site, scope, user, ok := m.resolveSite(w, r)
	if !ok {
		return
	}
	if !m.recordAudit(w, r, user, "files.upload.commit", site, map[string]any{"uploadId": r.PathValue("uploadId")}) {
		return
	}
	entry, err := m.operator.UploadCommit(r.Context(), scope, r.PathValue("uploadId"))
	if err != nil {
		writeOperatorError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, entry)
}

func (m *Module) uploadAbortHTTP(w http.ResponseWriter, r *http.Request) {
	_, scope, _, ok := m.resolveSite(w, r)
	if !ok {
		return
	}
	if err := m.operator.UploadAbort(r.Context(), scope, r.PathValue("uploadId")); err != nil {
		writeOperatorError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "aborted"})
}

func (m *Module) archiveHTTP(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Paths  []string `json:"paths"`
		Target string   `json:"target"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	site, scope, user, ok := m.resolveSite(w, r)
	if !ok {
		return
	}
	if len(request.Paths) == 0 || len(request.Paths) > 100 {
		writeError(w, http.StatusUnprocessableEntity, "files_invalid", "Between 1 and 100 paths are required.")
		return
	}
	for _, path := range request.Paths {
		if _, err := sitefs.CleanRelative(path); err != nil {
			writeError(w, http.StatusUnprocessableEntity, "files_invalid", "The path is not allowed: "+err.Error()+".")
			return
		}
	}
	if _, err := sitefs.CleanRelative(request.Target); err != nil || !strings.HasSuffix(request.Target, ".tar.gz") {
		writeError(w, http.StatusUnprocessableEntity, "files_invalid", "The archive target must be a site-relative path ending in .tar.gz.")
		return
	}
	if !m.recordAudit(w, r, user, "files.archive", site, map[string]any{"paths": request.Paths, "target": request.Target}) {
		return
	}
	job, err := m.jobs.SubmitTitled(r.Context(), "files.archive", "Compress files", archivePayload{Scope: scope, Paths: request.Paths, Target: request.Target}, &user.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "job_submission_failed", "The archive job could not be queued.")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"job": job})
}

func (m *Module) extractHTTP(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Path      string `json:"path"`
		TargetDir string `json:"targetDir"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	site, scope, user, ok := m.resolveSite(w, r)
	if !ok {
		return
	}
	lowered := strings.ToLower(request.Path)
	if _, err := sitefs.CleanRelative(request.Path); err != nil || (!strings.HasSuffix(lowered, ".zip") && !strings.HasSuffix(lowered, ".tar.gz") && !strings.HasSuffix(lowered, ".tgz")) {
		writeError(w, http.StatusUnprocessableEntity, "files_invalid", "The archive path must be a site-relative .zip, .tar.gz, or .tgz file.")
		return
	}
	if _, err := sitefs.CleanRelative(request.TargetDir); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "files_invalid", "The target directory is not allowed: "+err.Error()+".")
		return
	}
	if !m.recordAudit(w, r, user, "files.extract", site, map[string]any{"path": request.Path, "targetDir": request.TargetDir}) {
		return
	}
	job, err := m.jobs.SubmitTitled(r.Context(), "files.extract", "Extract archive", extractPayload{Scope: scope, Path: request.Path, TargetDir: request.TargetDir}, &user.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "job_submission_failed", "The extraction job could not be queued.")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"job": job})
}

func (m *Module) sizeHTTP(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Path string `json:"path"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	_, scope, user, ok := m.resolveSite(w, r)
	if !ok {
		return
	}
	if _, err := sitefs.CleanRelative(request.Path); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "files_invalid", "The path is not allowed: "+err.Error()+".")
		return
	}
	job, err := m.jobs.SubmitTitled(r.Context(), "files.size", "Measure size", sizePayload{Scope: scope, Path: request.Path}, &user.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "job_submission_failed", "The directory size job could not be queued.")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"job": job})
}
