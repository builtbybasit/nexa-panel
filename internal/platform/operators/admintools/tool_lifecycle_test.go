package admintools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeRunner struct {
	active   map[string]bool
	commands []Command
}

func (r *fakeRunner) Run(_ context.Context, command Command) ([]byte, error) {
	r.commands = append(r.commands, command)
	if command.Name == "systemctl" && len(command.Args) > 1 {
		unit := command.Args[len(command.Args)-1]
		switch command.Args[0] {
		case "start":
			r.active[unit] = true
		case "restart":
			r.active[unit] = true
		case "stop":
			r.active[unit] = false
		case "is-active":
			if r.active[unit] {
				return []byte("active\n"), nil
			}
			return []byte("inactive\n"), nil
		}
	}
	return nil, nil
}

func TestDeployRendersHardenedLocalhostQuadlet(t *testing.T) {
	runner := &fakeRunner{active: map[string]bool{}}
	operator, err := NewHostOperator(runner, HostConfig{QuadletRoot: filepath.Join(t.TempDir(), "quadlets"), ConfigRoot: filepath.Join(t.TempDir(), "config")})
	if err != nil {
		t.Fatal(err)
	}
	operator.now = func() time.Time { return time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC) }
	plan, err := operator.Plan(context.Background(), Change{Action: ActionDeploy, Tool: Tool{Kind: PGAdmin}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := operator.Apply(context.Background(), Execution{Plan: plan})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Verified || result.Tool.Status != "active" {
		t.Fatalf("result=%+v", result)
	}
	encoded, err := os.ReadFile(operator.quadletPath(PGAdmin))
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, wanted := range []string{"PublishPort=127.0.0.1:18081:5050", "PodmanArgs=--memory=256m", "PidsLimit=192", "ReadOnly=true", "NoNewPrivileges=true", "DropCapability=ALL", "config_local.py:/pgadmin4/config_local.py:ro", "PGADMIN_REPLACE_SERVERS_ON_STARTUP=True", "PGPASS_FILE=/nexa-config/pgpass"} {
		if !strings.Contains(text, wanted) {
			t.Errorf("quadlet missing %q:\n%s", wanted, text)
		}
	}
	config, err := os.ReadFile(filepath.Join(operator.configRoot, "pgadmin", "config_local.py"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(config), "WEBSERVER_AUTO_CREATE_USER = False") || strings.Contains(string(config), "HTTP_X_FORWARDED_USER") || strings.Contains(string(config), "HTTP_X_NEXA_PGADMIN_DISABLED") {
		t.Fatalf("deployed pgAdmin must fail closed until a session capability is installed:\n%s", config)
	}
}

func TestPhpMyAdminQuadletGrantsPrivilegedPortBind(t *testing.T) {
	// Apache binds port 80 during startup, which needs CAP_NET_BIND_SERVICE. The
	// hardened base drops all capabilities, so the tool's Quadlet must add this
	// one back or the container crash-loops with "could not bind to 0.0.0.0:80".
	quadlet := renderQuadlet(Tool{Kind: PHPMyAdmin, Port: 18080, MemoryMB: 128, PIDsLimit: 128, Image: "docker.io/library/phpmyadmin:5.2.3", ContainerName: "nexa-phpmyadmin"}, t.TempDir())
	if !strings.Contains(quadlet, "AddCapability=NET_BIND_SERVICE") {
		t.Fatalf("phpMyAdmin quadlet missing NET_BIND_SERVICE grant:\n%s", quadlet)
	}
	pgadmin := renderQuadlet(Tool{Kind: PGAdmin, Port: 18081, MemoryMB: 256, PIDsLimit: 192, Image: "docker.io/dpage/pgadmin4:9.16", ContainerName: "nexa-pgadmin"}, t.TempDir())
	if strings.Contains(pgadmin, "AddCapability") {
		t.Fatalf("pgAdmin listens on an unprivileged port and must not add capabilities:\n%s", pgadmin)
	}
}

func TestPlanRejectsUnsafeLimits(t *testing.T) {
	operator, _ := NewHostOperator(&fakeRunner{active: map[string]bool{}}, HostConfig{QuadletRoot: filepath.Join(t.TempDir(), "quadlets"), ConfigRoot: filepath.Join(t.TempDir(), "config")})
	if _, err := operator.Plan(context.Background(), Change{Action: ActionDeploy, Tool: Tool{Kind: PHPMyAdmin, MemoryMB: 2048}}); err == nil {
		t.Fatal("expected memory limit rejection")
	}
}

func TestLaunchBindsCredentialAndCreatesServerSideSignonSession(t *testing.T) {
	runner := &fakeRunner{active: map[string]bool{"nexa-phpmyadmin.service": true}}
	configRoot := filepath.Join(t.TempDir(), "config")
	operator, _ := NewHostOperator(runner, HostConfig{QuadletRoot: filepath.Join(t.TempDir(), "quadlets"), ConfigRoot: configRoot})
	operator.now = func() time.Time { return time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC) }
	secret := "database-secret"
	digest := sha256.Sum256([]byte(secret))
	change := Change{Action: ActionLaunch, Tool: Tool{Kind: PHPMyAdmin}, Launch: &Launch{SessionID: "launchSession1234", PanelUser: "admin", DatabaseHost: "host.containers.internal", DatabasePort: 3306, Database: "app_db", Username: "app_user", SecretSHA256: hex.EncodeToString(digest[:])}}
	plan, err := operator.Plan(context.Background(), change)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := operator.Apply(context.Background(), Execution{Plan: plan, Secret: "wrong"}); err == nil {
		t.Fatal("expected secret binding rejection")
	}
	observation, err := operator.Apply(context.Background(), Execution{Plan: plan, Secret: secret})
	if err != nil {
		t.Fatal(err)
	}
	if observation.UpstreamCookieName != "SignonSession" || observation.UpstreamCookieValue != "launchSession1234" {
		t.Fatalf("observation=%+v", observation)
	}
	encoded, err := os.ReadFile(filepath.Join(configRoot, "phpmyadmin", "sessions", "sess_launchSession1234"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), secret) || !strings.Contains(string(encoded), "PMA_single_signon_only_db") {
		t.Fatalf("session=%s", encoded)
	}
}

func TestPGAdminLaunchRotatesSessionBoundRemoteUserHeader(t *testing.T) {
	runner := &fakeRunner{active: map[string]bool{"nexa-pgadmin.service": true}}
	configRoot := filepath.Join(t.TempDir(), "config")
	operator, err := NewHostOperator(runner, HostConfig{QuadletRoot: filepath.Join(t.TempDir(), "quadlets"), ConfigRoot: configRoot})
	if err != nil {
		t.Fatal(err)
	}
	operator.now = func() time.Time { return time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC) }
	if err := operator.prepareToolConfig(PGAdmin); err != nil {
		t.Fatal(err)
	}
	secret := "database-secret"
	digest := sha256.Sum256([]byte(secret))
	launch := func(sessionID string) string {
		t.Helper()
		change := Change{Action: ActionLaunch, Tool: Tool{Kind: PGAdmin}, Launch: &Launch{
			SessionID: sessionID, PanelUser: "admin@nexa.example.com", DatabaseHost: "host.containers.internal",
			DatabasePort: 5432, Database: "app_db", Username: "app_user", SecretSHA256: hex.EncodeToString(digest[:]),
		}}
		plan, planErr := operator.Plan(context.Background(), change)
		if planErr != nil {
			t.Fatal(planErr)
		}
		if _, applyErr := operator.Apply(context.Background(), Execution{Plan: plan, Secret: secret}); applyErr != nil {
			t.Fatal(applyErr)
		}
		encoded, readErr := os.ReadFile(filepath.Join(configRoot, "pgadmin", "config_local.py"))
		if readErr != nil {
			t.Fatal(readErr)
		}
		return string(encoded)
	}

	firstSession := "firstSessionToken1234"
	firstConfig := launch(firstSession)
	firstVariable, err := pgAdminRemoteUserVariable(firstSession)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(firstConfig, "WEBSERVER_REMOTE_USER = '"+firstVariable+"'") || strings.Contains(firstConfig, firstSession) {
		t.Fatalf("first pgAdmin config is not bound to a non-plaintext session capability:\n%s", firstConfig)
	}

	secondSession := "secondSessionToken5678"
	secondConfig := launch(secondSession)
	secondVariable, err := pgAdminRemoteUserVariable(secondSession)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(secondConfig, "WEBSERVER_REMOTE_USER = '"+secondVariable+"'") || strings.Contains(secondConfig, firstVariable) {
		t.Fatalf("second launch did not revoke the previous trusted header:\n%s", secondConfig)
	}
}

func TestSecureWriteDoesNotFollowPredictableTemporarySymlink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "servers.json")
	victim := filepath.Join(root, "victim")
	if err := os.WriteFile(victim, []byte("do not overwrite"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, target+".tmp"); err != nil {
		t.Fatal(err)
	}
	if err := secureWrite(target, []byte("managed"), 0o640); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(victim)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "do not overwrite" {
		t.Fatalf("victim contents = %q; secureWrite followed the temporary symlink", contents)
	}
}

func TestPlanRejectsQuadletImageDirectiveInjection(t *testing.T) {
	operator, err := NewHostOperator(&fakeRunner{active: map[string]bool{}}, HostConfig{QuadletRoot: filepath.Join(t.TempDir(), "quadlets"), ConfigRoot: filepath.Join(t.TempDir(), "config")})
	if err != nil {
		t.Fatal(err)
	}
	_, err = operator.Plan(context.Background(), Change{Action: ActionDeploy, Tool: Tool{Kind: PGAdmin, Image: "docker.io/dpage/pgadmin4:9.16\nVolume=/etc:/host"}})
	if err == nil || !strings.Contains(err.Error(), "image reference") {
		t.Fatalf("Plan() error = %v, want an invalid image-reference error", err)
	}
}
