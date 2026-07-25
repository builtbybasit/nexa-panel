package webhandler

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nexa-panel/nexa-panel/internal/platform/httpapi"
	"github.com/nexa-panel/nexa-panel/internal/platform/httpapi/apispec"
)

// fakeRegistry records what each Handle* variant was asked to register so the
// spec-to-route resolution can be asserted without a live control panel.
type fakeRegistry struct {
	authorized    map[string]string // pattern -> permission
	authenticated []string
	open          []string
}

func newFakeRegistry() *fakeRegistry {
	return &fakeRegistry{authorized: map[string]string{}}
}

func (f *fakeRegistry) Handle(pattern string, _ http.Handler) error {
	f.open = append(f.open, pattern)
	return nil
}

func (f *fakeRegistry) HandleAuthenticated(pattern string, _ http.Handler) error {
	f.authenticated = append(f.authenticated, pattern)
	return nil
}

func (f *fakeRegistry) HandleAuthorized(pattern, permission string, _ http.Handler) error {
	f.authorized[pattern] = permission
	return nil
}

func TestHandleOpResolvesPatternAndPermissionFromSpec(t *testing.T) {
	registry := newFakeRegistry()
	if err := HandleOp(registry, "createDatabaseUser", func(http.ResponseWriter, *http.Request) {}); err != nil {
		t.Fatalf("HandleOp: %v", err)
	}
	const pattern = "POST /api/v1/databases/users"
	got, ok := registry.authorized[pattern]
	if !ok {
		t.Fatalf("expected authorized registration for %q, got %+v", pattern, registry.authorized)
	}
	if got != "databases.write" {
		t.Fatalf("permission = %q, want databases.write", got)
	}
}

func TestHandleOpRejectsUnknownOperation(t *testing.T) {
	registry := newFakeRegistry()
	err := HandleOp(registry, "operationThatDoesNotExist", func(http.ResponseWriter, *http.Request) {})
	if err == nil {
		t.Fatal("expected error for unknown operationId, got nil")
	}
}

func TestRegisterBindsEveryOperation(t *testing.T) {
	registry := newFakeRegistry()
	err := Register(registry, map[string]http.HandlerFunc{
		"listDatabaseServers": func(http.ResponseWriter, *http.Request) {},
		"dropDatabaseGrant":   func(http.ResponseWriter, *http.Request) {},
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if _, ok := registry.authorized["GET /api/v1/databases/servers"]; !ok {
		t.Fatalf("servers not registered: %+v", registry.authorized)
	}
	if _, ok := registry.authorized["DELETE /api/v1/databases/grants/{id}"]; !ok {
		t.Fatalf("grant delete not registered: %+v", registry.authorized)
	}
}

func TestActorReturnsSharedUnauthenticatedError(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	_, apiErr := Actor(request)
	if apiErr == nil {
		t.Fatal("expected 401 for missing user")
	}
	if apiErr.Status != http.StatusUnauthorized || apiErr.Code != "authentication_required" {
		t.Fatalf("apiErr = %+v", apiErr)
	}
}

func TestFailMapsErrorsToStatus(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{"api error", httpapi.NewError(http.StatusConflict, "conflict", "nope"), http.StatusConflict, "conflict"},
		{"wrapped api error", errWrapped(), http.StatusConflict, "conflict"},
		{"no rows", sql.ErrNoRows, http.StatusNotFound, "not_found"},
		{"opaque", errors.New("boom"), http.StatusInternalServerError, "internal_error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			Fail(recorder, tc.err)
			if recorder.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", recorder.Code, tc.wantStatus)
			}
			var body struct{ Code string }
			if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode body: %v", err)
			}
			if body.Code != tc.wantCode {
				t.Fatalf("code = %q, want %q", body.Code, tc.wantCode)
			}
		})
	}
}

func errWrapped() error {
	return errors.Join(errors.New("context"), httpapi.NewError(http.StatusConflict, "conflict", "nope"))
}

// Guards that the spec actually parsed at init; a broken embed would make every
// other assertion meaningless.
func TestSpecParsed(t *testing.T) {
	ops, err := apispec.Operations()
	if err != nil {
		t.Fatalf("Operations: %v", err)
	}
	if len(ops) < 100 {
		t.Fatalf("only %d operations parsed; expected the full contract", len(ops))
	}
}
