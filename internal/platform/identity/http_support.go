package identity

import (
	"net/http"

	"github.com/nexa-panel/nexa-panel/internal/platform/httpapi"
)

func remoteAddress(r *http.Request) string {
	return httpapi.RemoteAddress(r)
}
