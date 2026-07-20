package admintools

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/nexa-panel/nexa-panel/internal/platform/secureid"
)

func (execRunner) Run(ctx context.Context, command Command) ([]byte, error) {
	return exec.CommandContext(ctx, command.Name, command.Args...).CombinedOutput()
}

func (o *HostOperator) prepareToolConfig(kind Kind) error {
	root := filepath.Join(o.configRoot, string(kind))
	if kind == PHPMyAdmin {
		if err := os.MkdirAll(filepath.Join(root, "sessions"), 0o750); err != nil {
			return err
		}
		config := "<?php\n$cfg['Servers'][1]['auth_type'] = 'signon';\n$cfg['Servers'][1]['SignonSession'] = 'SignonSession';\n$cfg['Servers'][1]['SignonURL'] = '/';\n$cfg['Servers'][1]['AllowNoPassword'] = false;\n"
		if err := secureWrite(filepath.Join(root, "config.user.inc.php"), []byte(config), 0o640); err != nil {
			return err
		}
		secretPath := filepath.Join(root, "config.secret.inc.php")
		if _, err := os.Stat(secretPath); errors.Is(err, os.ErrNotExist) {
			secretConfig := "<?php\n$cfg['blowfish_secret'] = '" + randomID() + randomID() + "';\n"
			if err := secureWrite(secretPath, []byte(secretConfig), 0o640); err != nil {
				return err
			}
		}
		return o.ownForRuntime(root, kind)
	}
	if err := os.MkdirAll(filepath.Join(root, "data"), 0o750); err != nil {
		return err
	}
	files := []struct {
		name, value string
		mode        os.FileMode
	}{{"bootstrap-password", randomID() + randomID(), 0o600}, {"pgpass", "", 0o600}, {"servers.json", "{\"Servers\":{}}\n", 0o640}}
	for _, file := range files {
		path := filepath.Join(root, file.name)
		if _, err := os.Stat(path); err == nil {
			continue
		}
		if err := secureWrite(path, []byte(file.value), file.mode); err != nil {
			return err
		}
	}
	// The stopped/deployed configuration must not trust a deterministic header:
	// any local process can reach the loopback-published pgAdmin port. Install an
	// unexposed random capability and keep auto-creation disabled until launch.
	config, err := pgAdminConfig(randomID()+randomID(), false)
	if err != nil {
		return err
	}
	if err := secureWrite(filepath.Join(root, "config_local.py"), []byte(config), 0o640); err != nil {
		return err
	}
	if err := secureWrite(filepath.Join(root, "config_distro.py"), []byte("LOG_FILE = '/dev/null'\n"), 0o640); err != nil {
		return err
	}
	return o.ownForRuntime(root, kind)
}

// runtimeUID is the uid/gid the tool's container process runs as. Its config
// files are mounted read-only into the container, so they must be owned by this
// account — root-owned 0600/0640 files are unreadable to it and the tool exits
// on startup.
func runtimeUID(kind Kind) int {
	if kind == PGAdmin {
		return 5050 // pgAdmin's "pgadmin" service account
	}
	return 33 // phpMyAdmin serves as Apache's www-data
}

// ownForRuntime hands the tool's config tree to the container's service account
// so the read-only mounts are readable. A no-op off root (unit tests), where the
// agent does not run and the files are only asserted, never mounted.
func (o *HostOperator) ownForRuntime(root string, kind Kind) error {
	if os.Geteuid() != 0 {
		return nil
	}
	id := runtimeUID(kind)
	if err := setRuntimeOwnership(root, 0, id, 0o750); err != nil {
		return err
	}
	if kind == PHPMyAdmin {
		if err := setRuntimeOwnership(filepath.Join(root, "sessions"), id, id, 0o750); err != nil {
			return err
		}
		for _, name := range []string{"config.user.inc.php", "config.secret.inc.php"} {
			if err := setRuntimeOwnership(filepath.Join(root, name), 0, id, 0o640); err != nil {
				return err
			}
		}
		return nil
	}
	if err := setRuntimeOwnership(filepath.Join(root, "data"), id, id, 0o700); err != nil {
		return err
	}
	if err := setRuntimeOwnership(filepath.Join(root, "bootstrap-password"), id, id, 0o600); err != nil {
		return err
	}
	if err := setRuntimeOwnership(filepath.Join(root, "pgpass"), id, id, 0o600); err != nil {
		return err
	}
	for _, name := range []string{"servers.json", "config_local.py", "config_distro.py"} {
		if err := setRuntimeOwnership(filepath.Join(root, name), 0, id, 0o640); err != nil {
			return err
		}
	}
	return nil
}

