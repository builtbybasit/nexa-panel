package admintools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeRunner struct {
	active            map[string]bool
	installedPackages map[string]bool
	commands          []Command
	mysqlUnavailable  bool
}

func (r *fakeRunner) Run(_ context.Context, command Command) ([]byte, error) {
	r.commands = append(r.commands, command)
	if r.mysqlUnavailable && command.Name == "sh" && len(command.Args) == 2 && strings.Contains(command.Args[1], "--protocol=socket") {
		return []byte("ERROR 2002 (HY000): Can't connect to local MySQL server through socket\n"), errors.New("exit status 1")
	}
	if command.Name == "dpkg-query" && len(command.Args) > 0 && r.installedPackages[command.Args[len(command.Args)-1]] {
		return []byte("installed"), nil
	}
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
	root := t.TempDir()
	operator, err := NewHostOperator(runner, HostConfig{QuadletRoot: filepath.Join(root, "quadlets"), SystemdRoot: filepath.Join(root, "systemd"), ConfigRoot: filepath.Join(root, "config")})
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
	for _, wanted := range []string{"PublishPort=127.0.0.1:18081:5050", "PodmanArgs=--memory=512m", "PidsLimit=192", "ReadOnly=true", "NoNewPrivileges=true", "DropCapability=ALL", "config_local.py:/pgadmin4/config_local.py:ro", "PGADMIN_REPLACE_SERVERS_ON_STARTUP=True", "PGPASS_FILE=/nexa-config/pgpass", "Volume=/run/postgresql:/run/postgresql"} {
		if !strings.Contains(text, wanted) {
			t.Errorf("quadlet missing %q:\n%s", wanted, text)
		}
	}
	// The PostgreSQL socket is shared with the host runtime, so it must not carry a
	// ':Z' SELinux relabel (which would privatise the host's own postgres directory).
	if strings.Contains(text, "Volume=/run/postgresql:/run/postgresql:") {
		t.Errorf("postgres socket mount must not be relabeled or read-only:\n%s", text)
	}
	config, err := os.ReadFile(filepath.Join(operator.configRoot, "pgadmin", "config_local.py"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(config), "WEBSERVER_AUTO_CREATE_USER = False") || strings.Contains(string(config), "HTTP_X_FORWARDED_USER") || strings.Contains(string(config), "HTTP_X_NEXA_PGADMIN_DISABLED") {
		t.Fatalf("deployed pgAdmin must fail closed until a session capability is installed:\n%s", config)
	}
	timer, err := os.ReadFile(operator.pgAdminIdleTimerPath())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(timer), "OnActiveSec=15min") || !strings.Contains(string(timer), "Unit=nexa-pgadmin-idle-stop.service") {
		t.Fatalf("pgAdmin idle timer is not session-aligned:\n%s", timer)
	}
	stopUnit, err := os.ReadFile(operator.pgAdminIdleServicePath())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(stopUnit), "ExecStart=/bin/systemctl stop nexa-pgadmin.service") || !strings.Contains(string(stopUnit), "truncate --size=0 "+filepath.Join(root, "config", "pgadmin", "pgpass")) || !strings.Contains(string(stopUnit), "truncate --size=0 "+filepath.Join(root, "config", "pgadmin", "config_local.py")) {
		t.Fatalf("pgAdmin idle service does not stop only pgAdmin:\n%s", stopUnit)
	}
}

