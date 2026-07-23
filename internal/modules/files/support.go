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

func (m *Module) recordAudit(w http.ResponseWriter, r *http.Request, user identity.User, action string, site sites.Site, metadata map[string]any) bool {
	if err := m.audit.RecordSensitive(r.Context(), m.auditEntry(r, user, action, site, metadata)); err != nil {
		writeError(w, http.StatusServiceUnavailable, "audit_unavailable", "The file change was refused because it could not be recorded in the audit log.")
		return false
	}
	return true
}

// recordAuditRead audits a read of site content — reading a file into the editor
// and downloading one — so file exfiltration leaves the same trail as a
// mutation. It is deliberately best-effort rather than fail-closed: reads carry
// no side effect to undo, they are issued continuously by the file manager, and
// refusing them while the audit table is briefly unavailable would break
// browsing without preventing anything. The loss is still logged at ERROR by the
// sink.
func (m *Module) recordAuditRead(r *http.Request, user identity.User, action string, site sites.Site, metadata map[string]any) {
	m.audit.RecordBestEffort(r.Context(), m.auditEntry(r, user, action, site, metadata))
}

func (m *Module) auditEntry(r *http.Request, user identity.User, action string, site sites.Site, metadata map[string]any) audit.Entry {
	if metadata == nil {
		metadata = map[string]any{}
	}
	metadata["siteId"] = site.ID
	metadata["slug"] = site.Slug
	metadata["result"] = "requested"
	return audit.Entry{ActorUserID: &user.ID, Action: action, Subject: "site:" + site.ID, RemoteAddress: remoteAddress(r), Metadata: metadata}
}

func remoteAddress(r *http.Request) string {
	return httpapi.RemoteAddress(r)
}
