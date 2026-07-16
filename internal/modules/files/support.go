package files

import (
	"encoding/json"
	"errors"

	"io"
	"net"
	"net/http"

	"github.com/nexa-panel/nexa-panel/internal/modules/sites"

	"github.com/nexa-panel/nexa-panel/internal/platform/audit"
	"github.com/nexa-panel/nexa-panel/internal/platform/identity"

	filesoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/files"
)

// decodeJSON allows 4 MiB bodies: a file write carries up to 1 MiB of
// content inside the JSON envelope.
func decodeJSON(w http.ResponseWriter, r *http.Request, destination any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 4*1024*1024)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return errors.New("Request body must be valid JSON.")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("Request body must contain one JSON object.")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]string{"code": code, "message": message})
}

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
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}
