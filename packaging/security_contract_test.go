package packaging_test

import (
	"bufio"
	"os"
	"strings"
	"testing"
)

type unitDirectives map[string][]string

func readServiceDirectives(t *testing.T, path string) unitDirectives {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	directives := unitDirectives{}
	section := ""
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.Trim(line, "[]")
			continue
		}
		if section != "Service" || line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if !found {
			t.Fatalf("%s contains an invalid service directive %q", path, line)
		}
		key = strings.TrimSpace(key)
		directives[key] = append(directives[key], strings.TrimSpace(value))
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return directives
}

func requireDirective(t *testing.T, directives unitDirectives, key, wanted string) {
	t.Helper()
	values := directives[key]
	if len(values) == 0 {
		t.Fatalf("service contract is missing %s=%s", key, wanted)
	}
	if got := values[len(values)-1]; got != wanted {
		t.Fatalf("%s = %q, want %q", key, got, wanted)
	}
}

func requireEmptyDirective(t *testing.T, directives unitDirectives, key string) {
	t.Helper()
	values, ok := directives[key]
	if !ok || len(values) == 0 {
		t.Fatalf("service contract is missing an explicit empty %s=", key)
	}
	if got := values[len(values)-1]; got != "" {
		t.Fatalf("%s = %q, want an empty capability set", key, got)
	}
}

func requireAbsentDirective(t *testing.T, directives unitDirectives, key string) {
	t.Helper()
	if values, ok := directives[key]; ok {
		t.Fatalf("shared directory must be owned by tmpfiles, found %s=%q", key, values)
	}
}

func requireWordSet(t *testing.T, value string, wanted ...string) {
	t.Helper()
	got := map[string]bool{}
	for _, word := range strings.Fields(value) {
		got[word] = true
	}
	if len(got) != len(wanted) {
		t.Fatalf("directive words = %q, want exactly %q", value, wanted)
	}
	for _, word := range wanted {
		if !got[word] {
			t.Fatalf("directive words = %q, missing %q", value, word)
		}
	}
}

func TestPrivilegedAgentRetainsPackageWritesInsideReadOnlyHostSandbox(t *testing.T) {
	directives := readServiceDirectives(t, "systemd/nexa-agent.service")
	requireDirective(t, directives, "User", "root")
	requireDirective(t, directives, "Group", "nexa")
	requireDirective(t, directives, "UMask", "0177")
	requireDirective(t, directives, "ProtectSystem", "strict")
	requireDirective(t, directives, "ProtectHome", "true")
	requireDirective(t, directives, "NoNewPrivileges", "true")
	requireDirective(t, directives, "ConfigurationDirectoryMode", "0711")

	writeRoots := directives["ReadWritePaths"]
	if len(writeRoots) != 1 {
		t.Fatalf("agent must declare one auditable ReadWritePaths boundary, got %q", writeRoots)
	}
	requireWordSet(t, writeRoots[0], "/boot", "/etc", "/opt", "/srv", "/usr", "/var", "/run/containers", "/run/nexa-panel", "/run/php", "/run/postgresql")

	for _, key := range []string{
		"KeyringMode", "LockPersonality", "ProtectClock", "ProtectControlGroups",
		"ProtectHostname", "ProtectKernelLogs", "ProtectKernelModules",
		"ProtectKernelTunables", "RestrictRealtime", "SystemCallArchitectures",
	} {
		wanted := "true"
		if key == "KeyringMode" {
			wanted = "private"
		} else if key == "SystemCallArchitectures" {
			wanted = "native"
		}
		requireDirective(t, directives, key, wanted)
	}

	requireAbsentDirective(t, directives, "RuntimeDirectory")
	requireAbsentDirective(t, directives, "StateDirectory")
	requireAbsentDirective(t, directives, "LogsDirectory")
	requireDirective(t, directives, "ExecStartPre", "/usr/bin/nexa agent-token --path /etc/nexa-panel/agent.token")
	requireAbsentDirective(t, directives, "ExecStartPost")
}

