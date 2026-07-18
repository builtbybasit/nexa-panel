package backups

import (
	"errors"
	"path"
	"strings"
)

// remoteName is the fixed rclone remote alias every invocation defines through
// the environment. Because config is per-process env, the name can be constant.
const remoteName = "nexa"

// rcloneRemote turns an account into (remote reference, environment) for an
// rclone invocation. It defines the remote entirely through RCLONE_CONFIG_NEXA_*
// variables so nothing is written to a config file or exposed on the command
// line. The returned reference is "nexa:<path>".
//
// Local remotes are addressed by absolute filesystem path; every other backend
// treats Path as a sub-path under the remote root (e.g. "bucket/dir" for S3).
func rcloneRemote(account Account) (string, []string, error) {
	backend := strings.TrimSpace(account.Type)
	if backend == "" {
		return "", nil, errors.New("backup account type is required")
	}
	env := []string{"RCLONE_CONFIG_" + strings.ToUpper(remoteName) + "_TYPE=" + backend}
	for key, value := range account.Params {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		env = append(env, "RCLONE_CONFIG_"+strings.ToUpper(remoteName)+"_"+strings.ToUpper(key)+"="+value)
	}

	reference := remoteName + ":"
	target := strings.TrimSpace(account.Path)
	switch backend {
	case TypeLocal:
		if !strings.HasPrefix(target, "/") {
			return "", nil, errors.New("local backup path must be absolute")
		}
		reference += path.Clean(target)
	default:
		reference += strings.TrimPrefix(path.Clean("/"+target), "/")
	}
	return reference, env, nil
}

// sanitize bounds an rclone error message so a failed connection report stays
// small and never dumps an unbounded backend stack trace to the UI.
func sanitize(message string) string {
	message = strings.TrimSpace(message)
	message = strings.ReplaceAll(message, "\n", " ")
	if len(message) > 300 {
		message = message[:300]
	}
	return message
}