func setRuntimeOwnership(path string, uid, gid int, mode os.FileMode) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("admin tool path %s must not be a symlink", path)
	}
	if err := os.Chmod(path, mode); err != nil {
		return err
	}
	return os.Lchown(path, uid, gid)
}

func (o *HostOperator) bootstrapLaunch(ctx context.Context, change Change, secret string) (Observation, error) {
	root := filepath.Join(o.configRoot, string(change.Tool.Kind))
	if err := os.MkdirAll(root, 0o750); err != nil {
		return Observation{}, err
	}
	if change.Tool.Kind == PHPMyAdmin {
		path := filepath.Join(root, "sessions", "sess_"+change.Launch.SessionID)
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			return Observation{}, err
		}
		content := phpSession(map[string]string{
			"PMA_single_signon_user":     change.Launch.Username,
			"PMA_single_signon_password": secret,
			"PMA_single_signon_host":     change.Launch.DatabaseHost,
			"PMA_single_signon_port":     strconv.Itoa(change.Launch.DatabasePort),
			"PMA_single_signon_only_db":  change.Launch.Database,
		})
		if err := secureWrite(path, []byte(content), 0o640); err != nil {
			return Observation{}, fmt.Errorf("write phpMyAdmin signon session: %w", err)
		}
		if os.Geteuid() == 0 {
			// The session directory is writable by the container. Lchown cannot
			// follow a path swapped for a symlink during launch bootstrap.
			if err := os.Lchown(path, 33, 33); err != nil {
				return Observation{}, fmt.Errorf("secure phpMyAdmin signon session ownership: %w", err)
			}
			info, err := os.Lstat(path)
			if err != nil || !info.Mode().IsRegular() {
				return Observation{}, fmt.Errorf("verify phpMyAdmin signon session: %w", firstError(err, errors.New("session is not a regular file")))
			}
		}
		return Observation{Tool: change.Tool, Verified: true, UpstreamCookieName: "SignonSession", UpstreamCookieValue: change.Launch.SessionID}, nil
	}
	pgpass := fmt.Sprintf("%s:%d:%s:%s:%s\n", pgpassEscape(change.Launch.DatabaseHost), change.Launch.DatabasePort, pgpassEscape(change.Launch.Database), pgpassEscape(change.Launch.Username), pgpassEscape(secret))
	pgpassPath := filepath.Join(root, "pgpass")
	if err := secureWrite(pgpassPath, []byte(pgpass), 0o600); err != nil {
		return Observation{}, fmt.Errorf("write pgAdmin passfile: %w", err)
	}
	if os.Geteuid() == 0 {
		if err := setRuntimeOwnership(pgpassPath, 5050, 5050, 0o600); err != nil {
			return Observation{}, fmt.Errorf("secure pgAdmin passfile ownership: %w", err)
		}
	}
	servers := map[string]any{"Servers": map[string]any{"1": map[string]any{"Name": change.Launch.Database, "Group": "Nexa Panel", "Host": change.Launch.DatabaseHost, "Port": change.Launch.DatabasePort, "MaintenanceDB": change.Launch.Database, "Username": change.Launch.Username, "SSLMode": "prefer", "Shared": true}}}
	encoded, _ := json.MarshalIndent(servers, "", "  ")
	serversPath := filepath.Join(root, "servers.json")
	if err := secureWrite(serversPath, append(encoded, '\n'), 0o640); err != nil {
		return Observation{}, fmt.Errorf("write pgAdmin server catalog: %w", err)
	}
	if os.Geteuid() == 0 {
		if err := setRuntimeOwnership(serversPath, 0, 5050, 0o640); err != nil {
			return Observation{}, fmt.Errorf("secure pgAdmin server catalog ownership: %w", err)
		}
	}
	config, err := pgAdminConfig(change.Launch.SessionID, true)
	if err != nil {
		return Observation{}, err
	}
	configPath := filepath.Join(root, "config_local.py")
	if err := secureWrite(configPath, []byte(config), 0o640); err != nil {
		return Observation{}, fmt.Errorf("write pgAdmin session trust configuration: %w", err)
	}
	if os.Geteuid() == 0 {
		if err := setRuntimeOwnership(configPath, 0, 5050, 0o640); err != nil {
			return Observation{}, fmt.Errorf("secure pgAdmin session trust configuration: %w", err)
		}
	}
	if output, err := o.runner.Run(ctx, Command{Name: "systemctl", Args: []string{"restart", change.Tool.SystemdUnit}}); err != nil {
		return Observation{}, commandError("restart pgAdmin with scoped server catalog", output, err)
	}
	if output, err := o.runner.Run(ctx, Command{Name: "systemctl", Args: []string{"is-active", change.Tool.SystemdUnit}}); err != nil || strings.TrimSpace(string(output)) != "active" {
		return Observation{}, commandError("verify pgAdmin launch", output, firstError(err, errors.New("service is not active")))
	}
	return Observation{Tool: change.Tool, Verified: true}, nil
}

