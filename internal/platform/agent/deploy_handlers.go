package agent

import (
	"errors"
	"net/http"

	"github.com/nexa-panel/nexa-panel/internal/platform/httpapi"
	deployoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/deploy"
)

func WithDeployOperator(operator deployoperator.Operator) Option {
	return func(server *Server) { server.deploy = operator }
}

func (s *Server) deploySSHApplyHTTP(w http.ResponseWriter, r *http.Request) {
	var request deployoperator.SSHAccessRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	observation, err := s.deploy.ApplySSHAccess(r.Context(), request)
	// The control panel refuses this pairing before it ever calls the node, so
	// reaching here means the two enables raced; the node's answer is the
	// authoritative one and the code carries the reason back intact.
	if errors.Is(err, deployoperator.ErrSFTPJailPresent) {
		writeError(w, http.StatusConflict, "sftp_jail_present", err.Error())
		return
	}
	if err != nil {
		writeError(w, http.StatusConflict, "deploy_operation_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, observation)
}

// deploySSHGenerateKeyHTTP returns both halves of a fresh key pair. The private
// half is in this response and nowhere else — the node keeps no copy — so the
// response body must never be logged.
func (s *Server) deploySSHGenerateKeyHTTP(w http.ResponseWriter, r *http.Request) {
	var request deployoperator.SSHAccessRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	generated, err := s.deploy.GenerateUserKey(r.Context(), request)
	if err != nil {
		writeError(w, http.StatusConflict, "deploy_operation_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, generated)
}

// deployKeyEnsureHTTP returns the public half of the site's deploy key. The
// private half is not in this response and there is no endpoint that could
// return it: it is written under the site root and never read back.
func (s *Server) deployKeyEnsureHTTP(w http.ResponseWriter, r *http.Request) {
	var request deployoperator.DeployKeyRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	observation, err := s.deploy.EnsureDeployKey(r.Context(), request)
	if err != nil {
		writeError(w, http.StatusConflict, "deploy_operation_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, observation)
}

// deployEnvReadHTTP returns the site's shared .env. The body carries a secret,
// so like the generated key pair it must never be logged; the request itself
// carries only the site identity.
func (s *Server) deployEnvReadHTTP(w http.ResponseWriter, r *http.Request) {
	var request deployoperator.EnvRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	document, err := s.deploy.ReadSharedEnv(r.Context(), request)
	if err != nil {
		writeEnvFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, document)
}

// deployEnvWriteHTTP replaces the shared .env. The document is bounded by the
// operator itself, which is what keeps a large body from reaching the node's
// disk however this handler is called.
func (s *Server) deployEnvWriteHTTP(w http.ResponseWriter, r *http.Request) {
	var request struct {
		deployoperator.EnvRequest
		Content string `json:"content"`
	}
	if err := decodeSharedEnvJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	document, err := s.deploy.WriteSharedEnv(r.Context(), request.EnvRequest, request.Content)
	if err != nil {
		writeEnvFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, document)
}

// decodeSharedEnvJSON leaves room for the identity fields and for JSON escaping
// on top of a document at the operator's own cap, so a legitimate
// maximum-size document is refused by the operator's message rather than by
// the reader. Everything else on this surface keeps the 16 KiB default.
func decodeSharedEnvJSON(w http.ResponseWriter, r *http.Request, destination any) error {
	return httpapi.DecodeJSONLimit(w, r, destination, int64(4*deployoperator.MaxSharedEnvBytes+4096))
}

func writeEnvFailure(w http.ResponseWriter, err error) {
	// A site that is not in deployer mode has no release tree at all; the code
	// carries that back so the panel can say so instead of reporting a failure.
	if errors.Is(err, deployoperator.ErrSharedEnvMissing) {
		writeError(w, http.StatusConflict, "shared_env_missing", err.Error())
		return
	}
	writeError(w, http.StatusConflict, "deploy_operation_failed", err.Error())
}

// deployPrepareHTTP verifies the node's deployment tooling and installs what is
// missing. It can run for tens of minutes on a cold apt index, which is why the
// control panel calls it on its own long-timeout client; a tool that could not
// be installed comes back as a warning in the observation, not as an error.
func (s *Server) deployPrepareHTTP(w http.ResponseWriter, r *http.Request) {
	var request deployoperator.PrepareRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	observation, err := s.deploy.Prepare(r.Context(), request)
	if err != nil {
		writeError(w, http.StatusConflict, "deploy_operation_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, observation)
}

// deployFPMReloadApplyHTTP grants or withdraws the site's narrow permission to
// reload its own PHP-FPM master: one root-owned argument-less wrapper and one
// sudoers rule naming exactly that path. Nothing in the request reaches either
// file's content except through the operator's identity and branch validation,
// so a request from a compromised caller cannot widen the grant.
func (s *Server) deployFPMReloadApplyHTTP(w http.ResponseWriter, r *http.Request) {
	var request deployoperator.FPMReloadRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	observation, err := s.deploy.ApplyFPMReload(r.Context(), request)
	if err != nil {
		writeError(w, http.StatusConflict, "deploy_operation_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, observation)
}

// deployGitHubTestHTTP reports the verdict of the two probes. A probe that ran
// and failed is a 200 with authOk false, not an error: the panel shows the
// tail. Only a probe that could not be started at all is a failure here.
func (s *Server) deployGitHubTestHTTP(w http.ResponseWriter, r *http.Request) {
	var request deployoperator.GitHubTestRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	result, err := s.deploy.TestGitHub(r.Context(), request)
	if err != nil {
		writeError(w, http.StatusConflict, "deploy_operation_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}
