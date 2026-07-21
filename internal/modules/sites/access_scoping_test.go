package sites

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/platform/audit"
	"github.com/nexa-panel/nexa-panel/internal/platform/authorization"
	"github.com/nexa-panel/nexa-panel/internal/platform/identity"
	"github.com/nexa-panel/nexa-panel/internal/platform/jobs"
	"github.com/nexa-panel/nexa-panel/internal/platform/persistence"
	"github.com/nexa-panel/nexa-panel/internal/platform/secrets"
	"github.com/uptrace/bun"
)

// fakeAccessPolicy scopes developers to an explicit allow list, mirroring the
// identity module's grant semantics.
type fakeAccessPolicy struct{ allowed map[string]bool }

func (p fakeAccessPolicy) SiteAccessible(_ context.Context, user identity.User, siteID string) (bool, error) {
	if user.Role != "developer" {
		return true, nil
	}
	return p.allowed[siteID], nil
}

func (p fakeAccessPolicy) AccessibleSiteIDs(_ context.Context, user identity.User) (bool, []string, error) {
	if user.Role != "developer" {
		return true, nil, nil
	}
	ids := make([]string, 0, len(p.allowed))
	for id, granted := range p.allowed {
		if granted {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return false, ids, nil
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
	if _, err := database.ExecContext(ctx,
		`INSERT INTO identity_sessions (id, user_id, token_hash, created_at, expires_at, last_seen_at, mfa_verified_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"session_"+id, id, hash[:], now, now.Add(time.Hour), now, now); err != nil {
		t.Fatalf("seed session for %s: %v", username, err)
	}
	return &http.Cookie{Name: "nexa_session", Value: token}
}

func TestDeveloperSiteScoping(t *testing.T) {
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
	secretBox, err := secrets.New(bytes.Repeat([]byte{5}, 32))
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
	sitesModule, err := New(ctx, database, queue, runtimeCatalog{"8.4": true}, fakeSiteOperator{})
	if err != nil {
		t.Fatal(err)
	}
	queue.Start(context.Background())
	t.Cleanup(queue.Close)

	granted, grantedJob, err := sitesModule.Create(ctx, CreateRequest{Slug: "granted", DisplayName: "Granted", PrimaryDomain: "granted.example.com", PHPVersion: "8.4"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	hidden, hiddenJob, err := sitesModule.Create(ctx, CreateRequest{Slug: "hidden", DisplayName: "Hidden", PrimaryDomain: "hidden.example.com", PHPVersion: "8.4"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitForJob(t, queue, grantedJob.ID)
	waitForJob(t, queue, hiddenJob.ID)

	if exists, err := sitesModule.SiteExists(ctx, granted.ID); err != nil || !exists {
		t.Fatalf("SiteExists(granted) = %v, %v", exists, err)
	}
	if exists, err := sitesModule.SiteExists(ctx, "site_absent"); err != nil || exists {
		t.Fatalf("SiteExists(absent) = %v, %v", exists, err)
	}

	sitesModule.SetAccessPolicy(fakeAccessPolicy{allowed: map[string]bool{granted.ID: true}})
	mux := http.NewServeMux()
	registry := authenticatedRegistry{mux: mux, authentication: identityModule, authorization: authorization.New()}
	if err := sitesModule.Register(registry); err != nil {
		t.Fatal(err)
	}

	adminCookie := seedIdentitySession(t, database, "admin-1", "root", "admin", strings.Repeat("a", 64))
	developerCookie := seedIdentitySession(t, database, "dev-1", "dev", "developer", strings.Repeat("b", 64))

	get := func(path string, cookie *http.Cookie) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.AddCookie(cookie)
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		return response
	}
	listSites := func(cookie *http.Cookie) []Site {
		response := get("/api/v1/sites", cookie)
		if response.Code != http.StatusOK {
			t.Fatalf("list = %d %s", response.Code, response.Body.String())
		}
		var listing struct {
			Items []Site `json:"items"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &listing); err != nil {
			t.Fatalf("decode listing: %v", err)
		}
		return listing.Items
	}

	if items := listSites(adminCookie); len(items) != 2 {
		t.Fatalf("admin list count = %d, want 2", len(items))
	}
	items := listSites(developerCookie)
	if len(items) != 1 || items[0].ID != granted.ID {
		t.Fatalf("developer list = %+v, want only granted site", items)
	}

	if response := get("/api/v1/sites/"+granted.ID, developerCookie); response.Code != http.StatusOK {
		t.Fatalf("developer get granted = %d %s", response.Code, response.Body.String())
	}
	if response := get("/api/v1/sites/"+hidden.ID, developerCookie); response.Code != http.StatusNotFound {
		t.Fatalf("developer get hidden = %d %s", response.Code, response.Body.String())
	}
	if response := get("/api/v1/sites/"+hidden.ID, adminCookie); response.Code != http.StatusOK {
		t.Fatalf("admin get hidden = %d %s", response.Code, response.Body.String())
	}

	if response := get("/api/v1/sites/"+granted.ID+"/plan", developerCookie); response.Code != http.StatusOK {
		t.Fatalf("developer plan granted = %d %s", response.Code, response.Body.String())
	}
	if response := get("/api/v1/sites/"+hidden.ID+"/plan", developerCookie); response.Code != http.StatusNotFound {
		t.Fatalf("developer plan hidden = %d %s", response.Code, response.Body.String())
	}
}
