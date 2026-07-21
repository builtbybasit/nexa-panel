package agent

import (
	"net/http"

	sftpoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/sftp"
)

func WithSFTPOperator(operator sftpoperator.Operator) Option {
	return func(server *Server) { server.sftp = operator }
}

func (s *Server) sftpApplyHTTP(w http.ResponseWriter, r *http.Request) {
	var request sftpoperator.Request
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	observation, err := s.sftp.Apply(r.Context(), request)
	if err != nil {
		writeError(w, http.StatusConflict, "sftp_operation_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, observation)
}
