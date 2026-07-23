package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestSPAHandlerServesAsset(t *testing.T) {
	handler := newSPAHandler(fstest.MapFS{
		"index.html":    &fstest.MapFile{Data: []byte("index")},
		"assets/app.js": &fstest.MapFile{Data: []byte("app")},
	})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/assets/app.js", nil))

	if response.Code != http.StatusOK || response.Body.String() != "app" {
		t.Fatalf("asset response = %d %q", response.Code, response.Body.String())
	}
	if got := response.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Fatalf("cache control = %q", got)
	}
}

func TestSPAHandlerFallsBackToIndex(t *testing.T) {
	handler := newSPAHandler(fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("index")},
	})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/system", nil))

	if response.Code != http.StatusOK || response.Body.String() != "index" {
		t.Fatalf("fallback response = %d %q", response.Code, response.Body.String())
	}
}

func TestSPAHandlerMissingIndex(t *testing.T) {
	handler := newSPAHandler(fstest.MapFS{})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusServiceUnavailable)
	}
}

// An unrouted API path must return JSON 404 rather than the SPA shell. Serving
// index.html with 200 made every typo and removed endpoint look like success to
// a JSON client, which is how it went unnoticed until a live node was probed.
func TestUnroutedAPIPathReturnsJSONNotFoundInsteadOfTheSPA(t *testing.T) {
	handler := newSPAHandler(fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("index")},
	})
	for _, target := range []string{"/api", "/api/v1/definitely-not-a-route", "/api/v1/sites/nope/nope"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, target, nil))
		if response.Code != http.StatusNotFound {
			t.Errorf("%s = %d, want 404", target, response.Code)
		}
		if strings.Contains(response.Body.String(), "index") {
			t.Errorf("%s served the SPA shell: %q", target, response.Body.String())
		}
	}

	// A normal client route must still receive the SPA so deep links work.
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/sites/new", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "index") {
		t.Fatalf("SPA deep link = %d %q", response.Code, response.Body.String())
	}
}
