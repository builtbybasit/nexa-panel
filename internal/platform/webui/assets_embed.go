//go:build embed

package webui

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed dist
var embedded embed.FS

// Handler serves the compiled Vue application from the Nexa binary.
func Handler() http.Handler {
	dist, err := fs.Sub(embedded, "dist")
	if err != nil {
		panic("open embedded frontend: " + err.Error())
	}
	return newSPAHandler(dist)
}
