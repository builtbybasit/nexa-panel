package agent

import (
	"net/http"

	firewalloperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/firewall"
)

func WithFirewallOperator(operator firewalloperator.Operator) Option {
	return func(server *Server) { server.firewall = operator }
}

func (s *Server) firewallStatusHTTP(w http.ResponseWriter, r *http.Request) {
	status, err := s.firewall.Discover(r.Context())
	if err != nil {
		writeError(w, http.StatusConflict, "firewall_operation_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) firewallApplyHTTP(w http.ResponseWriter, r *http.Request) {
	var change firewalloperator.Change
	if err := decodeJSON(w, r, &change); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	status, err := s.firewall.Apply(r.Context(), change)
	if err != nil {
		writeError(w, http.StatusConflict, "firewall_operation_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, status)
}