func phpSession(values map[string]string) string {
	order := []string{"PMA_single_signon_user", "PMA_single_signon_password", "PMA_single_signon_host", "PMA_single_signon_port", "PMA_single_signon_only_db"}
	var builder strings.Builder
	for _, key := range order {
		value := values[key]
		builder.WriteString(key)
		builder.WriteString("|s:")
		builder.WriteString(strconv.Itoa(len(value)))
		builder.WriteString(":\"")
		builder.WriteString(value)
		builder.WriteString("\";")
	}
	return builder.String()
}

func pgpassEscape(value string) string {
	return strings.NewReplacer("\\", "\\\\", ":", "\\:").Replace(value)
}

func secureWrite(path string, value []byte, mode os.FileMode) error {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	keep := false
	defer func() {
		_ = temporary.Close()
		if !keep {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(mode); err != nil {
		return err
	}
	if _, err := io.Copy(temporary, bytes.NewReader(value)); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	keep = true
	if handle, openErr := os.Open(directory); openErr == nil {
		_ = handle.Sync()
		_ = handle.Close()
	}
	return nil
}

const PGAdminSessionHeaderPrefix = "X-Nexa-PgAdmin-"

// PGAdminSessionHeader derives a capability header name from the existing
// server-side launch token. The token itself is never forwarded or logged, and
// a loopback caller cannot invent a different header because pgAdmin trusts
// only this exact per-launch name.
func PGAdminSessionHeader(sessionID string) (string, error) {
	if !validToken(sessionID) {
		return "", errors.New("pgAdmin session token is invalid")
	}
	digest := sha256.Sum256([]byte(sessionID))
	return PGAdminSessionHeaderPrefix + hex.EncodeToString(digest[:]), nil
}

func pgAdminRemoteUserVariable(sessionID string) (string, error) {
	header, err := PGAdminSessionHeader(sessionID)
	if err != nil {
		return "", err
	}
	return "HTTP_" + strings.ToUpper(strings.ReplaceAll(header, "-", "_")), nil
}

func pgAdminConfig(sessionID string, autoCreate bool) (string, error) {
	remoteUser, err := pgAdminRemoteUserVariable(sessionID)
	if err != nil {
		return "", err
	}
	autoCreateValue := "False"
	if autoCreate {
		autoCreateValue = "True"
	}
	return "AUTHENTICATION_SOURCES = ['webserver']\n" +
		"WEBSERVER_AUTO_CREATE_USER = " + autoCreateValue + "\n" +
		"WEBSERVER_REMOTE_USER = '" + remoteUser + "'\n" +
		"MASTER_PASSWORD_REQUIRED = False\n" +
		"ALLOW_SAVE_PASSWORD = False\n" +
		"ALLOW_SAVE_TUNNEL_PASSWORD = False\n" +
		"CHECK_EMAIL_DELIVERABILITY = False\n" +
		"ENHANCED_COOKIE_PROTECTION = True\n", nil
}

func validToken(value string) bool {
	if len(value) < 16 || len(value) > 64 {
		return false
	}
	for _, character := range value {
		if !((character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || character == '-' || character == '_') {
			return false
		}
	}
	return true
}

func renderQuadlet(tool Tool, configRoot string) string {
	config := filepath.Join(configRoot, string(tool.Kind))
	containerPort := 80
	if tool.Kind == PGAdmin {
		containerPort = 5050
	}
	// The memory ceiling goes through PodmanArgs, not a Memory= key: Quadlet only
	// gained a native Memory= key in podman 5.0, and passing an unsupported key
	// makes the generator refuse the whole unit (the .service is never produced,
	// so `systemctl start` fails with "Unit not found"). --memory has been valid
	// since podman 4.5, covering the 4.x hosts this runs on. PidsLimit= is fine.
	lines := []string{"[Unit]", "Description=Nexa " + string(tool.Kind), "After=network-online.target", "", "[Container]", "Image=" + tool.Image, "ContainerName=" + tool.ContainerName, "PublishPort=127.0.0.1:" + strconv.Itoa(tool.Port) + ":" + strconv.Itoa(containerPort), "PodmanArgs=--memory=" + strconv.Itoa(tool.MemoryMB) + "m", "PidsLimit=" + strconv.Itoa(tool.PIDsLimit), "ReadOnly=true", "NoNewPrivileges=true", "DropCapability=ALL", "Volume=" + config + ":/nexa-config:ro,Z"}
	if tool.Kind == PHPMyAdmin {
		// The image's Apache binds port 80 as root during startup, which needs
		// CAP_NET_BIND_SERVICE — a privileged (<1024) port. DropCapability=ALL
		// above strips it, so Apache fails with "make_sock: could not bind to
		// address 0.0.0.0:80 (Permission denied)" and the container crash-loops
		// until systemd gives up. Grant back only this one capability; pgAdmin
		// listens on 5050 and needs nothing extra. (Docker's default
		// net.ipv4.ip_unprivileged_port_start=0 hides this in the container test
		// harness; podman here does not relax the privileged-port range.)
		lines = append(lines, "AddCapability=NET_BIND_SERVICE", "Environment=PMA_HOST=host.containers.internal", "Volume="+config+"/sessions:/sessions:Z,U", "Volume="+config+"/config.user.inc.php:/etc/phpmyadmin/config.user.inc.php:ro,Z", "Volume="+config+"/config.secret.inc.php:/etc/phpmyadmin/config.secret.inc.php:ro,Z", "Tmpfs=/tmp:rw,noexec,nosuid,size=32m", "Tmpfs=/var/run/apache2:rw,noexec,nosuid,size=4m", "Tmpfs=/var/lock/apache2:rw,noexec,nosuid,size=4m",
			// notmpcopyup keeps the log tmpfs empty. The image ships
			// /var/log/apache2/{access,error}.log as symlinks to /dev/stdout and
			// /dev/stderr; podman's default tmpcopyup replicates them into the
			// mount, and Apache — detached, journald-logged, capability-stripped —
			// cannot reopen /dev/stderr, dying with "could not open error log file".
			// An empty tmpfs lets Apache create real log files instead.
			"Tmpfs=/var/log/apache2:rw,noexec,nosuid,size=16m,notmpcopyup")
	}
	if tool.Kind == PGAdmin {
		lines = append(lines, "Environment=PGADMIN_LISTEN_PORT=5050", "Environment=PGADMIN_DISABLE_POSTFIX=1", "Environment=PGADMIN_CUSTOM_CONFIG_DISTRO_FILE=/nexa-config/config_distro.py", "Environment=PGADMIN_REPLACE_SERVERS_ON_STARTUP=True", "Environment=PGPASS_FILE=/nexa-config/pgpass", "Environment=PGADMIN_DEFAULT_EMAIL=bootstrap@nexa.example.com", "Environment=PGADMIN_DEFAULT_PASSWORD_FILE=/nexa-config/bootstrap-password", "Volume="+config+"/data:/var/lib/pgadmin:Z,U", "Volume="+config+"/config_local.py:/pgadmin4/config_local.py:ro,Z", "Volume="+config+"/servers.json:/pgadmin4/servers.json:ro,Z", "Volume="+config+"/pgpass:/nexa-config/pgpass:ro,Z", "Tmpfs=/tmp:rw,noexec,nosuid,size=32m")
	}
	lines = append(lines, "", "[Service]", "Restart=on-failure", "TimeoutStartSec=180", "", "[Install]", "WantedBy=multi-user.target", "")
	return strings.Join(lines, "\n")
}

func mergeRendered(tool Tool, encoded string) Tool {
	if strings.Contains(encoded, "Image=") {
		for _, line := range strings.Split(encoded, "\n") {
			if strings.HasPrefix(line, "Image=") {
				tool.Image = strings.TrimPrefix(line, "Image=")
			}
		}
	}
	return tool
}

func (o *HostOperator) quadletPath(kind Kind) string {
	return filepath.Join(o.quadletRoot, "nexa-"+string(kind)+".container")
}

func fingerprint(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func randomID() string { return secureid.Hex(16) }

// mentionsMissingUnit reports whether systemctl's output is the "Unit … not
// found" message it prints when a service does not exist — the signature of a
// Quadlet that was never generated into a unit.
func mentionsMissingUnit(output []byte) bool {
	return strings.Contains(strings.ToLower(string(output)), "not found")
}

func commandError(action string, output []byte, err error) error {
	message := strings.TrimSpace(string(output))
	if len(message) > 500 {
		message = message[:500]
	}
	if message == "" {
		return fmt.Errorf("%s: %w", action, err)
	}
	return fmt.Errorf("%s: %w: %s", action, err, message)
}

func firstError(values ...error) error {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return errors.New("operation failed")
}
