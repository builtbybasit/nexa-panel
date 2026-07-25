package agent

import (
	"net/http"

	selfupdateoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/selfupdate"
)

// WithSelfUpdateOperator attaches the self-update operator to the agent. Only an
// agent built with this option serves the self-update routes.
func WithSelfUpdateOperator(operator selfupdateoperator.Operator) Option {
	return func(server *Server) { server.selfUpdate = operator }
}

// selfUpdateLatestHTTP reports the installed version and the newest release the
// node could move to. It mutates nothing, so the bearer token is the whole
// authorization story.
func (s *Server) selfUpdateLatestHTTP(w http.ResponseWriter, r *http.Request) {
	availability, err := s.selfUpdate.Latest(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "self_update_check_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, availability)
}

// selfUpdateTransactionHTTP reports the node's most recent update transaction.
// It is the route that makes a severed apply recoverable: activation restarts
// this very agent, so the apply response never arrives, and the client comes
// back here once the new agent is listening to read the committed outcome. It
// mutates nothing, so the bearer token is the whole authorization story.
func (s *Server) selfUpdateTransactionHTTP(w http.ResponseWriter, r *http.Request) {
	status, err := s.selfUpdate.Transaction(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "self_update_transaction_unavailable", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, status)
}

// selfUpdateApplyHTTP downloads, verifies, and transactionally activates the
// target release. Authorization is the bearer token plus the
// control panel's own system.update permission gate on the route that reaches
// here; the operator itself trusts no caller-supplied path, only a version
// string it re-validates.
func (s *Server) selfUpdateApplyHTTP(w http.ResponseWriter, r *http.Request) {
	var change selfupdateoperator.Change
	if err := decodeJSON(w, r, &change); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	result, err := s.selfUpdate.Apply(r.Context(), change)
	if err != nil {
		writeError(w, http.StatusConflict, "self_update_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}
