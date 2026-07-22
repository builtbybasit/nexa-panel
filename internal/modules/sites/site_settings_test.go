package sites

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/platform/audit"
	"github.com/nexa-panel/nexa-panel/internal/platform/authorization"
	"github.com/nexa-panel/nexa-panel/internal/platform/identity"
	"github.com/nexa-panel/nexa-panel/internal/platform/jobs"
	siteoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/sites"
	"github.com/nexa-panel/nexa-panel/internal/platform/persistence"
	"github.com/nexa-panel/nexa-panel/internal/platform/secrets"
)

// capturingOperator records the last site definition it was asked to plan, so a
// test can assert a settings change re-rendered the *complete* vhost — routes,
// TLS, and settings together — not a bare primary-domain view.
type capturingOperator struct {
	fakeSiteOperator
	lastPlanned *siteoperator.Site
}

// PlanTeardown mirrors the real operator: the same rendered plan, but with an
// empty before-state so a rollback removes every managed path.
func (o *capturingOperator) PlanTeardown(ctx context.Context, site siteoperator.Site) (siteoperator.Plan, error) {
	plan, err := o.Plan(ctx, site)
	if err != nil {
		return siteoperator.Plan{}, err
	}
	plan.Before = make([]siteoperator.Snapshot, len(plan.Artifacts))
	for index := range plan.Artifacts {
		plan.Before[index] = siteoperator.Snapshot{Path: plan.Artifacts[index].Path}
	}
	plan.RetiredBefore = make([]siteoperator.Snapshot, len(plan.Retired))
	for index, path := range plan.Retired {
		plan.RetiredBefore[index] = siteoperator.Snapshot{Path: path}
	}
	plan.EnabledBefore = false
	return plan, nil
}

func (o *capturingOperator) Plan(ctx context.Context, site siteoperator.Site) (siteoperator.Plan, error) {
	o.lastPlanned = &site
	return o.fakeSiteOperator.Plan(ctx, site)
}

// stubRouting / stubTLS stand in for the domains and certificates modules so
// settingsJob can re-derive an active site's full definition without them.
type stubRouting struct{ routes []siteoperator.Route }

func (s stubRouting) Routing(context.Context, string, string) ([]siteoperator.Route, error) {
	return s.routes, nil
}

type stubTLS struct {
	tls     *siteoperator.TLS
	domains []string
}

func (s stubTLS) TLSForSite(context.Context, string) (*siteoperator.TLS, []string, error) {
	return s.tls, s.domains, nil
}

