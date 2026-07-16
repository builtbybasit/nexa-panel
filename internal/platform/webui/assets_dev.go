//go:build !embed

package webui

import "net/http"

// Handler returns nil in development because Vite serves the Vue application.
func Handler() http.Handler {
	return nil
}
