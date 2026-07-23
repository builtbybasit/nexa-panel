package logs

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
	"sync"
	"testing"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/modules/sites"
	"github.com/nexa-panel/nexa-panel/internal/platform/audit"
	"github.com/nexa-panel/nexa-panel/internal/platform/authorization"
	"github.com/nexa-panel/nexa-panel/internal/platform/identity"
	logsoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/logs"
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

type readCall struct {
	scope   sitefs.Scope
	request logsoperator.ReadRequest
}

// fakeOperator scripts Read responses so the SSE poll loop can be driven
// deterministically; the mutex keeps it safe under the handler goroutine.
type fakeOperator struct {
	mu        sync.Mutex
	listCalls []sitefs.Scope
	readCalls []readCall
	listing   logsoperator.Listing
	reads     []logsoperator.ReadResult
	body      []byte
	fail      error
}

func (o *fakeOperator) List(_ context.Context, scope sitefs.Scope) (logsoperator.Listing, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.listCalls = append(o.listCalls, scope)
	if o.fail != nil {
		return logsoperator.Listing{}, o.fail
	}
	return o.listing, nil
}

func (o *fakeOperator) Read(_ context.Context, scope sitefs.Scope, request logsoperator.ReadRequest) (logsoperator.ReadResult, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if request.FromOffset != nil {
		// Copy so recorded calls survive the handler mutating its offset.
		offset := *request.FromOffset
		request.FromOffset = &offset
	}
	o.readCalls = append(o.readCalls, readCall{scope: scope, request: request})
	if o.fail != nil {
		return logsoperator.ReadResult{}, o.fail
	}
	if len(o.reads) == 0 {
		return logsoperator.ReadResult{Lines: []string{}}, nil
	}
	next := o.reads[0]
	if len(o.reads) > 1 {
		o.reads = o.reads[1:]
	}
	return next, nil
}

func (o *fakeOperator) Download(_ context.Context, scope sitefs.Scope, name string) (io.ReadCloser, logsoperator.Entry, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.fail != nil {
		return nil, logsoperator.Entry{}, o.fail
	}
	return io.NopCloser(bytes.NewReader(o.body)), logsoperator.Entry{Name: name, Size: int64(len(o.body))}, nil
}

func (o *fakeOperator) recordedReads() []readCall {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]readCall(nil), o.readCalls...)
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
	module   *Module
	operator *fakeOperator
	site     sites.Site
	failed   sites.Site
	pending  sites.Site
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

	active := sites.Site{ID: "site_active", Slug: "granted", RootPath: "/srv/nexa/sites/granted", UnixUser: "nexa_granted", Status: sites.StatusActive}
	failed := sites.Site{ID: "site_failed", Slug: "broken", RootPath: "/srv/nexa/sites/broken", UnixUser: "nexa_broken", Status: sites.StatusFailed}
	pending := sites.Site{ID: "site_pending", Slug: "pending", RootPath: "/srv/nexa/sites/pending", UnixUser: "nexa_pending", Status: sites.StatusPlanning}
	operator := &fakeOperator{
		listing: logsoperator.Listing{Items: []logsoperator.Entry{{Name: "access.log", Size: 12}, {Name: "tasks/1.log", Size: 3}}},
		body:    []byte("log-content\n"),
	}
	logsModule, err := New(fakeCatalog{active.ID: active, failed.ID: failed, pending.ID: pending}, fakeAccessPolicy{allowed: map[string]bool{active.ID: true}}, operator)
	if err != nil {
		t.Fatal(err)
	}
	// Keep the SSE loop fast enough for tests without changing its shape.
	logsModule.streamPollInterval = 5 * time.Millisecond
	logsModule.streamKeepAliveInterval = 20 * time.Millisecond

	mux := http.NewServeMux()
	registry := authenticatedRegistry{mux: mux, authentication: identityModule, authorization: authorization.New()}
	if err := logsModule.Register(registry); err != nil {
		t.Fatal(err)
	}
	return &harness{
		mux: mux, module: logsModule, operator: operator, site: active, failed: failed, pending: pending,
		admin:  seedIdentitySession(t, database, "admin-1", "root", "admin", strings.Repeat("a", 64)),
		dev:    seedIdentitySession(t, database, "dev-1", "dev", "developer", strings.Repeat("b", 64)),
		viewer: seedIdentitySession(t, database, "viewer-1", "viewer", "viewer", strings.Repeat("c", 64)),
	}
}

func (h *harness) do(t *testing.T, method, path string, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, nil)
	if cookie != nil {
		authenticate(request, cookie)
	}
	response := httptest.NewRecorder()
	h.mux.ServeHTTP(response, request)
	return response
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

