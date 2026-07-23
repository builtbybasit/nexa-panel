package webui

import (
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"

	"github.com/nexa-panel/nexa-panel/internal/platform/httpapi"
)

func newSPAHandler(assets fs.FS) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		requested := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if requested == "." || requested == "" {
			requested = "index.html"
		}

		// An unrouted API path must never fall through to the SPA. Serving
		// index.html with 200 makes a typo, a removed endpoint, and a version
		// mismatch all indistinguishable from success to anything parsing JSON.
		if requested == "api" || strings.HasPrefix(requested, "api/") {
			httpapi.WriteError(w, http.StatusNotFound, "not_found", "No such API endpoint.")
			return
		}

		content, err := fs.ReadFile(assets, requested)
		if err != nil {
			requested = "index.html"
			content, err = fs.ReadFile(assets, requested)
		}
		if err != nil {
			http.Error(w, "frontend is unavailable", http.StatusServiceUnavailable)
			return
		}

		if contentType := mime.TypeByExtension(path.Ext(requested)); contentType != "" {
			w.Header().Set("Content-Type", contentType)
		}
		if requested == "index.html" {
			w.Header().Set("Cache-Control", "no-cache")
		} else {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(content)))
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodGet {
			_, _ = w.Write(content)
		}
	})
}
