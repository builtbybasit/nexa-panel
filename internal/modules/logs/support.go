package logs

import (
	"encoding/json"
	"errors"

	"net/http"

	logsoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/logs"
)

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
	var operationErr *logsoperator.OperationError
	if errors.As(err, &operationErr) {
		writeJSON(w, logsoperator.StatusFor(operationErr.Code), operationErr)
		return
	}
	writeError(w, http.StatusBadGateway, "logs_agent_unavailable", "The node agent could not complete the log operation.")
}
