package logs

import (
	"errors"
	"net/http"

	"github.com/nexa-panel/nexa-panel/internal/platform/httpapi"
	logsoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/logs"
)

var (
	writeJSON  = httpapi.WriteJSON
	writeError = httpapi.WriteError
)

// writeOperatorError relays the operator's typed failure with its HTTP
// status; anything else is an agent transport problem and stays generic.
func writeOperatorError(w http.ResponseWriter, err error) {
	var operationErr *logsoperator.OperationError
	if errors.As(err, &operationErr) {
		writeJSON(w, logsoperator.StatusFor(operationErr.Code), operationErr)
		return
	}
	writeError(w, http.StatusBadGateway, "logs_agent_unavailable", "The node agent could not complete the log operation.")
}