func TestLogsSiteGuards(t *testing.T) {
	h := newHarness(t)

	if response := h.do(t, http.MethodGet, "/api/v1/sites/"+h.site.ID+"/logs", nil); response.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous list = %d %s", response.Code, response.Body.String())
	}
	if response := h.do(t, http.MethodGet, "/api/v1/sites/site_absent/logs", h.admin); response.Code != http.StatusNotFound {
		t.Fatalf("missing site = %d %s", response.Code, response.Body.String())
	}
	response := h.do(t, http.MethodGet, "/api/v1/sites/"+h.pending.ID+"/logs", h.admin)
	if response.Code != http.StatusConflict || errorCode(t, response) != "site_not_provisioned" {
		t.Fatalf("unprovisioned site = %d %s", response.Code, response.Body.String())
	}
	if len(h.operator.listCalls) != 0 {
		t.Fatal("guards must run before the operator is called")
	}

	// Logs stay readable for failed sites — that is when they matter most.
	if response := h.do(t, http.MethodGet, "/api/v1/sites/"+h.failed.ID+"/logs", h.admin); response.Code != http.StatusOK {
		t.Fatalf("failed site list = %d %s", response.Code, response.Body.String())
	}

	// Developers see ungranted sites as missing, not forbidden.
	granted := h.do(t, http.MethodGet, "/api/v1/sites/"+h.site.ID+"/logs", h.dev)
	if granted.Code != http.StatusOK {
		t.Fatalf("developer granted list = %d %s", granted.Code, granted.Body.String())
	}
	hidden := h.do(t, http.MethodGet, "/api/v1/sites/"+h.failed.ID+"/logs", h.dev)
	if hidden.Code != http.StatusNotFound || errorCode(t, hidden) != "site_not_found" {
		t.Fatalf("developer hidden site = %d %s", hidden.Code, hidden.Body.String())
	}

	var listing logsoperator.Listing
	if err := json.Unmarshal(granted.Body.Bytes(), &listing); err != nil || len(listing.Items) != 2 || listing.Items[0].Name != "access.log" {
		t.Fatalf("listing shape = %s, %v", granted.Body.String(), err)
	}
	last := h.operator.listCalls[len(h.operator.listCalls)-1]
	if last.SiteID != h.site.ID || last.RootPath != h.site.RootPath || last.UnixUser != h.site.UnixUser {
		t.Fatalf("operator scope = %+v", last)
	}

	// Viewers hold logs.read and may list too.
	if response := h.do(t, http.MethodGet, "/api/v1/sites/"+h.site.ID+"/logs", h.viewer); response.Code != http.StatusOK {
		t.Fatalf("viewer list = %d %s", response.Code, response.Body.String())
	}
}

func TestLogsListTreatsMissingDirectoryAsEmpty(t *testing.T) {
	h := newHarness(t)

	h.operator.fail = &logsoperator.OperationError{Code: logsoperator.CodeNotFound, Message: "The site directory is not present on this node."}
	response := h.do(t, http.MethodGet, "/api/v1/sites/"+h.site.ID+"/logs", h.admin)
	if response.Code != http.StatusOK || strings.TrimSpace(response.Body.String()) != `{"items":[]}` {
		t.Fatalf("missing dir list = %d %s", response.Code, response.Body.String())
	}

	// Other agent failures still surface as a gateway problem.
	h.operator.fail = io.ErrUnexpectedEOF
	response = h.do(t, http.MethodGet, "/api/v1/sites/"+h.site.ID+"/logs", h.admin)
	if response.Code != http.StatusBadGateway || errorCode(t, response) != "logs_agent_unavailable" {
		t.Fatalf("agent failure = %d %s", response.Code, response.Body.String())
	}
}

