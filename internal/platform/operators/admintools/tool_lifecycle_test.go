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
	for _, wanted := range []string{"PublishPort=127.0.0.1:18081:5050", "Memory=256M", "PidsLimit=192", "ReadOnly=true", "NoNewPrivileges=true", "DropCapability=ALL", "config_local.py:/pgadmin4/config_local.py:ro", "PGADMIN_REPLACE_SERVERS_ON_STARTUP=True", "PGPASS_FILE=/nexa-config/pgpass"} {
		if !strings.Contains(text, wanted) {
			t.Errorf("quadlet missing %q:\n%s", wanted, text)
		}
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
