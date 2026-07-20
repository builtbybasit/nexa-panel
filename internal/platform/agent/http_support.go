package agent

import (
	"github.com/nexa-panel/nexa-panel/internal/platform/httpapi"
)

var (
	decodeJSON = httpapi.DecodeJSON
	writeJSON  = httpapi.WriteJSON
	writeError = httpapi.WriteError
)
