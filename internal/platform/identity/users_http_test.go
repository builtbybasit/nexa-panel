package identity

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/platform/audit"
	"github.com/nexa-panel/nexa-panel/internal/platform/persistence"
	"github.com/nexa-panel/nexa-panel/internal/platform/secrets"
)

type muxRegistry struct{ mux *http.ServeMux }

func (r muxRegistry) Handle(pattern string, handler http.Handler) error {
	r.mux.Handle(pattern, handler)
	return nil
}

func (r muxRegistry) HandleAuthenticated(pattern string, handler http.Handler) error {
	r.mux.Handle(pattern, handler)
	return nil
}

func (r muxRegistry) HandleAuthorized(pattern, _ string, handler http.Handler) error {
	r.mux.Handle(pattern, handler)
	return nil
}

type fakeSiteDirectory map[string]bool

func (d fakeSiteDirectory) SiteExists(_ context.Context, id string) (bool, error) {
	return d[id], nil
}

type usersHarness struct {
	module *Module
	audit  *audit.Module
	mux    *http.ServeMux
	actor  User
}

func newUsersHarness(t *testing.T) *usersHarness {
	t.Helper()
	database, err := persistence.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := persistence.RunMigrations(context.Background(), database); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	ctx := context.Background()
	auditModule, err := audit.New(ctx, database)
	if err != nil {
		t.Fatalf("create audit module: %v", err)
	}
	secretBox, err := secrets.New(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatalf("create secret box: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	identityModule, err := NewWithConfig(ctx, database, auditModule, secretBox, logger, Config{
		SessionTTL: time.Hour, PasswordMemoryKiB: 64, PasswordIterations: 1, PasswordThreads: 1,
	})
	if err != nil {
		t.Fatalf("create identity module: %v", err)
	}
	currentTime := time.Unix(1_700_000_000, 0).UTC()
	identityModule.now = func() time.Time { return currentTime }
	mux := http.NewServeMux()
	if err := identityModule.Register(muxRegistry{mux}); err != nil {
		t.Fatalf("register identity routes: %v", err)
	}
	return &usersHarness{module: identityModule, audit: auditModule, mux: mux}
}

func (h *usersHarness) seedUser(t *testing.T, username, role string) User {
	t.Helper()
	hash, err := hashPassword("a-strong-password", h.module.parameters)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	model := &userModel{
		ID: randomID(16), Username: username, PasswordHash: hash, Role: role,
		CreatedAt: h.module.now().UTC(), RecoveryCodeHashes: "[]",
	}
	if _, err := h.module.database.NewInsert().Model(model).Exec(context.Background()); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	return User{ID: model.ID, Username: username, Role: role}
}

func (h *usersHarness) seedSession(t *testing.T, userID string) {
	t.Helper()
	now := h.module.now().UTC()
	model := &sessionModel{
		ID: randomID(16), UserID: userID, TokenHash: []byte(randomID(16)), CreatedAt: now,
		ExpiresAt: now.Add(time.Hour), LastSeenAt: now, RemoteAddress: "", UserAgent: "",
	}
	if _, err := h.module.database.NewInsert().Model(model).Exec(context.Background()); err != nil {
		t.Fatalf("seed session: %v", err)
	}
}

func (h *usersHarness) sessionCount(t *testing.T, userID string) int {
	t.Helper()
	count, err := h.module.database.NewSelect().Model((*sessionModel)(nil)).
		Where("user_id = ?", userID).Count(context.Background())
	if err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	return count
}

func (h *usersHarness) do(method, path, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if h.actor.ID != "" {
		request = request.WithContext(context.WithValue(request.Context(), principalContextKey{}, principal{User: h.actor}))
	}
	response := httptest.NewRecorder()
	h.mux.ServeHTTP(response, request)
	return response
}

func decodeManagedUser(t *testing.T, body *bytes.Buffer) ManagedUser {
	t.Helper()
	var user ManagedUser
	if err := json.Unmarshal(body.Bytes(), &user); err != nil {
		t.Fatalf("decode managed user: %v", err)
	}
	return user
}

