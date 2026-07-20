package files

import (
	"errors"
	"net/http"

	"github.com/nexa-panel/nexa-panel/internal/modules/sites"
	"github.com/nexa-panel/nexa-panel/internal/platform/audit"
	"github.com/nexa-panel/nexa-panel/internal/platform/httpapi"
	"github.com/nexa-panel/nexa-panel/internal/platform/identity"
	filesoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/files"
)

// decodeJSON allows 4 MiB bodies: a file write carries up to 1 MiB of
// content inside the JSON envelope.
func decodeJSON(w http.ResponseWriter, r *http.Request, destination any) error {
	return httpapi.DecodeJSONLimit(w, r, destination, 4*1024*1024)
}

var (
	writeJSON  = httpapi.WriteJSON
	writeError = httpapi.WriteError
)

// writeOperatorError relays the operator's typed failure with its HTTP
// status; anything else is an agent transport problem and stays generic.
func writeOperatorError(w http.ResponseWriter, err error) {
	var operationErr *filesoperator.OperationError
	if errors.As(err, &operationErr) {
		writeJSON(w, filesoperator.StatusFor(operationErr.Code), operationErr)
		return
	}
	writeError(w, http.StatusBadGateway, "files_agent_unavailable", "The node agent could not complete the file operation.")
}

func (m *Module) recordAudit(r *http.Request, user identity.User, action string, site sites.Site, metadata map[string]any) {
	if metadata == nil {
		metadata = map[string]any{}
	}
	metadata["siteId"] = site.ID
	metadata["slug"] = site.Slug
	_ = m.audit.Record(r.Context(), audit.Entry{ActorUserID: &user.ID, Action: action, Subject: "site:" + site.ID, RemoteAddress: remoteAddress(r), Metadata: metadata})
}

func remoteAddress(r *http.Request) string {
	return httpapi.RemoteAddress(r)
}