func TestDeploysNativePHPMyAdminBehindLoopbackNginx(t *testing.T) {
	root := t.TempDir()
	runner := &fakeRunner{active: map[string]bool{}}
	operator, err := NewHostOperator(runner, HostConfig{
		QuadletRoot:          filepath.Join(root, "quadlets"),
		SystemdRoot:          filepath.Join(root, "systemd"),
		ConfigRoot:           filepath.Join(root, "config"),
		NginxAvailableRoot:   filepath.Join(root, "nginx-available"),
		NginxEnabledRoot:     filepath.Join(root, "nginx-enabled"),
		PHPMyAdminConfigRoot: filepath.Join(root, "phpmyadmin-etc"),
	})
	if err != nil {
		t.Fatal(err)
	}
	operator.now = func() time.Time { return time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC) }
	plan, err := operator.Plan(context.Background(), Change{Action: ActionDeploy, Tool: Tool{Kind: PHPMyAdmin}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := operator.Apply(context.Background(), Execution{Plan: plan})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Verified || result.Tool.Status != "active" || result.Tool.Image != "" || result.Tool.MemoryMB != 0 {
		t.Fatalf("result=%+v", result)
	}
	info, err := os.Stat(operator.configRoot)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o711 {
		t.Fatalf("native phpMyAdmin configuration root mode = %v, want 0711", info.Mode().Perm())
	}
	if _, err := os.Stat(operator.quadletPath(PHPMyAdmin)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("native phpMyAdmin must not leave a Quadlet: %v", err)
	}
	nginxConfig, err := os.ReadFile(operator.phpMyAdminNginxPath())
	if err != nil {
		t.Fatal(err)
	}
	for _, wanted := range []string{"listen 127.0.0.1:18080", "root /usr/share/phpmyadmin", "fastcgi_pass unix:/run/php/php8.3-fpm.sock", "session.save_path=" + filepath.Join(root, "config", "phpmyadmin", "sessions"), "location = /nexa-signon-failed", "location ~ ^/(setup|libraries|templates)/"} {
		if !strings.Contains(string(nginxConfig), wanted) {
			t.Fatalf("native phpMyAdmin Nginx config missing %q:\n%s", wanted, nginxConfig)
		}
	}
	unit, err := os.ReadFile(operator.nativePHPMyAdminUnitPath())
	if err != nil {
		t.Fatal(err)
	}
	for _, wanted := range []string{"Type=oneshot", "RemainAfterExit=yes", "Requires=nginx.service php8.3-fpm.service", "ExecStop=/usr/bin/rm -f", "ExecStart=/usr/sbin/nginx -s reload", "ExecStop=/usr/sbin/nginx -s reload"} {
		if !strings.Contains(string(unit), wanted) {
			t.Fatalf("native phpMyAdmin unit missing %q:\n%s", wanted, unit)
		}
	}
	if strings.Contains(string(unit), "systemctl reload nginx") {
		t.Fatalf("native phpMyAdmin unit must not nest a systemctl transaction:\n%s", unit)
	}
	loader, err := os.ReadFile(operator.phpMyAdminLoaderPath())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(loader), filepath.Join(root, "config", "phpmyadmin", "config.user.inc.php")) || !strings.Contains(string(loader), filepath.Join(root, "config", "phpmyadmin", "config.secret.inc.php")) {
		t.Fatalf("phpMyAdmin loader does not include the managed configuration:\n%s", loader)
	}
	dropIn, err := os.ReadFile(operator.phpFPMDropInPath())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(dropIn), "[Service]") || !strings.Contains(string(dropIn), "ReadWritePaths=-"+filepath.Join(root, "config", "phpmyadmin", "sessions")) {
		t.Fatalf("php-fpm drop-in does not grant session write access:\n%s", dropIn)
	}
	commands := fmt.Sprint(runner.commands)
	for _, wanted := range []string{"debconf-set-selections", "phpmyadmin", "php8.3-fpm", "php8.3-mysql", "restart php8.3-fpm.service", "enable nexa-phpmyadmin.service"} {
		if !strings.Contains(commands, wanted) {
			t.Fatalf("native phpMyAdmin deployment did not invoke %q: %s", wanted, commands)
		}
	}

	// Configuration storage: the loader conditionally includes the control config,
	// the deploy loads the pma__ schema, and the emitted config wires the control
	// user and every storage table so no "storage not configured" warning shows.
	controlPath := filepath.Join(root, "config", "phpmyadmin", "config.control.inc.php")
	if !strings.Contains(string(loader), controlPath) || !strings.Contains(string(loader), "is_readable(") {
		t.Fatalf("phpMyAdmin loader must include the control storage config when present:\n%s", loader)
	}
	if !strings.Contains(commands, phpMyAdminCreateTablesSQL) {
		t.Fatalf("deploy did not load the phpMyAdmin control storage schema: %s", commands)
	}
	// The control-storage SQL must resolve its client at run time so it works on
	// MariaDB hosts (11+ drops the `mysql` symlink), not only MySQL.
	if !strings.Contains(commands, "command -v mysql || command -v mariadb") {
		t.Fatalf("control storage must fall back to the mariadb client: %s", commands)
	}
	control, err := os.ReadFile(controlPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, wanted := range []string{"$cfg['Servers'][1]['controluser'] = 'pma';", "$cfg['Servers'][1]['pmadb'] = 'phpmyadmin';", "$cfg['Servers'][1]['relation'] = 'pma__relation';", "$cfg['Servers'][1]['column_info'] = 'pma__column_info';", "$cfg['Servers'][1]['favorite'] = 'pma__favorite';"} {
		if !strings.Contains(string(control), wanted) {
			t.Fatalf("phpMyAdmin control storage config missing %q:\n%s", wanted, control)
		}
	}
	// The control password must never reach a process argument list; it lives only
	// in the 0640 config and the transient 0600 grant script (already removed).
	var controlPass string
	for _, line := range strings.Split(string(control), "\n") {
		if strings.Contains(line, "controlpass") {
			controlPass = line
		}
	}
	if controlPass == "" || strings.Contains(commands, "IDENTIFIED BY") {
		t.Fatalf("control password handling leaked into command arguments or is missing from config:\ncommands=%s\nconfig=%s", commands, control)
	}
	if _, err := os.Stat(filepath.Join(root, "config", "phpmyadmin", ".pma-control-grant.sql")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("transient control grant script was not cleaned up: %v", err)
	}
}

