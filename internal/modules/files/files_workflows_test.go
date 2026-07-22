package files

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/modules/sites"
	"github.com/nexa-panel/nexa-panel/internal/platform/audit"
	"github.com/nexa-panel/nexa-panel/internal/platform/authorization"
	"github.com/nexa-panel/nexa-panel/internal/platform/identity"
	"github.com/nexa-panel/nexa-panel/internal/platform/jobs"
	filesoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/files"
	"github.com/nexa-panel/nexa-panel/internal/platform/operators/sitefs"
	"github.com/nexa-panel/nexa-panel/internal/platform/persistence"
	"github.com/nexa-panel/nexa-panel/internal/platform/secrets"
	"github.com/uptrace/bun"
)

type fakeCatalog map[string]sites.Site

func (c fakeCatalog) Get(_ context.Context, id string) (sites.Site, error) {
	site, ok := c[id]
	if !ok {
		return sites.Site{}, sql.ErrNoRows
	}
	return site, nil
}

// fakeAccessPolicy mirrors the identity module's grant semantics: only
// developers are scoped.
type fakeAccessPolicy struct{ allowed map[string]bool }

func (p fakeAccessPolicy) SiteAccessible(_ context.Context, user identity.User, siteID string) (bool, error) {
	if user.Role != "developer" {
		return true, nil
	}
	return p.allowed[siteID], nil
}

type operatorCall struct {
	method string
	scope  sitefs.Scope
	args   []any
}

type fakeOperator struct {
	calls []operatorCall
	fail  error
	body  []byte
}

func (o *fakeOperator) record(method string, scope sitefs.Scope, args ...any) {
	o.calls = append(o.calls, operatorCall{method: method, scope: scope, args: args})
}

func (o *fakeOperator) last() operatorCall { return o.calls[len(o.calls)-1] }

func (o *fakeOperator) List(_ context.Context, scope sitefs.Scope, path string) (filesoperator.Listing, error) {
	o.record("List", scope, path)
	if o.fail != nil {
		return filesoperator.Listing{}, o.fail
	}
	return filesoperator.Listing{Entries: []filesoperator.Entry{{Name: "index.php", Kind: "file", Size: 12, Mode: "rw-r-----"}}}, nil
}

func (o *fakeOperator) Stat(_ context.Context, scope sitefs.Scope, path string) (filesoperator.Entry, error) {
	o.record("Stat", scope, path)
	return filesoperator.Entry{Name: "index.php", Kind: "file"}, o.fail
}

func (o *fakeOperator) Read(_ context.Context, scope sitefs.Scope, path string) (filesoperator.ReadResult, error) {
	o.record("Read", scope, path)
	return filesoperator.ReadResult{Content: "hello", ETag: "etag-1", Size: 5}, o.fail
}

func (o *fakeOperator) Write(_ context.Context, scope sitefs.Scope, path, content, expectedETag string) (filesoperator.WriteResult, error) {
	o.record("Write", scope, path, content, expectedETag)
	if o.fail != nil {
		return filesoperator.WriteResult{}, o.fail
	}
	sum := sha256.Sum256([]byte(content))
	return filesoperator.WriteResult{ETag: string(sum[:4])}, nil
}

func (o *fakeOperator) Mkdir(_ context.Context, scope sitefs.Scope, path string) (filesoperator.Entry, error) {
	o.record("Mkdir", scope, path)
	return filesoperator.Entry{Name: path, Kind: "dir"}, o.fail
}

func (o *fakeOperator) Move(_ context.Context, scope sitefs.Scope, from, to string, overwrite bool) (filesoperator.Entry, error) {
	o.record("Move", scope, from, to, overwrite)
	return filesoperator.Entry{Name: to, Kind: "file"}, o.fail
}

func (o *fakeOperator) Chmod(_ context.Context, scope sitefs.Scope, path, mode string) (filesoperator.Entry, error) {
	o.record("Chmod", scope, path, mode)
	return filesoperator.Entry{Name: path, Kind: "file", Mode: "rwxr-xr-x"}, o.fail
}

func (o *fakeOperator) Copy(_ context.Context, scope sitefs.Scope, from, to string) (filesoperator.CopyResult, error) {
	o.record("Copy", scope, from, to)
	return filesoperator.CopyResult{Copied: 2}, o.fail
}

func (o *fakeOperator) Delete(_ context.Context, scope sitefs.Scope, path string, recursive bool) error {
	o.record("Delete", scope, path, recursive)
	return o.fail
}

