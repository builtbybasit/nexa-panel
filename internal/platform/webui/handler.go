package webui

import (
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
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
