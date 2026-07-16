package postgres

import (
	"path/filepath"

	"encoding/hex"
	"os/exec"

	"context"

	"encoding/json"

	"crypto/sha256"
	"os/user"

	"strconv"

	"errors"
	"fmt"

	"crypto/rand"

	"strings"
)

func (execRunner) Run(ctx context.Context, command Command) ([]byte, error) {
	process := exec.CommandContext(ctx, command.Name, command.Args...)
	if command.Stdin != "" {
		process.Stdin = strings.NewReader(command.Stdin)
	}
	output := &cappedOutput{limit: 64 * 1024}
	process.Stdout, process.Stderr = output, output
	err := process.Run()
	return output.data, err
}

func (w *cappedOutput) Write(value []byte) (int, error) {
	original := len(value)
	remaining := w.limit - len(w.data)
	if remaining > 0 {
		if len(value) > remaining {
			value = value[:remaining]
		}
		w.data = append(w.data, value...)
	}
	return original, nil
}

func (o *HostOperator) instance(version, cluster string, port int, status, owner, dataPath, logPath string) Instance {
	return Instance{ID: instanceID(version, cluster), Version: version, Cluster: cluster, Port: port, Status: status, Owner: owner, DataPath: filepath.Clean(dataPath), SocketPath: o.socketRoot, LogPath: filepath.Clean(logPath), ConfigPath: filepath.Join(o.configRoot, version, cluster), SystemdUnit: fmt.Sprintf("postgresql@%s-%s.service", version, cluster), ManagedByNexa: strings.HasPrefix(cluster, "nexa_")}
}

func supportedVersion(version string) bool {
	return version == "16" || version == "17" || version == "18"
}

func instanceID(version, cluster string) string { return "postgresql_" + version + "_" + cluster }

func first(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func ownerFromUID(encoded json.RawMessage) string {
	value := strings.Trim(strings.TrimSpace(string(encoded)), `"`)
	if value == "" || value == "null" {
		return ""
	}
	account, err := user.LookupId(value)
	if err != nil {
		return ""
	}
	return account.Username
}

func jsonTruthy(encoded json.RawMessage) bool {
	value := strings.Trim(strings.ToLower(strings.TrimSpace(string(encoded))), `"`)
	return value == "true" || value == "1"
}

func (value *jsonString) UnmarshalJSON(encoded []byte) error {
	if len(encoded) > 0 && encoded[0] == '"' {
		var text string
		if err := json.Unmarshal(encoded, &text); err != nil {
			return err
		}
		*value = jsonString(text)
		return nil
	}
	*value = jsonString(strings.TrimSpace(string(encoded)))
	return nil
}

func (value *jsonInt) UnmarshalJSON(encoded []byte) error {
	text := strings.Trim(strings.TrimSpace(string(encoded)), `"`)
	parsed, err := strconv.Atoi(text)
	if err != nil {
		*value = 0
		return nil
	}
	*value = jsonInt(parsed)
	return nil
}

func firstAvailablePort(instances []Instance) int {
	used := make(map[int]struct{}, len(instances))
	for _, instance := range instances {
		used[instance.Port] = struct{}{}
	}
	for port := 5432; port <= 5532; port++ {
		if _, exists := used[port]; !exists {
			return port
		}
	}
	return 0
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

func asPostgres(version, program string, args ...string) Command {
	return Command{Name: "runuser", Args: append([]string{"-u", "postgres", "--", binary(version, program)}, args...)}
}

func binary(version, program string) string {
	return filepath.Join("/usr/lib/postgresql", version, "bin", program)
}

func quoteIdentifier(value string) string { return `"` + strings.ReplaceAll(value, `"`, `""`) + `"` }

func terminateSQL(database string) string {
	return "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = '" + database + "' AND pid <> pg_backend_pid();\n"
}

func truncateName(value string) string {
	if len(value) > 63 {
		return value[:63]
	}
	return value
}

func redactCommandError(action string, output []byte, err error, secret string) error {
	return commandError(action, []byte(strings.ReplaceAll(string(output), secret, "[redacted]")), err)
}

func firstError(values ...error) error {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return errors.New("operation failed")
}