func (o *fakeOperator) UploadBegin(_ context.Context, scope sitefs.Scope, path string, size int64, overwrite bool) (filesoperator.Upload, error) {
	o.record("UploadBegin", scope, path, size, overwrite)
	return filesoperator.Upload{UploadID: strings.Repeat("a", 32)}, o.fail
}

func (o *fakeOperator) UploadChunk(_ context.Context, scope sitefs.Scope, uploadID string, offset int64, body io.Reader) error {
	content, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	o.record("UploadChunk", scope, uploadID, offset, string(content))
	return o.fail
}

func (o *fakeOperator) UploadCommit(_ context.Context, scope sitefs.Scope, uploadID string) (filesoperator.Entry, error) {
	o.record("UploadCommit", scope, uploadID)
	return filesoperator.Entry{Name: "media.bin", Kind: "file", Size: 4}, o.fail
}

func (o *fakeOperator) UploadAbort(_ context.Context, scope sitefs.Scope, uploadID string) error {
	o.record("UploadAbort", scope, uploadID)
	return o.fail
}

func (o *fakeOperator) Download(_ context.Context, scope sitefs.Scope, path string) (io.ReadCloser, filesoperator.Entry, error) {
	o.record("Download", scope, path)
	if o.fail != nil {
		return nil, filesoperator.Entry{}, o.fail
	}
	return io.NopCloser(bytes.NewReader(o.body)), filesoperator.Entry{Name: "asset.css", Kind: "file", Size: int64(len(o.body))}, nil
}

func (o *fakeOperator) Archive(_ context.Context, scope sitefs.Scope, paths []string, target string) (filesoperator.ArchiveResult, error) {
	o.record("Archive", scope, paths, target)
	return filesoperator.ArchiveResult{Entries: len(paths), Bytes: 42}, o.fail
}

func (o *fakeOperator) Extract(_ context.Context, scope sitefs.Scope, path, targetDir string) (filesoperator.ExtractResult, error) {
	o.record("Extract", scope, path, targetDir)
	return filesoperator.ExtractResult{Written: 3}, o.fail
}

func (o *fakeOperator) DirectorySize(_ context.Context, scope sitefs.Scope, path string) (filesoperator.SizeResult, error) {
	o.record("DirectorySize", scope, path)
	return filesoperator.SizeResult{Bytes: 9, Files: 3, Dirs: 1}, o.fail
}

type authenticatedRegistry struct {
	mux            *http.ServeMux
	authentication interface {
		Middleware(http.Handler) http.Handler
	}
	authorization *authorization.Policy
}

func (r authenticatedRegistry) Handle(pattern string, handler http.Handler) error {
	r.mux.Handle(pattern, handler)
	return nil
}

func (r authenticatedRegistry) HandleAuthenticated(pattern string, handler http.Handler) error {
	r.mux.Handle(pattern, r.authentication.Middleware(handler))
	return nil
}

func (r authenticatedRegistry) HandleAuthorized(pattern, permission string, handler http.Handler) error {
	r.mux.Handle(pattern, r.authentication.Middleware(r.authorization.Middleware(permission, handler)))
	return nil
}

// csrfTokenFor mirrors the double-submit token the identity module binds to a
// session, so seeded sessions can satisfy the same check real ones do.
func csrfTokenFor(sessionToken string) string { return "csrf-" + sessionToken }

// authenticate presents both halves of the double submit plus a same-origin
// signal, which is what the identity middleware requires of an unsafe request.
func authenticate(request *http.Request, cookie *http.Cookie) {
	request.AddCookie(cookie)
	request.Header.Set("Origin", "http://"+request.Host)
	request.Header.Set("X-CSRF-Token", csrfTokenFor(cookie.Value))
}

