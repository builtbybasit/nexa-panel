package agent

import (
	"net/http"

	"github.com/nexa-panel/nexa-panel/internal/platform/httpapi"
	backupoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/backups"
)

// WithBackupOperator registers the backups operator so the agent exposes the
// /v1/backups/* routes.
func WithBackupOperator(operator backupoperator.Operator) Option {
	return func(server *Server) { server.backups = operator }
}

func (s *Server) backupTestAccountHTTP(w http.ResponseWriter, r *http.Request) {
	var account backupoperator.Account
	if err := decodeJSON(w, r, &account); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	result, err := s.backups.TestAccount(r.Context(), account)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "backup_test_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) backupRunHTTP(w http.ResponseWriter, r *http.Request) {
	var request backupoperator.RunRequest
	if err := decodeBackupRunJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if err := backupoperator.ValidateRunRequest(request); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid_request", err.Error())
		return
	}
	manifest, err := s.backups.Run(r.Context(), request)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "backup_run_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, manifest)
}

func (s *Server) backupRunSystemHTTP(w http.ResponseWriter, r *http.Request) {
	var request backupoperator.SystemRunRequest
	if err := decodeBackupRunJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if err := backupoperator.ValidateSystemRunRequest(request); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid_request", err.Error())
		return
	}
	manifest, err := s.backups.RunSystem(r.Context(), request)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "backup_system_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, manifest)
}

func (s *Server) backupRestoreHTTP(w http.ResponseWriter, r *http.Request) {
	var request backupoperator.RestoreRequest
	if err := decodeBackupRunJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if err := backupoperator.ValidateRestoreRequest(request); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid_request", err.Error())
		return
	}
	if err := s.backups.Restore(r.Context(), request); err != nil {
		writeError(w, http.StatusInternalServerError, "backup_restore_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "restored"})
}

func (s *Server) backupDeleteCopyHTTP(w http.ResponseWriter, r *http.Request) {
	var request backupoperator.DeleteRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if err := backupoperator.ValidateDeleteRequest(request); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid_request", err.Error())
		return
	}
	if err := s.backups.DeleteCopy(r.Context(), request); err != nil {
		writeError(w, http.StatusInternalServerError, "backup_delete_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (s *Server) backupInstallScheduleHTTP(w http.ResponseWriter, r *http.Request) {
	var spec backupoperator.ScheduleSpec
	if err := decodeJSON(w, r, &spec); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if err := s.backups.InstallSchedule(r.Context(), spec); err != nil {
		writeError(w, http.StatusInternalServerError, "backup_schedule_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "installed"})
}

func (s *Server) backupRemoveScheduleHTTP(w http.ResponseWriter, r *http.Request) {
	var request struct {
		PlanID string `json:"planId"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if err := s.backups.RemoveSchedule(r.Context(), request.PlanID); err != nil {
		writeError(w, http.StatusInternalServerError, "backup_schedule_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "removed"})
}

// decodeBackupRunJSON permits a larger body than the default: a run request
// carries the account's params plus every resolved site and database target.
func decodeBackupRunJSON(w http.ResponseWriter, r *http.Request, destination any) error {
	return httpapi.DecodeJSONLimit(w, r, destination, 256*1024)
}