func TestLogsReadParamParsing(t *testing.T) {
	h := newHarness(t)
	base := "/api/v1/sites/" + h.site.ID + "/logs/read"

	// Tail mode: no offset parameter, defaults applied.
	if response := h.do(t, http.MethodGet, base+"?name=access.log", h.admin); response.Code != http.StatusOK {
		t.Fatalf("tail read = %d %s", response.Code, response.Body.String())
	}
	call := h.operator.recordedReads()[0]
	if call.request.Name != "access.log" || call.request.FromOffset != nil ||
		call.request.TailLines != logsoperator.ReadDefaultTail || call.request.MaxBytes != logsoperator.ReadDefaultMaxBytes || call.request.Filter != "" {
		t.Fatalf("default tail request = %+v", call.request)
	}

	// Explicit parameters pass through with the documented caps.
	if response := h.do(t, http.MethodGet, base+"?name=tasks/1.log&tail=99999&filter=ERR&offset=4096&max=99999999", h.admin); response.Code != http.StatusOK {
		t.Fatalf("parameterized read = %d %s", response.Code, response.Body.String())
	}
	call = h.operator.recordedReads()[1]
	if call.request.Name != "tasks/1.log" || call.request.Filter != "ERR" ||
		call.request.FromOffset == nil || *call.request.FromOffset != 4096 ||
		call.request.TailLines != logsoperator.ReadMaxTail || call.request.MaxBytes != logsoperator.ReadMaxBytes {
		t.Fatalf("parameterized request = %+v", call.request)
	}

	for _, query := range []string{"?name=a.log&tail=junk", "?name=a.log&tail=0", "?name=a.log&offset=-1", "?name=a.log&offset=junk", "?name=a.log&max=0"} {
		if response := h.do(t, http.MethodGet, base+query, h.admin); response.Code != http.StatusBadRequest {
			t.Fatalf("query %q = %d %s", query, response.Code, response.Body.String())
		}
	}

	// Operator errors keep their typed status.
	h.operator.fail = &logsoperator.OperationError{Code: logsoperator.CodeInvalid, Message: "The log name is not allowed."}
	if response := h.do(t, http.MethodGet, base+"?name=../etc", h.admin); response.Code != http.StatusUnprocessableEntity || errorCode(t, response) != logsoperator.CodeInvalid {
		t.Fatalf("invalid passthrough = %d %s", response.Code, response.Body.String())
	}
}

// streamRecorder is a concurrency-safe ResponseWriter for driving the SSE
// handler from another goroutine.
type streamRecorder struct {
	mu     sync.Mutex
	header http.Header
	status int
	body   bytes.Buffer
}

func newStreamRecorder() *streamRecorder { return &streamRecorder{header: http.Header{}} }

func (r *streamRecorder) Header() http.Header {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.header
}

func (r *streamRecorder) WriteHeader(status int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.status = status
}

func (r *streamRecorder) Write(data []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.body.Write(data)
}

func (r *streamRecorder) Flush() {}

func (r *streamRecorder) snapshot() (int, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.status, r.body.String()
}

// serveStream runs the SSE handler in a goroutine and returns a cancel for
// the request context plus a channel closed when the handler returns.
func (h *harness) serveStream(t *testing.T, path string, headers map[string]string) (*streamRecorder, context.CancelFunc, chan struct{}) {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, path, nil)
	authenticate(request, h.admin)
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	ctx, cancel := context.WithCancel(request.Context())
	request = request.WithContext(ctx)
	recorder := newStreamRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.mux.ServeHTTP(recorder, request)
	}()
	return recorder, cancel, done
}

func waitFor(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}

func TestLogsStreamFramingAndRotation(t *testing.T) {
	h := newHarness(t)
	h.operator.reads = []logsoperator.ReadResult{
		{Lines: []string{"boot", "ready"}, NextOffset: 100, Size: 100},  // initial tail
		{Lines: []string{}, NextOffset: 100, Size: 100},                 // quiet poll: no event
		{Lines: []string{"request served"}, NextOffset: 130, Size: 130}, // growth
		{Rotated: true, NextOffset: 0, Size: 4, Lines: []string{}},      // rotation
		{Lines: []string{}, NextOffset: 0, Size: 4},                     // steady state
	}

	recorder, cancel, done := h.serveStream(t, "/api/v1/sites/"+h.site.ID+"/logs/stream?name=access.log&filter=req", nil)
	waitFor(t, 5*time.Second, func() bool {
		_, body := recorder.snapshot()
		return len(h.operator.recordedReads()) >= 5 && strings.Contains(body, ": keep-alive\n\n")
	})
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("stream did not stop on request cancel")
	}

	status, body := recorder.snapshot()
	if status != http.StatusOK {
		t.Fatalf("stream status = %d", status)
	}
	if contentType := recorder.Header().Get("Content-Type"); contentType != "text/event-stream" {
		t.Fatalf("content type = %q", contentType)
	}
	if !strings.Contains(body, "id: 100\nevent: lines\ndata: {\"lines\":[\"boot\",\"ready\"],\"rotated\":false}\n\n") {
		t.Fatalf("initial tail event missing from %q", body)
	}
	if !strings.Contains(body, "id: 130\nevent: lines\ndata: {\"lines\":[\"request served\"],\"rotated\":false}\n\n") {
		t.Fatalf("growth event missing from %q", body)
	}
	if !strings.Contains(body, "id: 0\nevent: lines\ndata: {\"lines\":[],\"rotated\":true}\n\n") {
		t.Fatalf("rotation event missing from %q", body)
	}
	// Quiet polls emit nothing: exactly three events despite five reads.
	if events := strings.Count(body, "event: lines"); events != 3 {
		t.Fatalf("event count = %d in %q", events, body)
	}
	if !strings.Contains(body, ": keep-alive\n\n") {
		t.Fatalf("keep-alive comment missing from %q", body)
	}

	// The tail seeds the poll loop: first call is tail mode with the filter,
	// later polls resume from the reported offsets.
	calls := h.operator.recordedReads()
	if calls[0].request.FromOffset != nil || calls[0].request.TailLines != logsoperator.ReadDefaultTail || calls[0].request.Filter != "req" {
		t.Fatalf("initial read = %+v", calls[0].request)
	}
	if calls[1].request.FromOffset == nil || *calls[1].request.FromOffset != 100 || calls[1].request.Filter != "req" {
		t.Fatalf("first poll = %+v", calls[1].request)
	}
	if *calls[3].request.FromOffset != 130 {
		t.Fatalf("post-growth poll = %+v", calls[3].request)
	}
	if *calls[4].request.FromOffset != 0 {
		t.Fatalf("post-rotation poll = %+v", calls[4].request)
	}
}

