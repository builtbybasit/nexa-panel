package admintools

import (
	"os"

	"path/filepath"

	"context"
	"crypto/rand"
	"crypto/sha256"
	"strings"

	"errors"

	"os/exec"

	"encoding/hex"
	"encoding/json"
	"strconv"

	"fmt"
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
			return secureWrite(secretPath, []byte(secretConfig), 0o640)
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Join(root, "data"), 0o750); err != nil {
		return err
	}
	files := []struct {
		name, value string
		mode        os.FileMode
	}{{"bootstrap-password", randomID() + randomID(), 0o600}, {"pgpass", "", 0o600}, {"servers.json", "{\"Servers\":{}}\n", 0o640}, {"config_local.py", "AUTHENTICATION_SOURCES = ['webserver']\nWEBSERVER_AUTO_CREATE_USER = True\nWEBSERVER_REMOTE_USER = 'HTTP_X_FORWARDED_USER'\nMASTER_PASSWORD_REQUIRED = False\n", 0o640}, {"config_distro.py", "LOG_FILE = '/dev/null'\n", 0o640}}
	for _, file := range files {
		path := filepath.Join(root, file.name)
		if _, err := os.Stat(path); err == nil {
			continue
		}
		if err := secureWrite(path, []byte(file.value), file.mode); err != nil {
			return err
		}
	}
	return nil
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
			_ = os.Chown(path, 33, 33)
		}
		return Observation{Tool: change.Tool, Verified: true, UpstreamCookieName: "SignonSession", UpstreamCookieValue: change.Launch.SessionID}, nil
	}
	pgpass := fmt.Sprintf("%s:%d:%s:%s:%s\n", pgpassEscape(change.Launch.DatabaseHost), change.Launch.DatabasePort, pgpassEscape(change.Launch.Database), pgpassEscape(change.Launch.Username), pgpassEscape(secret))
	if err := secureWrite(filepath.Join(root, "pgpass"), []byte(pgpass), 0o600); err != nil {
		return Observation{}, fmt.Errorf("write pgAdmin passfile: %w", err)
	}
	if os.Geteuid() == 0 {
		_ = os.Chown(filepath.Join(root, "pgpass"), 5050, 5050)
	}
	servers := map[string]any{"Servers": map[string]any{"1": map[string]any{"Name": change.Launch.Database, "Group": "Nexa Panel", "Host": change.Launch.DatabaseHost, "Port": change.Launch.DatabasePort, "MaintenanceDB": change.Launch.Database, "Username": change.Launch.Username, "SSLMode": "prefer", "Shared": true}}}
	encoded, _ := json.MarshalIndent(servers, "", "  ")
	if err := secureWrite(filepath.Join(root, "servers.json"), append(encoded, '\n'), 0o640); err != nil {
		return Observation{}, fmt.Errorf("write pgAdmin server catalog: %w", err)
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
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, value, mode); err != nil {
		return err
	}
	if err := os.Chmod(temporary, mode); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return nil
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
	lines := []string{"[Unit]", "Description=Nexa " + string(tool.Kind), "After=network-online.target", "", "[Container]", "Image=" + tool.Image, "ContainerName=" + tool.ContainerName, "PublishPort=127.0.0.1:" + strconv.Itoa(tool.Port) + ":" + strconv.Itoa(containerPort), "Memory=" + strconv.Itoa(tool.MemoryMB) + "M", "PidsLimit=" + strconv.Itoa(tool.PIDsLimit), "ReadOnly=true", "NoNewPrivileges=true", "DropCapability=ALL", "Volume=" + config + ":/nexa-config:ro,Z"}
	if tool.Kind == PHPMyAdmin {
		lines = append(lines, "Environment=PMA_HOST=host.containers.internal", "Volume="+config+"/sessions:/sessions:Z,U", "Volume="+config+"/config.user.inc.php:/etc/phpmyadmin/config.user.inc.php:ro,Z", "Volume="+config+"/config.secret.inc.php:/etc/phpmyadmin/config.secret.inc.php:ro,Z", "Tmpfs=/tmp:rw,noexec,nosuid,size=32m", "Tmpfs=/var/run/apache2:rw,noexec,nosuid,size=4m", "Tmpfs=/var/lock/apache2:rw,noexec,nosuid,size=4m", "Tmpfs=/var/log/apache2:rw,noexec,nosuid,size=16m")
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

func randomID() string {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		panic(err)
	}
	return hex.EncodeToString(value)
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