func seedIdentitySession(t *testing.T, database *bun.DB, id, username, role, token string) *http.Cookie {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	if _, err := database.ExecContext(ctx,
		`INSERT INTO identity_users (id, username, password_hash, created_at, role, totp_confirmed_at) VALUES (?, ?, 'unused', ?, ?, ?)`,
		id, username, now, role, now); err != nil {
		t.Fatalf("seed user %s: %v", username, err)
	}
	hash := sha256.Sum256([]byte(token))
	csrfHash := sha256.Sum256([]byte(csrfTokenFor(token)))
	if _, err := database.ExecContext(ctx,
		`INSERT INTO identity_sessions (id, user_id, token_hash, csrf_token_hash, created_at, expires_at, last_seen_at, mfa_verified_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		"session_"+id, id, hash[:], csrfHash[:], now, now.Add(time.Hour), now, now); err != nil {
		t.Fatalf("seed session for %s: %v", username, err)
	}
	return &http.Cookie{Name: "nexa_session", Value: token}
}

type harness struct {
	mux      *http.ServeMux
	operator *fakeOperator
	queue    *jobs.Module
	audit    *audit.Module
	site     sites.Site
	inactive sites.Site
	admin    *http.Cookie
	dev      *http.Cookie
	viewer   *http.Cookie
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	ctx := context.Background()
	database, err := persistence.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := persistence.RunMigrations(context.Background(), database); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	auditLog, err := audit.New(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	secretBox, err := secrets.New(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	identityModule, err := identity.NewWithConfig(ctx, database, auditLog, secretBox, logger, identity.Config{
		SessionTTL: time.Hour, PasswordMemoryKiB: 64, PasswordIterations: 1, PasswordThreads: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	queue, err := jobs.NewWithConfig(ctx, database, auditLog, logger, jobs.Config{PollInterval: 5 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	queue.Start(context.Background())
	t.Cleanup(queue.Close)

	active := sites.Site{ID: "site_active", Slug: "granted", RootPath: "/srv/nexa/sites/granted", UnixUser: "nexa_granted", Status: sites.StatusActive}
	inactive := sites.Site{ID: "site_planning", Slug: "pending", RootPath: "/srv/nexa/sites/pending", UnixUser: "nexa_pending", Status: sites.StatusPlanning}
	operator := &fakeOperator{body: []byte("body{color:red}")}
	filesModule, err := New(queue, fakeCatalog{active.ID: active, inactive.ID: inactive}, fakeAccessPolicy{allowed: map[string]bool{active.ID: true}}, operator, auditLog)
	if err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	registry := authenticatedRegistry{mux: mux, authentication: identityModule, authorization: authorization.New()}
	if err := filesModule.Register(registry); err != nil {
		t.Fatal(err)
	}
	return &harness{
		mux: mux, operator: operator, queue: queue, audit: auditLog, site: active, inactive: inactive,
		admin:  seedIdentitySession(t, database, "admin-1", "root", "admin", strings.Repeat("a", 64)),
		dev:    seedIdentitySession(t, database, "dev-1", "dev", "developer", strings.Repeat("b", 64)),
		viewer: seedIdentitySession(t, database, "viewer-1", "viewer", "viewer", strings.Repeat("c", 64)),
	}
}

func (h *harness) do(t *testing.T, method, path string, body string, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	request := httptest.NewRequest(method, path, reader)
	authenticate(request, cookie)
	response := httptest.NewRecorder()
	h.mux.ServeHTTP(response, request)
	return response
}

func (h *harness) waitForJob(t *testing.T, id int64) jobs.Job {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		job, err := h.queue.Get(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if job.State.Terminal() {
			return job
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("job did not complete")
	return jobs.Job{}
}

func errorCode(t *testing.T, response *httptest.ResponseRecorder) string {
	t.Helper()
	var payload struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode error payload %q: %v", response.Body.String(), err)
	}
	return payload.Code
}

func TestFilesSiteGuards(t *testing.T) {
	h := newHarness(t)

	if response := h.do(t, http.MethodGet, "/api/v1/sites/site_absent/files?path=public", "", h.admin); response.Code != http.StatusNotFound {
		t.Fatalf("missing site = %d %s", response.Code, response.Body.String())
	}
	response := h.do(t, http.MethodGet, "/api/v1/sites/"+h.inactive.ID+"/files?path=public", "", h.admin)
	if response.Code != http.StatusConflict || errorCode(t, response) != "site_not_active" {
		t.Fatalf("inactive site = %d %s", response.Code, response.Body.String())
	}
	if len(h.operator.calls) != 0 {
		t.Fatal("guards must run before the operator is called")
	}

	// Developers see ungranted sites as missing, not forbidden.
	granted := h.do(t, http.MethodGet, "/api/v1/sites/"+h.site.ID+"/files?path=public", "", h.dev)
	if granted.Code != http.StatusOK {
		t.Fatalf("developer granted list = %d %s", granted.Code, granted.Body.String())
	}
	hidden := h.do(t, http.MethodGet, "/api/v1/sites/"+h.inactive.ID+"/files?path=public", "", h.dev)
	if hidden.Code != http.StatusNotFound || errorCode(t, hidden) != "site_not_found" {
		t.Fatalf("developer hidden site = %d %s", hidden.Code, hidden.Body.String())
	}

	var listing filesoperator.Listing
	if err := json.Unmarshal(granted.Body.Bytes(), &listing); err != nil || len(listing.Entries) != 1 || listing.Entries[0].Name != "index.php" {
		t.Fatalf("listing shape = %s, %v", granted.Body.String(), err)
	}
	if call := h.operator.last(); call.method != "List" || call.scope.SiteID != h.site.ID || call.scope.RootPath != h.site.RootPath || call.args[0] != "public" {
		t.Fatalf("operator call = %+v", call)
	}
}

func TestFilesPermissionWiring(t *testing.T) {
	h := newHarness(t)

	if response := h.do(t, http.MethodGet, "/api/v1/sites/"+h.site.ID+"/files?path=", "", h.viewer); response.Code != http.StatusOK {
		t.Fatalf("viewer list = %d %s", response.Code, response.Body.String())
	}
	if response := h.do(t, http.MethodPut, "/api/v1/sites/"+h.site.ID+"/files/content", `{"path":"public/x.txt","content":"x","expectedEtag":""}`, h.viewer); response.Code != http.StatusForbidden {
		t.Fatalf("viewer write = %d %s", response.Code, response.Body.String())
	}
	if response := h.do(t, http.MethodPost, "/api/v1/sites/"+h.site.ID+"/files/delete", `{"path":"public/x.txt","recursive":false}`, h.viewer); response.Code != http.StatusForbidden {
		t.Fatalf("viewer delete = %d %s", response.Code, response.Body.String())
	}
	if response := h.do(t, http.MethodPut, "/api/v1/sites/"+h.site.ID+"/files/content", `{"path":"public/x.txt","content":"x","expectedEtag":""}`, h.dev); response.Code != http.StatusOK {
		t.Fatalf("developer write = %d %s", response.Code, response.Body.String())
	}
}

func TestFilesWriteAuditAndConflictPassthrough(t *testing.T) {
	h := newHarness(t)

	response := h.do(t, http.MethodPut, "/api/v1/sites/"+h.site.ID+"/files/content", `{"path":"public/index.php","content":"<?php","expectedEtag":"old"}`, h.admin)
	if response.Code != http.StatusOK {
		t.Fatalf("write = %d %s", response.Code, response.Body.String())
	}
	var written filesoperator.WriteResult
	if err := json.Unmarshal(response.Body.Bytes(), &written); err != nil || written.ETag == "" {
		t.Fatalf("write shape = %s, %v", response.Body.String(), err)
	}
	call := h.operator.last()
	if call.method != "Write" || call.args[0] != "public/index.php" || call.args[1] != "<?php" || call.args[2] != "old" {
		t.Fatalf("operator write call = %+v", call)
	}

	events, err := h.audit.List(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range events {
		if event.Action == "files.write" && event.Subject == "site:"+h.site.ID && event.Metadata["path"] == "public/index.php" && event.Metadata["slug"] == h.site.Slug {
			found = true
			if _, leaked := event.Metadata["content"]; leaked {
				t.Fatal("audit metadata must never include file content")
			}
		}
	}
	if !found {
		t.Fatalf("files.write audit event missing from %+v", events)
	}

	h.operator.fail = &filesoperator.OperationError{Code: filesoperator.CodeETagConflict, Message: "The file changed since it was loaded; reload it before saving."}
	conflict := h.do(t, http.MethodPut, "/api/v1/sites/"+h.site.ID+"/files/content", `{"path":"public/index.php","content":"x","expectedEtag":"stale"}`, h.admin)
	if conflict.Code != http.StatusConflict || errorCode(t, conflict) != filesoperator.CodeETagConflict {
		t.Fatalf("etag conflict = %d %s", conflict.Code, conflict.Body.String())
	}

	h.operator.fail = &filesoperator.OperationError{Code: filesoperator.CodeInvalid, Message: "Changes are only allowed inside the public, private, tmp, and backups directories."}
	invalid := h.do(t, http.MethodPost, "/api/v1/sites/"+h.site.ID+"/files/mkdir", `{"path":"logs/dir"}`, h.admin)
	if invalid.Code != http.StatusUnprocessableEntity || errorCode(t, invalid) != filesoperator.CodeInvalid {
		t.Fatalf("invalid passthrough = %d %s", invalid.Code, invalid.Body.String())
	}
}

func TestFilesUploadChunkAndDownloadStreaming(t *testing.T) {
	h := newHarness(t)
	uploadID := strings.Repeat("a", 32)

	response := h.do(t, http.MethodPut, "/api/v1/sites/"+h.site.ID+"/files/uploads/"+uploadID+"?offset=6", "chunk-bytes", h.admin)
	if response.Code != http.StatusOK {
		t.Fatalf("chunk = %d %s", response.Code, response.Body.String())
	}
	call := h.operator.last()
	if call.method != "UploadChunk" || call.args[0] != uploadID || call.args[1] != int64(6) || call.args[2] != "chunk-bytes" {
		t.Fatalf("chunk call = %+v", call)
	}

	if response := h.do(t, http.MethodPut, "/api/v1/sites/"+h.site.ID+"/files/uploads/"+uploadID+"?offset=junk", "x", h.admin); response.Code != http.StatusBadRequest {
		t.Fatalf("bad offset = %d %s", response.Code, response.Body.String())
	}
	if response := h.do(t, http.MethodPut, "/api/v1/sites/"+h.site.ID+"/files/uploads/"+uploadID+"?offset=-1", "x", h.admin); response.Code != http.StatusBadRequest {
		t.Fatalf("negative offset = %d %s", response.Code, response.Body.String())
	}

	h.operator.fail = &filesoperator.OperationError{Code: filesoperator.CodeOffsetConflict, Message: "The upload is at offset 12; resend the chunk from there.", Offset: 12}
	conflict := h.do(t, http.MethodPut, "/api/v1/sites/"+h.site.ID+"/files/uploads/"+uploadID+"?offset=0", "x", h.admin)
	if conflict.Code != http.StatusConflict {
		t.Fatalf("offset conflict = %d", conflict.Code)
	}
	var payload filesoperator.OperationError
	if err := json.Unmarshal(conflict.Body.Bytes(), &payload); err != nil || payload.Offset != 12 {
		t.Fatalf("conflict payload = %s, %v", conflict.Body.String(), err)
	}
	h.operator.fail = nil

	download := h.do(t, http.MethodGet, "/api/v1/sites/"+h.site.ID+"/files/download?path=public/asset.css", "", h.admin)
	if download.Code != http.StatusOK || download.Body.String() != "body{color:red}" {
		t.Fatalf("download = %d %q", download.Code, download.Body.String())
	}
	if disposition := download.Header().Get("Content-Disposition"); !strings.Contains(disposition, "attachment") || !strings.Contains(disposition, "asset.css") {
		t.Fatalf("disposition = %q", disposition)
	}
	if download.Header().Get("Content-Length") != "15" {
		t.Fatalf("content length = %q", download.Header().Get("Content-Length"))
	}
}

func TestFilesJobsEmbedValidatedScope(t *testing.T) {
	h := newHarness(t)

	response := h.do(t, http.MethodPost, "/api/v1/sites/"+h.site.ID+"/files/archive", `{"paths":["public/app"],"target":"backups/app.tar.gz"}`, h.admin)
	if response.Code != http.StatusAccepted {
		t.Fatalf("archive = %d %s", response.Code, response.Body.String())
	}
	var accepted struct {
		Job jobs.Job `json:"job"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &accepted); err != nil || accepted.Job.Kind != "files.archive" {
		t.Fatalf("archive response = %s, %v", response.Body.String(), err)
	}
	if job := h.waitForJob(t, accepted.Job.ID); job.State != jobs.StateSucceeded {
		t.Fatalf("archive job = %+v", job)
	}
	call := h.operator.last()
	if call.method != "Archive" || call.scope.SiteID != h.site.ID || call.scope.RootPath != h.site.RootPath || call.scope.UnixUser != h.site.UnixUser {
		t.Fatalf("archive call = %+v", call)
	}

	if response := h.do(t, http.MethodPost, "/api/v1/sites/"+h.site.ID+"/files/archive", `{"paths":["../evil"],"target":"backups/app.tar.gz"}`, h.admin); response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("hostile archive path = %d %s", response.Code, response.Body.String())
	}
	if response := h.do(t, http.MethodPost, "/api/v1/sites/"+h.site.ID+"/files/archive", `{"paths":["public"],"target":"backups/app.zip"}`, h.admin); response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("bad archive target = %d %s", response.Code, response.Body.String())
	}

	response = h.do(t, http.MethodPost, "/api/v1/sites/"+h.site.ID+"/files/extract", `{"path":"backups/app.tar.gz","targetDir":"public/unpacked"}`, h.admin)
	if response.Code != http.StatusAccepted {
		t.Fatalf("extract = %d %s", response.Code, response.Body.String())
	}
	if err := json.Unmarshal(response.Body.Bytes(), &accepted); err != nil || accepted.Job.Kind != "files.extract" {
		t.Fatalf("extract response = %s, %v", response.Body.String(), err)
	}
	if job := h.waitForJob(t, accepted.Job.ID); job.State != jobs.StateSucceeded {
		t.Fatalf("extract job = %+v", job)
	}
	if response := h.do(t, http.MethodPost, "/api/v1/sites/"+h.site.ID+"/files/extract", `{"path":"backups/app.rar","targetDir":"public/unpacked"}`, h.admin); response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("bad extract archive = %d %s", response.Code, response.Body.String())
	}

	response = h.do(t, http.MethodPost, "/api/v1/sites/"+h.site.ID+"/files/size", `{"path":"public"}`, h.admin)
	if response.Code != http.StatusAccepted {
		t.Fatalf("size = %d %s", response.Code, response.Body.String())
	}
	if err := json.Unmarshal(response.Body.Bytes(), &accepted); err != nil || accepted.Job.Kind != "files.size" {
		t.Fatalf("size response = %s, %v", response.Body.String(), err)
	}
	job := h.waitForJob(t, accepted.Job.ID)
	if job.State != jobs.StateSucceeded {
		t.Fatalf("size job = %+v", job)
	}
	var result filesoperator.SizeResult
	if err := json.Unmarshal(job.Result, &result); err != nil || result.Files != 3 || result.Bytes != 9 {
		t.Fatalf("size result = %s, %v", job.Result, err)
	}

	// Jobs cannot be submitted for sites the actor cannot see or use.
	if response := h.do(t, http.MethodPost, "/api/v1/sites/"+h.inactive.ID+"/files/archive", `{"paths":["public"],"target":"backups/x.tar.gz"}`, h.admin); response.Code != http.StatusConflict {
		t.Fatalf("archive on inactive = %d %s", response.Code, response.Body.String())
	}
	if response := h.do(t, http.MethodPost, "/api/v1/sites/"+h.inactive.ID+"/files/archive", `{"paths":["public"],"target":"backups/x.tar.gz"}`, h.dev); response.Code != http.StatusNotFound {
		t.Fatalf("developer archive on hidden = %d %s", response.Code, response.Body.String())
	}
}