func newSettingsModule(t *testing.T, operator siteoperator.Operator) (*Module, *jobs.Module, context.Context) {
	t.Helper()
	ctx := context.Background()
	database, err := persistence.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := persistence.RunMigrations(ctx, database); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	auditLog, err := audit.New(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	queue, err := jobs.NewWithConfig(ctx, database, auditLog, slog.New(slog.NewTextHandler(io.Discard, nil)), jobs.Config{PollInterval: 5 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	module, err := New(ctx, database, queue, runtimeCatalog{"8.4": true}, operator)
	if err != nil {
		t.Fatal(err)
	}
	queue.Start(context.Background())
	t.Cleanup(queue.Close)
	return module, queue, ctx
}

// createSite creates a site and waits for its background planning job to finish,
// so a later synchronous operator.Plan call is not racing the create's own plan
// (which would otherwise overwrite a captured plan with the bare initial view).
func createSite(t *testing.T, module *Module, queue *jobs.Module, ctx context.Context, slug, domain string) Site {
	t.Helper()
	site, job, err := module.Create(ctx, CreateRequest{Slug: slug, DisplayName: slug, PrimaryDomain: domain, PHPVersion: "8.4"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitForJob(t, queue, job.ID)
	return site
}

func persistSettings(t *testing.T, module *Module, ctx context.Context, siteID string, settings siteoperator.Settings) {
	t.Helper()
	encoded, _ := json.Marshal(settings)
	stored := string(encoded)
	if _, err := module.database.NewUpdate().Model((*siteModel)(nil)).Set("settings_json = ?", stored).Where("id = ?", siteID).Exec(ctx); err != nil {
		t.Fatalf("persist settings: %v", err)
	}
}

// setStatus forces a site into a lifecycle state so the settings handler's state
// machine can be exercised without driving the whole plan/activate flow.
func setStatus(t *testing.T, m *Module, siteID string, status Status) {
	t.Helper()
	if _, err := m.database.NewUpdate().Model((*siteModel)(nil)).Set("status = ?", status).Where("id = ?", siteID).Exec(context.Background()); err != nil {
		t.Fatalf("set status: %v", err)
	}
}

func TestDefinitionMergesRoutesTLSAndSettings(t *testing.T) {
	module, queue, ctx := newSettingsModule(t, fakeSiteOperator{})
	site := createSite(t, module, queue, ctx, "demo-site", "demo.example.com")

	// A brand-new site has no settings column -> zero-value Settings (baseline).
	def, err := module.Definition(ctx, site.ID, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Settings carries a slice (IndexFiles), so it is no longer comparable with ==.
	if !reflect.DeepEqual(def.Settings, siteoperator.Settings{}) {
		t.Fatalf("expected baseline settings for untouched site, got %+v", def.Settings)
	}

	body := 128
	persistSettings(t, module, ctx, site.ID, siteoperator.Settings{HTTP3: true, ClientMaxBodyMB: &body})
	routes := []siteoperator.Route{{Hostname: "www.demo.example.com", Kind: "alias"}}
	tls := &siteoperator.TLS{CertificatePath: "/etc/letsencrypt/live/demo.example.com/fullchain.pem", PrivateKeyPath: "/etc/letsencrypt/live/demo.example.com/privkey.pem"}
	def, err = module.Definition(ctx, site.ID, routes, tls, []string{"demo.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if !def.Settings.HTTP3 || def.Settings.ClientMaxBodyMB == nil || *def.Settings.ClientMaxBodyMB != 128 {
		t.Fatalf("settings not merged: %+v", def.Settings)
	}
	if len(def.Routes) != 1 || def.TLS == nil {
		t.Fatalf("routes/TLS not merged: routes=%+v tls=%+v", def.Routes, def.TLS)
	}
}

func TestGetBlanksPasswordHashButRetainsItInternally(t *testing.T) {
	module, queue, ctx := newSettingsModule(t, fakeSiteOperator{})
	site := createSite(t, module, queue, ctx, "demo-site", "demo.example.com")
	persistSettings(t, module, ctx, site.ID, siteoperator.Settings{BasicAuth: siteoperator.BasicAuth{Enabled: true, Username: "deploy", PasswordHash: "$2y$10$abcdefghijklmnopqrstuv"}})

	got, err := module.Get(ctx, site.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Settings.BasicAuth.PasswordHash != "" {
		t.Fatalf("password hash leaked to API surface: %q", got.Settings.BasicAuth.PasswordHash)
	}
	if !got.Settings.BasicAuth.Enabled || got.Settings.BasicAuth.Username != "deploy" {
		t.Fatalf("non-secret basic-auth fields dropped: %+v", got.Settings.BasicAuth)
	}
	internal, err := module.loadSettings(ctx, site.ID)
	if err != nil {
		t.Fatal(err)
	}
	if internal.BasicAuth.PasswordHash == "" {
		t.Fatal("stored hash must be retained for the operator to re-render")
	}
}

func TestNormalizeSettingsHashesPreservesAndValidates(t *testing.T) {
	// A supplied plaintext password is hashed, never stored verbatim.
	hashed, err := normalizeSettings(siteoperator.Settings{}, SettingsRequest{
		BasicAuth: BasicAuthRequest{Enabled: true, Username: "deploy", Password: "s3cret-passw0rd"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if hashed.BasicAuth.PasswordHash == "" || hashed.BasicAuth.PasswordHash == "s3cret-passw0rd" {
		t.Fatalf("password must be hashed, got %q", hashed.BasicAuth.PasswordHash)
	}

	// Re-saving with a blank password and the same username keeps the old hash.
	current := siteoperator.Settings{BasicAuth: siteoperator.BasicAuth{Enabled: true, Username: "deploy", PasswordHash: hashed.BasicAuth.PasswordHash}}
	kept, err := normalizeSettings(current, SettingsRequest{BasicAuth: BasicAuthRequest{Enabled: true, Username: "deploy"}, HSTS: true})
	if err != nil {
		t.Fatal(err)
	}
	if kept.BasicAuth.PasswordHash != hashed.BasicAuth.PasswordHash {
		t.Fatal("blank password on re-save must preserve the existing credential")
	}

	// Enabling basic auth with no password and no prior credential is rejected.
	if _, err := normalizeSettings(siteoperator.Settings{}, SettingsRequest{BasicAuth: BasicAuthRequest{Enabled: true, Username: "deploy"}}); err == nil {
		t.Fatal("expected error enabling basic auth without a password")
	}

	// Out-of-bounds values are rejected at the boundary.
	if _, err := normalizeSettings(siteoperator.Settings{}, SettingsRequest{GzipLevel: 99}); err == nil {
		t.Fatal("expected validation error for out-of-bounds gzip level")
	}
}

func TestNormalizeSettingsTidiesAndGuardsSubdirectory(t *testing.T) {
	// The boundary is forgiving about the slashes a user is likely to type.
	for input, want := range map[string]string{
		"public/":      "public",
		"/public":      "public",
		"  app/public": "app/public",
		"":             "",
	} {
		got, err := normalizeSettings(siteoperator.Settings{}, SettingsRequest{Subdirectory: input})
		if err != nil {
			t.Fatalf("subdirectory %q should normalize, got %v", input, err)
		}
		if got.Subdirectory != want {
			t.Fatalf("subdirectory %q normalized to %q, want %q", input, got.Subdirectory, want)
		}
	}
	// But traversal is refused outright — it must never reach the operator.
	for _, unsafe := range []string{"../private", "app/../../logs", "app//public", `app\public`} {
		if _, err := normalizeSettings(siteoperator.Settings{}, SettingsRequest{Subdirectory: unsafe}); err == nil {
			t.Fatalf("unsafe subdirectory %q must be rejected", unsafe)
		}
	}
}

func TestNormalizeSettingsTidiesApplicationFilesAndWorkingDirectory(t *testing.T) {
	got, err := normalizeSettings(siteoperator.Settings{}, SettingsRequest{
		IndexFiles: []string{" app.php ", "", "index.html"}, WorkingDirectory: "/public/",
	})
	if err != nil {
		t.Fatalf("expected a tidied payload to be accepted: %v", err)
	}
	if len(got.IndexFiles) != 2 || got.IndexFiles[0] != "app.php" || got.IndexFiles[1] != "index.html" {
		t.Fatalf("application files not tidied in order: %+v", got.IndexFiles)
	}
	if got.WorkingDirectory != "public" {
		t.Fatalf("working subdirectory normalized to %q, want %q", got.WorkingDirectory, "public")
	}
	// An all-blank list collapses to the baseline rather than failing.
	blank, err := normalizeSettings(siteoperator.Settings{}, SettingsRequest{IndexFiles: []string{"", "  "}})
	if err != nil || blank.IndexFiles != nil {
		t.Fatalf("blank application file list should collapse to the baseline, got %+v (%v)", blank.IndexFiles, err)
	}
	// And traversal never reaches the operator.
	for _, unsafe := range []string{"../other", `app\public`, "app//public"} {
		if _, err := normalizeSettings(siteoperator.Settings{}, SettingsRequest{WorkingDirectory: unsafe}); err == nil {
			t.Fatalf("unsafe working subdirectory %q must be rejected", unsafe)
		}
	}
	if _, err := normalizeSettings(siteoperator.Settings{}, SettingsRequest{IndexFiles: []string{"a/b.php"}}); err == nil {
		t.Fatal("an application file with a separator must be rejected")
	}
}

// The boundary canonicalises the charset so the persisted token — and therefore
// the rendered byte — is identical however the caller spelled it.
func TestNormalizeSettingsCanonicalisesCharset(t *testing.T) {
	for input, want := range map[string]string{
		"UTF-8":   "utf-8",
		" utf-8 ": "utf-8",
		"OFF":     "off",
		"":        "",
	} {
		got, err := normalizeSettings(siteoperator.Settings{}, SettingsRequest{Charset: input})
		if err != nil {
			t.Fatalf("charset %q should normalize, got %v", input, err)
		}
		if got.Charset != want {
			t.Fatalf("charset %q normalized to %q, want %q", input, got.Charset, want)
		}
	}
	for _, unsupported := range []string{"utf8", "latin1", "utf-8; root /etc", "unicode"} {
		if _, err := normalizeSettings(siteoperator.Settings{}, SettingsRequest{Charset: unsupported}); err == nil {
			t.Fatalf("unsupported charset %q must be rejected", unsupported)
		}
	}
}

// The log-rotation frequency is emitted verbatim into the logrotate stanza, so
// the boundary lowercases and trims it before the operator's allowlist runs.
func TestNormalizeSettingsCanonicalisesLogRotationFrequency(t *testing.T) {
	got, err := normalizeSettings(siteoperator.Settings{}, SettingsRequest{
		LogRotation: siteoperator.LogRotation{Enabled: true, KeepFiles: 7, Frequency: " Weekly "},
	})
	if err != nil {
		t.Fatalf("expected a tidied rotation frequency to be accepted: %v", err)
	}
	if got.LogRotation.Frequency != "weekly" || got.LogRotation.KeepFiles != 7 || !got.LogRotation.Enabled {
		t.Fatalf("log rotation not threaded through: %+v", got.LogRotation)
	}
	if _, err := normalizeSettings(siteoperator.Settings{}, SettingsRequest{
		LogRotation: siteoperator.LogRotation{Enabled: true, Frequency: "monthly"},
	}); err == nil {
		t.Fatal("an unsupported rotation frequency must be rejected")
	}
}

// The anti-regression guard: settingsJob on an ACTIVE site must re-render the
// complete definition — settings AND the extra routes AND TLS — so it can never
// strip a site's domains or certificate the way a bare re-plan would.
func TestSettingsJobPreservesRoutesAndTLS(t *testing.T) {
	operator := &capturingOperator{}
	module, queue, ctx := newSettingsModule(t, operator)
	tls := &siteoperator.TLS{CertificatePath: "/etc/letsencrypt/live/demo.example.com/fullchain.pem", PrivateKeyPath: "/etc/letsencrypt/live/demo.example.com/privkey.pem"}
	module.SetRouteSource(stubRouting{routes: []siteoperator.Route{{Hostname: "www.demo.example.com", Kind: "alias"}}})
	module.SetTLSProvider(stubTLS{tls: tls, domains: []string{"demo.example.com"}})

	site := createSite(t, module, queue, ctx, "demo-site", "demo.example.com")
	persistSettings(t, module, ctx, site.ID, siteoperator.Settings{HSTS: true})
	setStatus(t, module, site.ID, StatusActive)

	if _, err := module.settingsJob(ctx, mustJSON(t, map[string]string{"siteId": site.ID}), func(int, string) error { return nil }); err != nil {
		t.Fatalf("settingsJob: %v", err)
	}
	if operator.lastPlanned == nil {
		t.Fatal("operator was never asked to plan the settings change")
	}
	planned := *operator.lastPlanned
	if !planned.Settings.HSTS {
		t.Fatalf("settings not threaded into the active-site re-render: %+v", planned.Settings)
	}
	if len(planned.Routes) != 1 || planned.Routes[0].Hostname != "www.demo.example.com" {
		t.Fatalf("active-site settings edit dropped routes: %+v", planned.Routes)
	}
	if planned.TLS == nil || planned.TLS.CertificatePath != tls.CertificatePath {
		t.Fatalf("active-site settings edit dropped TLS: %+v", planned.TLS)
	}
	updated, err := module.Get(ctx, site.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != StatusActive {
		t.Fatalf("site status after settings apply = %s, want active", updated.Status)
	}
}

// TestUpdateSettingsHTTPLifecycle drives the PATCH handler through the real
// identity + authorization middleware to assert the state-dependent responses:
// 422 invalid, 409 busy, 200 persist-only for a settled site, 202 for active.
func TestUpdateSettingsHTTPLifecycle(t *testing.T) {
	ctx := context.Background()
	database, err := persistence.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := persistence.RunMigrations(ctx, database); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	auditLog, err := audit.New(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
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
	module, err := New(ctx, database, queue, runtimeCatalog{"8.4": true}, fakeSiteOperator{})
	if err != nil {
		t.Fatal(err)
	}
	queue.Start(context.Background())
	t.Cleanup(queue.Close)

	site := createSite(t, module, queue, ctx, "demo-site", "demo.example.com")

	mux := http.NewServeMux()
	registry := authenticatedRegistry{mux: mux, authentication: identityModule, authorization: authorization.New()}
	if err := module.Register(registry); err != nil {
		t.Fatal(err)
	}
	cookie := seedIdentitySession(t, database, "admin-1", "root", "admin", strings.Repeat("a", 64))

	patch := func(body SettingsRequest) *httptest.ResponseRecorder {
		encoded, _ := json.Marshal(body)
		request := httptest.NewRequest(http.MethodPatch, "/api/v1/sites/"+site.ID+"/settings", bytes.NewReader(encoded))
		request.Header.Set("Content-Type", "application/json")
		authenticate(request, cookie)
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		return response
	}

	setStatus(t, module, site.ID, StatusDraft)
	if rec := patch(SettingsRequest{GzipLevel: 99}); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid settings = %d, want 422: %s", rec.Code, rec.Body.String())
	}

	setStatus(t, module, site.ID, StatusActivating)
	if rec := patch(SettingsRequest{HSTS: true}); rec.Code != http.StatusConflict {
		t.Fatalf("mid-operation edit = %d, want 409: %s", rec.Code, rec.Body.String())
	}

	setStatus(t, module, site.ID, StatusDraft)
	rec := patch(SettingsRequest{HSTS: true, PMMode: "dynamic", PMMaxChildren: 8})
	if rec.Code != http.StatusOK {
		t.Fatalf("settled-site edit = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var settled Site
	if err := json.Unmarshal(rec.Body.Bytes(), &settled); err != nil {
		t.Fatal(err)
	}
	if !settled.Settings.HSTS || settled.Settings.PMMode != "dynamic" {
		t.Fatalf("persist-only edit did not return updated settings: %+v", settled.Settings)
	}

	setStatus(t, module, site.ID, StatusActive)
	if rec := patch(SettingsRequest{ClientMaxBodyMB: ptrIntValue(256)}); rec.Code != http.StatusAccepted {
		t.Fatalf("active-site edit = %d, want 202: %s", rec.Code, rec.Body.String())
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	encoded, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func ptrIntValue(v int) *int { return &v }
