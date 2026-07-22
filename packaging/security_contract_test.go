package packaging_test

import (
	"bufio"
	"os"
	"strings"
	"testing"
)

type unitDirectives map[string][]string

// readUnitSections keys every section of a unit file, because the crash-loop
// guards live in [Unit] while the sandbox and resource limits live in [Service]
// and systemd silently ignores either one placed in the other section.
func readUnitSections(t *testing.T, path string) map[string]unitDirectives {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	sections := map[string]unitDirectives{}
	section := ""
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.Trim(line, "[]")
			continue
		}
		if section == "" || line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if !found {
			t.Fatalf("%s contains an invalid %s directive %q", path, section, line)
		}
		if sections[section] == nil {
			sections[section] = unitDirectives{}
		}
		key = strings.TrimSpace(key)
		sections[section][key] = append(sections[section][key], strings.TrimSpace(value))
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return sections
}

func readServiceDirectives(t *testing.T, path string) unitDirectives {
	t.Helper()
	return readUnitSections(t, path)["Service"]
}

func readUnitDirectives(t *testing.T, path string) unitDirectives {
	t.Helper()
	return readUnitSections(t, path)["Unit"]
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

func requireDirectiveValue(t *testing.T, directives unitDirectives, key, wanted string) {
	t.Helper()
	for _, value := range directives[key] {
		if value == wanted {
			return
		}
	}
	t.Fatalf("service contract is missing %s=%s, got %q", key, wanted, directives[key])
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

func TestControlPlaneUsesPrivateStateAndSharedAgentCredential(t *testing.T) {
	directives := readServiceDirectives(t, "systemd/nexa-api.service")
	requireDirective(t, directives, "User", "nexa")
	requireDirective(t, directives, "Group", "nexa")
	requireDirective(t, directives, "UMask", "0077")
	requireAbsentDirective(t, directives, "LoadCredential")
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
	if len(execStart) != 1 || !strings.Contains(execStart[0], "--unix-socket /run/nexa-panel/api.sock") || !strings.Contains(execStart[0], "--agent-token /etc/nexa-panel/agent.token") {
		t.Fatalf("control plane must use the local API socket and shared group-scoped credential, got %q", execStart)
	}
	if strings.Contains(execStart[0], "--master-key") {
		t.Fatalf("pinning --master-key suppresses the relocation out of the state directory, got %q", execStart[0])
	}
	if paths, ok := directives["ReadWritePaths"]; ok {
		t.Fatalf("the control plane only reads the master key, so /etc must stay read-only, found ReadWritePaths=%q", paths)
	}
	provision := directives["ExecStartPre"]
	if len(provision) != 1 || !strings.HasPrefix(provision[0], "+") {
		t.Fatalf("the master key must be provisioned by one privileged pre-start command, got %q", provision)
	}
	if !strings.Contains(provision[0], "chown nexa:nexa /etc/nexa-panel/master.key") || !strings.Contains(provision[0], "chmod 0600 /etc/nexa-panel/master.key") {
		t.Fatalf("the provisioned master key must end up owner-only and readable by the control plane, got %q", provision[0])
	}
	if !strings.Contains(provision[0], "mv -- /var/lib/nexa-panel/master.key /etc/nexa-panel/master.key") {
		t.Fatalf("an existing key must move out of the state directory rather than be left beside control.db, got %q", provision[0])
	}
	for _, environment := range directives["Environment"] {
		if strings.HasPrefix(environment, "NEXA_AGENT_TOKEN=") {
			t.Fatalf("agent token source must not be exposed directly to the web-facing service: %q", environment)
		}
	}
	requireDirectiveValue(t, directives, "Environment", "NEXA_LOG_LEVEL=info")
	addressFamilies := directives["RestrictAddressFamilies"]
	if len(addressFamilies) != 1 {
		t.Fatalf("control plane address-family contract = %q", addressFamilies)
	}
	requireWordSet(t, addressFamilies[0], "AF_UNIX", "AF_INET", "AF_INET6")
}

func TestLongRunningUnitsBoundRestartsAndResources(t *testing.T) {
	for _, unit := range []string{"systemd/nexa-api.service", "systemd/nexa-agent.service"} {
		unitSection := readUnitDirectives(t, unit)
		requireDirective(t, unitSection, "StartLimitIntervalSec", "300s")
		requireDirective(t, unitSection, "StartLimitBurst", "10")

		directives := readServiceDirectives(t, unit)
		requireDirective(t, directives, "Restart", "on-failure")
		requireDirective(t, directives, "RestartSec", "3s")
		requireDirectiveValue(t, directives, "Environment", "NEXA_LOG_LEVEL=info")
		for _, key := range []string{"MemoryHigh", "MemoryMax", "CPUQuota", "TasksMax", "LimitNOFILE"} {
			if values := directives[key]; len(values) != 1 || values[0] == "" {
				t.Fatalf("%s must bound %s exactly once, got %q", unit, key, values)
			}
		}
		// A start limit only takes effect in [Unit]; systemd parses these keys
		// nowhere else and would silently drop them from [Service].
		for _, key := range []string{"StartLimitIntervalSec", "StartLimitBurst"} {
			if values, ok := directives[key]; ok {
				t.Fatalf("%s puts %s in [Service] where systemd ignores it, got %q", unit, key, values)
			}
		}
	}

	api := readServiceDirectives(t, "systemd/nexa-api.service")
	requireDirective(t, api, "MemoryHigh", "256M")
	requireDirective(t, api, "MemoryMax", "512M")
	requireDirective(t, api, "TasksMax", "512")

	// The agent's ceiling covers apt, dpkg and podman running in its cgroup, so
	// it must stay well clear of the control plane's, or a legitimate package
	// install is killed by the limit meant to catch a leak.
	agent := readServiceDirectives(t, "systemd/nexa-agent.service")
	requireDirective(t, agent, "MemoryHigh", "1G")
	requireDirective(t, agent, "MemoryMax", "1536M")
	requireDirective(t, agent, "TasksMax", "4096")
}

func TestTimerDrivenBackupIsBoundedButNeverRateLimitedOut(t *testing.T) {
	unitSection := readUnitDirectives(t, "systemd/nexa-panel-system-backup.service")
	requireDirective(t, unitSection, "ConditionPathExists", "/etc/nexa-panel/system-backup.env")
	// A start limit would disable scheduled backups after a streak of transient
	// failures, and the oneshot has no Restart= to loop on in the first place.
	requireDirective(t, unitSection, "StartLimitIntervalSec", "0")

	directives := readServiceDirectives(t, "systemd/nexa-panel-system-backup.service")
	if values, ok := directives["Restart"]; ok {
		t.Fatalf("the backup oneshot must not restart itself; the timer is the retry, got Restart=%q", values)
	}
	requireDirective(t, directives, "MemoryMax", "256M")
	requireDirective(t, directives, "TasksMax", "64")
	requireDirectiveValue(t, directives, "Environment", "NEXA_LOG_LEVEL=info")
	requireEmptyDirective(t, directives, "CapabilityBoundingSet")
	requireDirective(t, directives, "ProtectSystem", "strict")
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
		"/etc/nexa-panel/agent.token":                     {kind: "z", mode: "0640", user: "root", group: "nexa"},
		"/var/lib/nexa-panel":                             {kind: "d", mode: "0700", user: "nexa", group: "nexa"},
		"/var/lib/nexa-panel/control.db*":                 {kind: "z", mode: "0600", user: "nexa", group: "nexa"},
		"/etc/nexa-panel/admin-tools":                     {kind: "d", mode: "0711", user: "root", group: "root"},
		"/etc/nexa-panel/admin-tools/phpmyadmin":          {kind: "d", mode: "0750", user: "root", group: "33"},
		"/etc/nexa-panel/admin-tools/phpmyadmin/sessions": {kind: "d", mode: "0750", user: "33", group: "33"},
		"/etc/nexa-panel/admin-tools/pgadmin":             {kind: "d", mode: "0750", user: "root", group: "5050"},
		"/etc/nexa-panel/admin-tools/pgadmin/data":        {kind: "d", mode: "0700", user: "5050", group: "5050"},
		"/etc/nexa-panel/generated":                       {kind: "d", mode: "0711", user: "root", group: "root"},
		"/etc/nexa-panel/generated/tasks":                 {kind: "d", mode: "0711", user: "root", group: "root"},
		// Holds the root-owned FPM reload wrappers a sudoers rule names by
		// absolute path. 0711 root:root is what stops a site account from
		// listing, replacing, or adding a script that sudo would then run.
		"/etc/nexa-panel/generated/deploy": {kind: "d", mode: "0711", user: "root", group: "root"},
		// Holds the per-site authorized_keys files. sshd opens them after
		// dropping to the login user, so this directory has to stay traversable
		// by others — a mode without o+x makes every key-based login fail with
		// "Could not open user authorized keys", and no root-run test sees it
		// because root bypasses the traversal check. 0711 traverses without
		// letting a site account enumerate the other sites' key files.
		"/etc/nexa-panel/generated/ssh": {kind: "d", mode: "0711", user: "root", group: "root"},
		"/var/log/nexa-panel":           {kind: "d", mode: "0700", user: "nexa", group: "nexa"},
	}
	for path, expected := range wanted {
		if got, ok := entries[path]; !ok {
			t.Errorf("tmpfiles contract is missing %s", path)
		} else if got != expected {
			t.Errorf("tmpfiles entry %s = %+v, want %+v", path, got, expected)
		}
	}
}

func TestNginxProxyPreservesExternalAuthorityAndUsesOnlyTheLocalAPISocket(t *testing.T) {
	content, err := os.ReadFile("nginx/nexa-panel.conf.template")
	if err != nil {
		t.Fatal(err)
	}
	configuration := string(content)
	for _, required := range []string{
		"client_max_body_size 10m;",
		"client_body_timeout 5m;",
		"proxy_pass http://unix:/run/nexa-panel/api.sock;",
		"proxy_set_header Host $http_host;",
		"proxy_set_header X-Forwarded-Proto $scheme;",
	} {
		if !strings.Contains(configuration, required) {
			t.Errorf("Nginx template is missing %q", required)
		}
	}
	if strings.Contains(configuration, "client_max_body_size 2g") {
		t.Fatal("Nginx template still permits unbounded multi-gigabyte request bodies")
	}
	if strings.Contains(configuration, "proxy_set_header Host $host;") {
		t.Fatal("Nginx template drops non-default ports from the external request authority")
	}
}
