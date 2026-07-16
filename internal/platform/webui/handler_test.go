package webui

import (
	"net/http"
	"net/http/httptest"
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