func TestNativePHPMyAdminDeploySucceedsWhenMySQLIsUnavailable(t *testing.T) {
	installed := make(map[string]bool, len(nativePHPMyAdminPackages))
	for _, name := range nativePHPMyAdminPackages {
		installed[name] = true
	}
	runner := &fakeRunner{active: map[string]bool{}, installedPackages: installed, mysqlUnavailable: true}
	root := t.TempDir()
	operator, err := NewHostOperator(runner, HostConfig{
		QuadletRoot: filepath.Join(root, "quadlets"), SystemdRoot: filepath.Join(root, "systemd"), ConfigRoot: filepath.Join(root, "config"),
		NginxAvailableRoot: filepath.Join(root, "nginx-available"), NginxEnabledRoot: filepath.Join(root, "nginx-enabled"), PHPMyAdminConfigRoot: filepath.Join(root, "phpmyadmin-etc"),
	})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := operator.Plan(context.Background(), Change{Action: ActionDeploy, Tool: Tool{Kind: PHPMyAdmin}})
	if err != nil {
		t.Fatal(err)
	}
	// The deploy must still succeed — phpMyAdmin is usable without the storage.
	if _, err := operator.Apply(context.Background(), Execution{Plan: plan}); err != nil {
		t.Fatalf("deploy must tolerate an offline MySQL: %v", err)
	}
	// No control config is left behind, so the guarded loader require is skipped and
	// no half-configured control user is referenced.
	if _, err := os.Stat(filepath.Join(root, "config", "phpmyadmin", "config.control.inc.php")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("control config must be absent when MySQL is unreachable: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "config", "phpmyadmin", ".pma-control-grant.sql")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("transient grant script must be cleaned up even on the skip path: %v", err)
	}
}

func TestNativePHPMyAdminDeploySkipsAPTWhenPackagesAreInstalled(t *testing.T) {
	installed := make(map[string]bool, len(nativePHPMyAdminPackages))
	for _, name := range nativePHPMyAdminPackages {
		installed[name] = true
	}
	runner := &fakeRunner{active: map[string]bool{}, installedPackages: installed}
	root := t.TempDir()
	operator, err := NewHostOperator(runner, HostConfig{
		QuadletRoot: filepath.Join(root, "quadlets"), SystemdRoot: filepath.Join(root, "systemd"), ConfigRoot: filepath.Join(root, "config"),
		NginxAvailableRoot: filepath.Join(root, "nginx-available"), NginxEnabledRoot: filepath.Join(root, "nginx-enabled"), PHPMyAdminConfigRoot: filepath.Join(root, "phpmyadmin-etc"),
	})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := operator.Plan(context.Background(), Change{Action: ActionDeploy, Tool: Tool{Kind: PHPMyAdmin}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := operator.Apply(context.Background(), Execution{Plan: plan}); err != nil {
		t.Fatal(err)
	}
	for _, command := range runner.commands {
		arguments := strings.Join(command.Args, " ")
		// The control-storage step legitimately shells out to mysql; only the apt
		// preseed / install path should be absent when packages are present.
		if command.Name == "apt-get" || command.Name == "/usr/bin/env" || (command.Name == "sh" && strings.Contains(arguments, "debconf-set-selections")) {
			t.Fatalf("installed native phpMyAdmin unexpectedly invoked package setup: %+v", command)
		}
	}
}

func TestStoppingPGAdminClearsSessionCredentialAndTrust(t *testing.T) {
	root := t.TempDir()
	runner := &fakeRunner{active: map[string]bool{"nexa-pgadmin.service": true, pgAdminIdleTimerUnit: true}}
	operator, err := NewHostOperator(runner, HostConfig{QuadletRoot: filepath.Join(root, "quadlets"), SystemdRoot: filepath.Join(root, "systemd"), ConfigRoot: filepath.Join(root, "config")})
	if err != nil {
		t.Fatal(err)
	}
	if err := operator.prepareToolConfig(PGAdmin); err != nil {
		t.Fatal(err)
	}
	if err := operator.writePGAdminIdleUnits(); err != nil {
		t.Fatal(err)
	}
	pgpassPath := filepath.Join(operator.configRoot, "pgadmin", "pgpass")
	configPath := filepath.Join(operator.configRoot, "pgadmin", "config_local.py")
	if err := secureWrite(pgpassPath, []byte("database-password"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := secureWrite(configPath, []byte("WEBSERVER_AUTO_CREATE_USER = True\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	plan, err := operator.Plan(context.Background(), Change{Action: ActionStop, Tool: Tool{Kind: PGAdmin}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := operator.Apply(context.Background(), Execution{Plan: plan}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{pgpassPath, configPath} {
		contents, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if len(contents) != 0 {
			t.Fatalf("stopped pgAdmin retained launch state in %s", path)
		}
	}
}

func TestPGAdminQuadletUsesItsRuntimeUIDAndGID(t *testing.T) {
	quadlet := renderQuadlet(Tool{Kind: PGAdmin, Port: 18081, MemoryMB: 512, PIDsLimit: 192, Image: "docker.io/dpage/pgadmin4:9.16", ContainerName: "nexa-pgadmin"}, t.TempDir())
	for _, wanted := range []string{"User=5050:5050", "DropCapability=ALL"} {
		if !strings.Contains(quadlet, wanted) {
			t.Fatalf("pgAdmin quadlet missing %q:\n%s", wanted, quadlet)
		}
	}
	if strings.Contains(quadlet, "AddCapability") {
		t.Fatalf("pgAdmin listens on an unprivileged port and must not add capabilities:\n%s", quadlet)
	}
}

func TestPhpMyAdminConfigSelectsMountedMySQLSocket(t *testing.T) {
	operator, _ := NewHostOperator(&fakeRunner{active: map[string]bool{}}, HostConfig{QuadletRoot: filepath.Join(t.TempDir(), "quadlets"), ConfigRoot: filepath.Join(t.TempDir(), "config")})
	if err := operator.prepareToolConfig(PHPMyAdmin); err != nil {
		t.Fatal(err)
	}
	encoded, err := os.ReadFile(filepath.Join(operator.configRoot, "phpmyadmin", "config.user.inc.php"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), "$cfg['Servers'][1]['connect_type'] = 'socket';") || !strings.Contains(string(encoded), "$cfg['Servers'][1]['socket'] = '/run/mysqld/mysqld.sock';") || !strings.Contains(string(encoded), "$cfg['Servers'][1]['SignonURL'] = '/nexa-signon-failed';") {
		t.Fatalf("phpMyAdmin config does not select the mounted socket:\n%s", encoded)
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
	root := t.TempDir()
	configRoot := filepath.Join(t.TempDir(), "config")
	operator, err := NewHostOperator(runner, HostConfig{QuadletRoot: filepath.Join(root, "quadlets"), SystemdRoot: filepath.Join(root, "systemd"), ConfigRoot: configRoot})
	if err != nil {
		t.Fatal(err)
	}
	operator.now = func() time.Time { return time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC) }
	if err := operator.prepareToolConfig(PGAdmin); err != nil {
		t.Fatal(err)
	}
	if err := operator.writePGAdminIdleUnits(); err != nil {
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

	// pgAdmin reaches Postgres over the mounted Unix socket, never TCP: the server
	// catalog points at the socket directory, and the passfile keys on "localhost"
	// (libpq's host token for a socket connection) rather than the credential's
	// unreachable host.containers.internal.
	servers, err := os.ReadFile(filepath.Join(configRoot, "pgadmin", "servers.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(servers), `"Host": "/run/postgresql"`) || strings.Contains(string(servers), "host.containers.internal") {
		t.Fatalf("pgAdmin server catalog must target the Unix socket, not TCP:\n%s", servers)
	}
	pgpass, err := os.ReadFile(filepath.Join(configRoot, "pgadmin", "pgpass"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(pgpass), "localhost:5432:app_db:app_user:") {
		t.Fatalf("pgAdmin passfile must key a socket connection on localhost:\n%s", pgpass)
	}
	firstVariable, err := pgAdminRemoteUserVariable(firstSession)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(firstConfig, "WEBSERVER_REMOTE_USER = '"+firstVariable+"'") || strings.Contains(firstConfig, firstSession) {
		t.Fatalf("first pgAdmin config is not bound to a non-plaintext session capability:\n%s", firstConfig)
	}

	// The panel serves every response with Referrer-Policy: no-referrer and tells
	// pgAdmin the request is secure via X-Scheme: https. Together those make
	// Flask-WTF's WTF_CSRF_SSL_STRICT referer check unsatisfiable — pgAdmin would
	// reject every API call with "The referrer header is missing." The generated
	// config must disable that redundant cross-check; the token CSRF and the
	// SameSite=Strict tool session cookie remain the real defenses.
	if !strings.Contains(firstConfig, "WTF_CSRF_SSL_STRICT = False") {
		t.Fatalf("pgAdmin config must disable the HTTPS referer CSRF check:\n%s", firstConfig)
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
	resets := 0
	for _, command := range runner.commands {
		if command.Name == "systemctl" && strings.Join(command.Args, " ") == "restart nexa-pgadmin-idle-stop.timer" {
			resets++
		}
	}
	if resets != 2 {
		t.Fatalf("pgAdmin idle timer resets = %d, want one per launch", resets)
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
