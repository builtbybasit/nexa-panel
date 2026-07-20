package identity

import (
	"net/http"

	"github.com/nexa-panel/nexa-panel/internal/platform/httpapi"
)

var (
	decodeJSON = httpapi.DecodeJSON
	writeJSON  = httpapi.WriteJSON
	writeError = httpapi.WriteError
)

func remoteAddress(r *http.Request) string {
	return httpapi.RemoteAddress(r)
}