func TestFilesChmodWiringAndAudit(t *testing.T) {
	h := newHarness(t)

	if response := h.do(t, http.MethodPost, "/api/v1/sites/"+h.site.ID+"/files/chmod", `{"path":"public/script.sh","mode":"755"}`, h.viewer); response.Code != http.StatusForbidden {
		t.Fatalf("viewer chmod = %d %s", response.Code, response.Body.String())
	}

	response := h.do(t, http.MethodPost, "/api/v1/sites/"+h.site.ID+"/files/chmod", `{"path":"public/script.sh","mode":"755"}`, h.admin)
	if response.Code != http.StatusOK {
		t.Fatalf("chmod = %d %s", response.Code, response.Body.String())
	}
	var entry filesoperator.Entry
	if err := json.Unmarshal(response.Body.Bytes(), &entry); err != nil || entry.Mode != "rwxr-xr-x" {
		t.Fatalf("chmod shape = %s, %v", response.Body.String(), err)
	}
	if call := h.operator.last(); call.method != "Chmod" || call.args[0] != "public/script.sh" || call.args[1] != "755" {
		t.Fatalf("operator chmod call = %+v", call)
	}

	events, err := h.audit.List(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range events {
		if event.Action == "files.chmod" && event.Subject == "site:"+h.site.ID && event.Metadata["path"] == "public/script.sh" && event.Metadata["mode"] == "755" {
			found = true
		}
	}
	if !found {
		t.Fatalf("files.chmod audit event missing from %+v", events)
	}

	h.operator.fail = &filesoperator.OperationError{Code: filesoperator.CodeInvalid, Message: "The mode must be three octal digits, such as 755."}
	invalid := h.do(t, http.MethodPost, "/api/v1/sites/"+h.site.ID+"/files/chmod", `{"path":"public/script.sh","mode":"9999"}`, h.admin)
	if invalid.Code != http.StatusUnprocessableEntity || errorCode(t, invalid) != filesoperator.CodeInvalid {
		t.Fatalf("invalid mode passthrough = %d %s", invalid.Code, invalid.Body.String())
	}
}