func TestLogsStreamResumeAndErrors(t *testing.T) {
	h := newHarness(t)
	h.operator.reads = []logsoperator.ReadResult{
		{Lines: []string{"resumed"}, NextOffset: 5000, Size: 5000},
		{Lines: []string{}, NextOffset: 5000, Size: 5000},
	}

	recorder, cancel, done := h.serveStream(t, "/api/v1/sites/"+h.site.ID+"/logs/stream?name=access.log", map[string]string{"Last-Event-ID": "4096"})
	waitFor(t, 5*time.Second, func() bool { return len(h.operator.recordedReads()) >= 2 })
	cancel()
	<-done

	calls := h.operator.recordedReads()
	if calls[0].request.FromOffset == nil || *calls[0].request.FromOffset != 4096 || calls[0].request.TailLines != 0 {
		t.Fatalf("resume read = %+v", calls[0].request)
	}
	if _, body := recorder.snapshot(); !strings.Contains(body, "id: 5000\nevent: lines\ndata: {\"lines\":[\"resumed\"],\"rotated\":false}\n\n") {
		t.Fatalf("resume event missing from %q", body)
	}

	// A malformed Last-Event-ID never opens a stream.
	malformed := httptest.NewRequest(http.MethodGet, "/api/v1/sites/"+h.site.ID+"/logs/stream?name=access.log", nil)
	authenticate(malformed, h.admin)
	malformed.Header.Set("Last-Event-ID", "junk")
	response := httptest.NewRecorder()
	h.mux.ServeHTTP(response, malformed)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("malformed Last-Event-ID = %d %s", response.Code, response.Body.String())
	}

	// A failing first read surfaces as a JSON error, not a broken stream.
	h.operator.fail = &logsoperator.OperationError{Code: logsoperator.CodeNotFound, Message: "The requested log file does not exist."}
	missing := h.do(t, http.MethodGet, "/api/v1/sites/"+h.site.ID+"/logs/stream?name=gone.log", h.admin)
	if missing.Code != http.StatusNotFound || errorCode(t, missing) != logsoperator.CodeNotFound {
		t.Fatalf("missing log stream = %d %s", missing.Code, missing.Body.String())
	}
}

func TestLogsDownloadStreamsAttachment(t *testing.T) {
	h := newHarness(t)

	response := h.do(t, http.MethodGet, "/api/v1/sites/"+h.site.ID+"/logs/download?name=tasks/1.log", h.admin)
	if response.Code != http.StatusOK || response.Body.String() != "log-content\n" {
		t.Fatalf("download = %d %q", response.Code, response.Body.String())
	}
	if disposition := response.Header().Get("Content-Disposition"); !strings.Contains(disposition, "attachment") || !strings.Contains(disposition, "1.log") {
		t.Fatalf("disposition = %q", disposition)
	}
	if response.Header().Get("Content-Length") != "12" {
		t.Fatalf("content length = %q", response.Header().Get("Content-Length"))
	}

	h.operator.fail = &logsoperator.OperationError{Code: logsoperator.CodeInvalid, Message: "Only regular log files can be read."}
	if response := h.do(t, http.MethodGet, "/api/v1/sites/"+h.site.ID+"/logs/download?name=link.log", h.admin); response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("refused download = %d %s", response.Code, response.Body.String())
	}
}