func TestUserCRUDHandlers(t *testing.T) {
	h := newUsersHarness(t)
	h.actor = h.seedUser(t, "root", "admin")

	created := h.do(http.MethodPost, "/api/v1/users", `{"username":"dev-anna","password":"a-strong-password","role":"developer"}`)
	if created.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", created.Code, created.Body.String())
	}
	developer := decodeManagedUser(t, created.Body)
	if developer.Username != "dev-anna" || developer.Role != "developer" || developer.MFAConfirmed || len(developer.SiteIDs) != 0 {
		t.Fatalf("unexpected created user: %+v", developer)
	}

	duplicate := h.do(http.MethodPost, "/api/v1/users", `{"username":"dev-anna","password":"a-strong-password","role":"viewer"}`)
	if duplicate.Code != http.StatusConflict {
		t.Fatalf("duplicate username = %d %s", duplicate.Code, duplicate.Body.String())
	}
	badRole := h.do(http.MethodPost, "/api/v1/users", `{"username":"other","password":"a-strong-password","role":"superuser"}`)
	if badRole.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid role = %d %s", badRole.Code, badRole.Body.String())
	}
	shortPassword := h.do(http.MethodPost, "/api/v1/users", `{"username":"other","password":"short","role":"viewer"}`)
	if shortPassword.Code != http.StatusUnprocessableEntity {
		t.Fatalf("short password = %d %s", shortPassword.Code, shortPassword.Body.String())
	}

	list := h.do(http.MethodGet, "/api/v1/users", "")
	if list.Code != http.StatusOK {
		t.Fatalf("list = %d %s", list.Code, list.Body.String())
	}
	var listing struct {
		Items []ManagedUser `json:"items"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &listing); err != nil {
		t.Fatalf("decode listing: %v", err)
	}
	if len(listing.Items) != 2 || listing.Items[0].Username != "dev-anna" || listing.Items[1].Username != "root" {
		t.Fatalf("unexpected listing: %+v", listing.Items)
	}
	if listing.Items[0].SiteIDs == nil {
		t.Fatal("siteIds must serialize as an empty array")
	}

	updated := h.do(http.MethodPatch, "/api/v1/users/"+developer.ID, `{"role":"viewer"}`)
	if updated.Code != http.StatusOK {
		t.Fatalf("update = %d %s", updated.Code, updated.Body.String())
	}
	if user := decodeManagedUser(t, updated.Body); user.Role != "viewer" {
		t.Fatalf("role after update = %q", user.Role)
	}
	missing := h.do(http.MethodPatch, "/api/v1/users/absent", `{"role":"viewer"}`)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("update missing = %d %s", missing.Code, missing.Body.String())
	}
	empty := h.do(http.MethodPatch, "/api/v1/users/"+developer.ID, `{}`)
	if empty.Code != http.StatusBadRequest {
		t.Fatalf("empty update = %d %s", empty.Code, empty.Body.String())
	}

	// A new user has no second factor, so login signs straight in; MFA can be
	// enabled later from account security.
	login := h.do(http.MethodPost, "/api/v1/auth/login", `{"username":"dev-anna","password":"a-strong-password"}`)
	if login.Code != http.StatusOK || !strings.Contains(login.Body.String(), `"next":"authenticated"`) {
		t.Fatalf("new user login = %d %s", login.Code, login.Body.String())
	}

	deleted := h.do(http.MethodDelete, "/api/v1/users/"+developer.ID, "")
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("delete = %d %s", deleted.Code, deleted.Body.String())
	}
	deletedAgain := h.do(http.MethodDelete, "/api/v1/users/"+developer.ID, "")
	if deletedAgain.Code != http.StatusNotFound {
		t.Fatalf("delete missing = %d %s", deletedAgain.Code, deletedAgain.Body.String())
	}

	events, err := h.audit.List(context.Background(), 50)
	if err != nil {
		t.Fatalf("list audit events: %v", err)
	}
	wanted := map[string]bool{"identity.user_created": false, "identity.user_updated": false, "identity.user_deleted": false}
	for _, event := range events {
		if _, ok := wanted[event.Action]; ok {
			wanted[event.Action] = true
		}
	}
	for action, seen := range wanted {
		if !seen {
			t.Errorf("audit event %q was not recorded", action)
		}
	}
}

func TestUserGuards(t *testing.T) {
	h := newUsersHarness(t)
	admin := h.seedUser(t, "root", "admin")
	h.actor = admin

	selfDelete := h.do(http.MethodDelete, "/api/v1/users/"+admin.ID, "")
	if selfDelete.Code != http.StatusConflict {
		t.Fatalf("self delete = %d %s", selfDelete.Code, selfDelete.Body.String())
	}
	demote := h.do(http.MethodPatch, "/api/v1/users/"+admin.ID, `{"role":"viewer"}`)
	if demote.Code != http.StatusConflict || !strings.Contains(demote.Body.String(), "last_admin") {
		t.Fatalf("demote last admin = %d %s", demote.Code, demote.Body.String())
	}

	developer := h.seedUser(t, "dev", "developer")
	h.actor = developer
	lastAdminDelete := h.do(http.MethodDelete, "/api/v1/users/"+admin.ID, "")
	if lastAdminDelete.Code != http.StatusConflict || !strings.Contains(lastAdminDelete.Body.String(), "last_admin") {
		t.Fatalf("delete last admin = %d %s", lastAdminDelete.Code, lastAdminDelete.Body.String())
	}

	h.actor = admin
	second := h.seedUser(t, "root2", "admin")
	demoteWithBackup := h.do(http.MethodPatch, "/api/v1/users/"+second.ID, `{"role":"operator"}`)
	if demoteWithBackup.Code != http.StatusOK {
		t.Fatalf("demote with second admin = %d %s", demoteWithBackup.Code, demoteWithBackup.Body.String())
	}

	// Role or password changes revoke every session of the target user.
	target := h.seedUser(t, "op1", "operator")
	h.seedSession(t, target.ID)
	h.seedSession(t, target.ID)
	if count := h.sessionCount(t, target.ID); count != 2 {
		t.Fatalf("seeded session count = %d", count)
	}
	password := h.do(http.MethodPatch, "/api/v1/users/"+target.ID, `{"password":"another-strong-password"}`)
	if password.Code != http.StatusOK {
		t.Fatalf("password update = %d %s", password.Code, password.Body.String())
	}
	if count := h.sessionCount(t, target.ID); count != 0 {
		t.Fatalf("sessions after password change = %d, want 0", count)
	}
}

func TestSiteGrantsReplaceAndScoping(t *testing.T) {
	h := newUsersHarness(t)
	h.actor = h.seedUser(t, "root", "admin")
	developer := h.seedUser(t, "dev", "developer")
	operator := h.seedUser(t, "op", "operator")

	unavailable := h.do(http.MethodPut, "/api/v1/users/"+developer.ID+"/sites", `{"siteIds":["site_a"]}`)
	if unavailable.Code != http.StatusServiceUnavailable {
		t.Fatalf("grants without directory = %d %s", unavailable.Code, unavailable.Body.String())
	}

	h.module.SetSiteDirectory(fakeSiteDirectory{"site_a": true, "site_b": true})

	granted := h.do(http.MethodPut, "/api/v1/users/"+developer.ID+"/sites", `{"siteIds":["site_b","site_a","site_b"]}`)
	if granted.Code != http.StatusOK {
		t.Fatalf("replace grants = %d %s", granted.Code, granted.Body.String())
	}
	var grantedBody struct {
		SiteIDs []string `json:"siteIds"`
	}
	if err := json.Unmarshal(granted.Body.Bytes(), &grantedBody); err != nil {
		t.Fatalf("decode grants: %v", err)
	}
	if len(grantedBody.SiteIDs) != 2 {
		t.Fatalf("granted site count = %d, want deduplicated 2", len(grantedBody.SiteIDs))
	}

	unknown := h.do(http.MethodPut, "/api/v1/users/"+developer.ID+"/sites", `{"siteIds":["site_missing"]}`)
	if unknown.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unknown site = %d %s", unknown.Code, unknown.Body.String())
	}
	notDeveloper := h.do(http.MethodPut, "/api/v1/users/"+operator.ID+"/sites", `{"siteIds":["site_a"]}`)
	if notDeveloper.Code != http.StatusBadRequest {
		t.Fatalf("grants for operator = %d %s", notDeveloper.Code, notDeveloper.Body.String())
	}
	missingUser := h.do(http.MethodPut, "/api/v1/users/absent/sites", `{"siteIds":["site_a"]}`)
	if missingUser.Code != http.StatusNotFound {
		t.Fatalf("grants for missing user = %d %s", missingUser.Code, missingUser.Body.String())
	}

	ctx := context.Background()
	if accessible, err := h.module.SiteAccessible(ctx, developer, "site_a"); err != nil || !accessible {
		t.Fatalf("SiteAccessible(dev, site_a) = %v, %v", accessible, err)
	}
	if accessible, err := h.module.SiteAccessible(ctx, developer, "site_c"); err != nil || accessible {
		t.Fatalf("SiteAccessible(dev, site_c) = %v, %v", accessible, err)
	}
	if accessible, err := h.module.SiteAccessible(ctx, h.actor, "site_c"); err != nil || !accessible {
		t.Fatalf("SiteAccessible(admin, site_c) = %v, %v", accessible, err)
	}
	all, ids, err := h.module.AccessibleSiteIDs(ctx, developer)
	if err != nil || all || len(ids) != 2 || ids[0] != "site_a" || ids[1] != "site_b" {
		t.Fatalf("AccessibleSiteIDs(dev) = %v, %v, %v", all, ids, err)
	}
	all, ids, err = h.module.AccessibleSiteIDs(ctx, h.actor)
	if err != nil || !all || ids != nil {
		t.Fatalf("AccessibleSiteIDs(admin) = %v, %v, %v", all, ids, err)
	}

	cleared := h.do(http.MethodPut, "/api/v1/users/"+developer.ID+"/sites", `{"siteIds":[]}`)
	if cleared.Code != http.StatusOK {
		t.Fatalf("clear grants = %d %s", cleared.Code, cleared.Body.String())
	}
	all, ids, err = h.module.AccessibleSiteIDs(ctx, developer)
	if err != nil || all || len(ids) != 0 {
		t.Fatalf("AccessibleSiteIDs after clear = %v, %v, %v", all, ids, err)
	}

	events, err := h.audit.List(ctx, 50)
	if err != nil {
		t.Fatalf("list audit events: %v", err)
	}
	replaced := 0
	for _, event := range events {
		if event.Action == "identity.user_site_grants_replaced" {
			replaced++
		}
	}
	if replaced != 2 {
		t.Fatalf("grant replacement audit events = %d, want 2", replaced)
	}
}