func TestControlPlaneUsesPrivateStateAndSystemdCredential(t *testing.T) {
	directives := readServiceDirectives(t, "systemd/nexa-api.service")
	requireDirective(t, directives, "User", "nexa")
	requireDirective(t, directives, "Group", "nexa")
	requireDirective(t, directives, "UMask", "0077")
	requireDirective(t, directives, "LoadCredential", "agent.token:/etc/nexa-panel/agent.token")
	requireDirective(t, directives, "StateDirectoryMode", "0700")
	requireDirective(t, directives, "LogsDirectoryMode", "0700")
	requireDirective(t, directives, "ProtectSystem", "strict")
	requireDirective(t, directives, "ProtectHome", "true")
	requireDirective(t, directives, "NoNewPrivileges", "true")
	requireDirective(t, directives, "RestrictNamespaces", "true")
	requireEmptyDirective(t, directives, "CapabilityBoundingSet")
	requireEmptyDirective(t, directives, "AmbientCapabilities")
	requireAbsentDirective(t, directives, "RuntimeDirectory")

	execStart := directives["ExecStart"]
	if len(execStart) != 1 || !strings.Contains(execStart[0], "--unix-socket /run/nexa-panel/api.sock") || !strings.Contains(execStart[0], "--agent-token %d/agent.token") {
		t.Fatalf("control plane must use the local API socket and private credential copy, got %q", execStart)
	}
	for _, environment := range directives["Environment"] {
		if strings.HasPrefix(environment, "NEXA_AGENT_TOKEN=") {
			t.Fatalf("agent token source must not be exposed directly to the web-facing service: %q", environment)
		}
	}
	addressFamilies := directives["RestrictAddressFamilies"]
	if len(addressFamilies) != 1 {
		t.Fatalf("control plane address-family contract = %q", addressFamilies)
	}
	requireWordSet(t, addressFamilies[0], "AF_UNIX", "AF_INET", "AF_INET6")
}

type tmpfilesEntry struct {
	kind, mode, user, group string
}

func readTmpfiles(t *testing.T) map[string]tmpfilesEntry {
	t.Helper()
	file, err := os.Open("tmpfiles/nexa-panel.conf")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	entries := map[string]tmpfilesEntry{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 0 || strings.HasPrefix(fields[0], "#") {
			continue
		}
		if len(fields) < 5 {
			t.Fatalf("invalid tmpfiles entry %q", scanner.Text())
		}
		entries[fields[1]] = tmpfilesEntry{kind: fields[0], mode: fields[2], user: fields[3], group: fields[4]}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return entries
}

func TestTmpfilesSeparatesRuntimeStateSecretsAndContainerData(t *testing.T) {
	entries := readTmpfiles(t)
	wanted := map[string]tmpfilesEntry{
		"/run/nexa-panel":                                 {kind: "d", mode: "2750", user: "nexa", group: "nexa"},
		"/etc/nexa-panel":                                 {kind: "d", mode: "0711", user: "root", group: "root"},
		"/etc/nexa-panel/agent.token":                     {kind: "z", mode: "0600", user: "root", group: "root"},
		"/var/lib/nexa-panel":                             {kind: "d", mode: "0700", user: "nexa", group: "nexa"},
		"/var/lib/nexa-panel/control.db*":                 {kind: "z", mode: "0600", user: "nexa", group: "nexa"},
		"/etc/nexa-panel/admin-tools":                     {kind: "d", mode: "0700", user: "root", group: "root"},
		"/etc/nexa-panel/admin-tools/phpmyadmin":          {kind: "d", mode: "0750", user: "root", group: "33"},
		"/etc/nexa-panel/admin-tools/phpmyadmin/sessions": {kind: "d", mode: "0750", user: "33", group: "33"},
		"/etc/nexa-panel/admin-tools/pgadmin":             {kind: "d", mode: "0750", user: "root", group: "5050"},
		"/etc/nexa-panel/admin-tools/pgadmin/data":        {kind: "d", mode: "0700", user: "5050", group: "5050"},
		"/etc/nexa-panel/generated":                       {kind: "d", mode: "0711", user: "root", group: "root"},
		"/etc/nexa-panel/generated/tasks":                 {kind: "d", mode: "0711", user: "root", group: "root"},
		"/var/log/nexa-panel":                             {kind: "d", mode: "0700", user: "nexa", group: "nexa"},
	}
	for path, expected := range wanted {
		if got, ok := entries[path]; !ok {
			t.Errorf("tmpfiles contract is missing %s", path)
		} else if got != expected {
			t.Errorf("tmpfiles entry %s = %+v, want %+v", path, got, expected)
		}
	}
}

func TestNginxProxyBoundsBodiesAndUsesOnlyTheLocalAPISocket(t *testing.T) {
	content, err := os.ReadFile("nginx/nexa-panel.conf.template")
	if err != nil {
		t.Fatal(err)
	}
	configuration := string(content)
	for _, required := range []string{
		"client_max_body_size 10m;",
		"client_body_timeout 5m;",
		"proxy_pass http://unix:/run/nexa-panel/api.sock;",
		"proxy_set_header X-Forwarded-Proto $scheme;",
	} {
		if !strings.Contains(configuration, required) {
			t.Errorf("Nginx template is missing %q", required)
		}
	}
	if strings.Contains(configuration, "client_max_body_size 2g") {
		t.Fatal("Nginx template still permits unbounded multi-gigabyte request bodies")
	}
}
