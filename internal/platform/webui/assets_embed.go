//go:build embed

package webui

import (
	"embed"
	"io/fs"
	"net/http"
)

// all: is required — a bare `//go:embed dist` silently omits files whose names
// begin with "_" or ".", and Vite emits shared chunks like
// _plugin-vue_export-helper-*.js. Without those, the SPA handler falls back to
// index.html for the missing chunk, and the browser rejects the text/html
// response for a module script ("Expected a JavaScript-or-Wasm module script").
//
//go:embed all:dist
var embedded embed.FS

// Handler serves the compiled Vue application from the Nexa binary.
func Handler() http.Handler {
	dist, err := fs.Sub(embedded, "dist")
	if err != nil {
		panic("open embedded frontend: " + err.Error())
	}
	return newSPAHandler(dist)
}
