//go:build embed

package controlplane

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEmbeddedFrontendServesVueRoutes(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server, err := New("test", nil, logger)
	if err != nil {
		t.Fatalf("New returned an error: %v", err)
	}

	for _, route := range []string{"/", "/system", "/unknown/vue/route"} {
		t.Run(route, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, route, nil)
			response := httptest.NewRecorder()
			server.Handler().ServeHTTP(response, request)

			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", response.Code)
			}
			if !strings.Contains(response.Body.String(), `<div id="app"></div>`) {
				t.Fatalf("route %q did not serve the Vue application", route)
			}
			if got := response.Header().Get("Cache-Control"); got != "no-cache" {
				t.Fatalf("Cache-Control = %q, want no-cache", got)
			}
		})
	}
}
