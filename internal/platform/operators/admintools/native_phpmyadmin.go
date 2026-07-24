package admintools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/nexa-panel/nexa-panel/internal/platform/hostcmd"
)

const (
	phpMyAdminDocumentRoot = "/usr/share/phpmyadmin"
	phpFPMUnit             = "php8.3-fpm.service"
	phpFPMSocket           = "/run/php/php8.3-fpm.sock"

	// phpMyAdmin "configuration storage" (pmadb): a dedicated control database and
	// user let phpMyAdmin persist bookmarks, relations, column comments, SQL
	// history, the designer, and other extended features. Without it phpMyAdmin
	// shows the "configuration storage is not completely configured" warning and
	// disables those features. The control user connects over the MySQL socket
	// independently of the per-launch signon user.
	phpMyAdminMySQLSocket     = "/run/mysqld/mysqld.sock"
	phpMyAdminControlUser     = "pma"
	phpMyAdminControlDB       = "phpmyadmin"
	phpMyAdminCreateTablesSQL = phpMyAdminDocumentRoot + "/sql/create_tables.sql"
	phpMyAdminControlConfig   = "config.control.inc.php"
)

// phpMyAdminControlTables maps each configuration-storage config key to the
// table create_tables.sql installs. Kept in one place so the emitted config can
// never drift from the schema phpMyAdmin ships.
var phpMyAdminControlTables = []struct{ key, table string }{
	{"bookmarktable", "pma__bookmark"},
	{"relation", "pma__relation"},
	{"table_info", "pma__table_info"},
	{"table_coords", "pma__table_coords"},
	{"pdf_pages", "pma__pdf_pages"},
	{"column_info", "pma__column_info"},
	{"history", "pma__history"},
	{"table_uiprefs", "pma__table_uiprefs"},
	{"tracking", "pma__tracking"},
	{"userconfig", "pma__userconfig"},
	{"users", "pma__users"},
	{"usergroups", "pma__usergroups"},
	{"navigationhiding", "pma__navigationhiding"},
	{"savedsearches", "pma__savedsearches"},
	{"central_columns", "pma__central_columns"},
	{"designer_settings", "pma__designer_settings"},
	{"export_templates", "pma__export_templates"},
	{"recent", "pma__recent"},
	{"favorite", "pma__favorite"},
}

var nativePHPMyAdminPackages = []string{
	"phpmyadmin",
	"php8.3-fpm",
	"php8.3-mysql",
	"php8.3-mbstring",
	"php8.3-zip",
	"php8.3-gd",
	"php8.3-curl",
}

// deployNativePHPMyAdmin installs phpMyAdmin as a host PHP application and
// gives it a private Nginx listener. The public gateway remains the only caller
// of that listener, so callers keep the same credential-free launch interface.
func (o *HostOperator) deployNativePHPMyAdmin(ctx context.Context, tool Tool) error {
	// Upgrade an existing container deployment in place. Its generated service
	// has the same stable name as the native lifecycle unit, so stop and remove
	// the Quadlet before systemd discovers the replacement.
	quadlet := o.quadletPath(PHPMyAdmin)
	if _, err := os.Lstat(quadlet); err == nil {
		if output, stopErr := o.runner.Run(ctx, Command{Name: "systemctl", Args: []string{"stop", tool.SystemdUnit}}); stopErr != nil {
			return hostcmd.Error("stop the legacy phpMyAdmin container", output, stopErr)
		}
		if err := os.Remove(quadlet); err != nil {
			return fmt.Errorf("remove the legacy phpMyAdmin Quadlet: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect the legacy phpMyAdmin Quadlet: %w", err)
	}
	if err := o.installNativePHPMyAdmin(ctx); err != nil {
		return err
	}
	if err := os.MkdirAll(o.configRoot, 0o711); err != nil {
		return fmt.Errorf("create admin tool configuration root: %w", err)
	}
	if info, err := os.Lstat(o.configRoot); err != nil {
		return fmt.Errorf("inspect admin tool configuration root: %w", err)
	} else if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("admin tool configuration root %s must not be a symlink", o.configRoot)
	}
	if err := os.Chmod(o.configRoot, 0o711); err != nil {
		return fmt.Errorf("make admin tool configuration root traversable: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(o.configRoot, string(PHPMyAdmin)), 0o750); err != nil {
		return fmt.Errorf("create phpMyAdmin configuration directory: %w", err)
	}
	if err := o.prepareToolConfig(PHPMyAdmin); err != nil {
		return err
	}
	for _, directory := range []string{o.systemdRoot, o.nginxAvailableRoot, o.nginxEnabledRoot, o.phpMyAdminConfigRoot} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			return fmt.Errorf("create native phpMyAdmin managed directory: %w", err)
		}
	}
	if err := secureWrite(o.phpMyAdminNginxPath(), []byte(o.renderNativePHPMyAdminNginx(tool)), 0o640); err != nil {
		return fmt.Errorf("write native phpMyAdmin Nginx configuration: %w", err)
	}
	if err := secureWrite(o.phpMyAdminLoaderPath(), []byte(o.renderNativePHPMyAdminLoader()), 0o640); err != nil {
		return fmt.Errorf("write native phpMyAdmin configuration loader: %w", err)
	}
	if os.Geteuid() == 0 {
		if err := setRuntimeOwnership(o.phpMyAdminLoaderPath(), 0, runtimeUID(PHPMyAdmin), 0o640); err != nil {
			return fmt.Errorf("secure native phpMyAdmin configuration loader: %w", err)
		}
	}
	// Provision the configuration storage so extended features (bookmarks,
	// relations, SQL history, designer, …) work instead of showing the
	// "configuration storage is not completely configured" warning. Best-effort:
	// a return here means a filesystem failure, not merely MySQL being offline.
	if err := o.configureControlStorage(ctx, filepath.Join(o.configRoot, string(PHPMyAdmin))); err != nil {
		return err
	}
	if err := secureWrite(o.nativePHPMyAdminUnitPath(), []byte(o.renderNativePHPMyAdminUnit()), 0o640); err != nil {
		return fmt.Errorf("write native phpMyAdmin lifecycle unit: %w", err)
	}
	if err := o.grantPHPFPMSessionAccess(ctx); err != nil {
		return err
	}
	// Wire the lifecycle unit into boot. It is a oneshot whose ExecStop removes the
	// Nginx site symlink, so without an enable it never re-runs after a reboot and
	// phpMyAdmin silently stays offline. grantPHPFPMSessionAccess already ran
	// daemon-reload, so systemd knows the freshly written unit here.
	if output, err := o.runner.Run(ctx, Command{Name: "systemctl", Args: []string{"enable", tool.SystemdUnit}}); err != nil {
		return hostcmd.Error("enable the native phpMyAdmin lifecycle unit", output, err)
	}
	return nil
}

// grantPHPFPMSessionAccess lets php-fpm write phpMyAdmin's session files. The
// session directory lives under Nexa's config root in /etc, which php-fpm mounts
// read-only via ProtectSystem=full, so session_start() fails with EROFS no matter
// how the directory is owned. A drop-in punches a writable hole for just that
// path; php-fpm must be restarted (not merely reloaded) for the namespace change
// to take effect.
func (o *HostOperator) grantPHPFPMSessionAccess(ctx context.Context) error {
	dropInDir := filepath.Dir(o.phpFPMDropInPath())
	if err := os.MkdirAll(dropInDir, 0o755); err != nil {
		return fmt.Errorf("create php-fpm drop-in directory: %w", err)
	}
	if err := secureWrite(o.phpFPMDropInPath(), []byte(o.renderPHPFPMSessionDropIn()), 0o644); err != nil {
		return fmt.Errorf("write php-fpm session drop-in: %w", err)
	}
	if err := os.Chmod(o.phpFPMDropInPath(), 0o644); err != nil {
		return fmt.Errorf("make php-fpm session drop-in readable: %w", err)
	}
	if output, err := o.runner.Run(ctx, Command{Name: "systemctl", Args: []string{"daemon-reload"}}); err != nil {
		return hostcmd.Error("reload systemd for the php-fpm session drop-in", output, err)
	}
	if output, err := o.runner.Run(ctx, Command{Name: "systemctl", Args: []string{"restart", phpFPMUnit}}); err != nil {
		return hostcmd.Error("restart php-fpm with session write access", output, err)
	}
	return nil
}

func (o *HostOperator) renderPHPFPMSessionDropIn() string {
	// The leading '-' keeps php-fpm bootable if the session directory has not yet
	// been created (e.g. after a config reset before the next phpMyAdmin deploy).
	return strings.Join([]string{
		"[Service]",
		"ReadWritePaths=-" + o.phpMyAdminSessionRoot(),
		"",
	}, "\n")
}

func (o *HostOperator) installNativePHPMyAdmin(ctx context.Context) error {
	missing := make([]string, 0, len(nativePHPMyAdminPackages))
	for _, name := range nativePHPMyAdminPackages {
		output, err := o.runner.Run(ctx, Command{Name: "dpkg-query", Args: []string{"-W", "-f=${db:Status-Status}", name}})
		if err != nil || strings.TrimSpace(string(output)) != "installed" {
			missing = append(missing, name)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	// Suppress Debian's Apache/dbconfig prompts: Nexa owns both the Nginx route
	// and database sign-on configuration. This script is constant; no caller
	// input reaches the shell.
	const preseed = `printf '%s\n' 'phpmyadmin phpmyadmin/reconfigure-webserver multiselect' 'phpmyadmin phpmyadmin/dbconfig-install boolean false' | debconf-set-selections`
	if output, err := o.runner.Run(ctx, Command{Name: "sh", Args: []string{"-c", preseed}}); err != nil {
		return hostcmd.Error("configure non-interactive phpMyAdmin installation", output, err)
	}
	if output, err := o.runner.Run(ctx, Command{Name: "apt-get", Args: []string{"update"}}); err != nil {
		return hostcmd.Error("update the package index for phpMyAdmin", output, err)
	}
	args := append([]string{"DEBIAN_FRONTEND=noninteractive", "apt-get", "install", "-y", "--no-install-recommends"}, missing...)
	if output, err := o.runner.Run(ctx, Command{Name: "/usr/bin/env", Args: args}); err != nil {
		return hostcmd.Error("install native phpMyAdmin", output, err)
	}
	return nil
}

func (o *HostOperator) renderNativePHPMyAdminNginx(tool Tool) string {
	sessionRoot := o.phpMyAdminSessionRoot()
	return strings.Join([]string{
		"server {",
		"    listen 127.0.0.1:" + strconv.Itoa(tool.Port) + ";",
		"    server_name _;",
		"    root " + phpMyAdminDocumentRoot + ";",
		"    index index.php;",
		"    access_log off;",
		"    server_tokens off;",
		"    client_max_body_size 64m;",
		"",
		// phpMyAdmin's signon auth redirects here whenever a request carries no
		// valid signon session. Without a terminal handler the target falls back
		// through index.php and redirects to itself forever — an infinite loop for
		// any cookieless client (readiness probes) or a browser whose session
		// expired. Answer it with a static page so the chain always terminates.
		"    location = /nexa-signon-failed {",
		"        default_type text/html;",
		"        return 200 \"<!doctype html><title>phpMyAdmin session</title><p>This phpMyAdmin session is not active. Relaunch it from Nexa.</p>\";",
		"    }",
		"",
		"    location / {",
		"        try_files $uri $uri/ /index.php?$query_string;",
		"    }",
		"",
		"    location ~ \\.php$ {",
		"        try_files $uri =404;",
		"        include fastcgi_params;",
		"        fastcgi_param SCRIPT_FILENAME $document_root$fastcgi_script_name;",
		"        fastcgi_param HTTP_PROXY \"\";",
		"        fastcgi_param PHP_ADMIN_VALUE \"session.save_path=" + sessionRoot + "\";",
		"        fastcgi_pass unix:" + phpFPMSocket + ";",
		"    }",
		"",
		"    location ~ ^/(setup|libraries|templates)/ { deny all; }",
		"    location ~ /\\.(?!well-known) { deny all; }",
		"}",
		"",
	}, "\n")
}

func (o *HostOperator) renderNativePHPMyAdminLoader() string {
	root := filepath.Join(o.configRoot, string(PHPMyAdmin))
	control := filepath.Join(root, phpMyAdminControlConfig)
	// The control-storage file is only present once the pmadb has been provisioned
	// (see configureControlStorage). Guard the require so phpMyAdmin still boots —
	// without extended features — when MySQL was unreachable at deploy time.
	return "<?php\n" +
		"require " + phpSingleQuoted(filepath.Join(root, "config.user.inc.php")) + ";\n" +
		"require " + phpSingleQuoted(filepath.Join(root, "config.secret.inc.php")) + ";\n" +
		"if (is_readable(" + phpSingleQuoted(control) + ")) { require " + phpSingleQuoted(control) + "; }\n"
}

// phpMyAdminSQLScript builds the shell command that feeds a SQL file to the
// local database over its socket as root. It resolves the client at run time —
// MariaDB 11+ no longer ships the `mysql` symlink, so it falls back to `mariadb`
// (both accept the same flags and default socket on Debian/Ubuntu), matching the
// MySQL operator's own binary selection. The runner has no stdin, hence the shell
// redirect. Only fixed, server-owned paths are interpolated; no caller input.
func phpMyAdminSQLScript(sqlFile string) string {
	return fmt.Sprintf(
		`client="$(command -v mysql || command -v mariadb)" && "$client" --protocol=socket --socket=%s --user=root < %s`,
		phpMyAdminMySQLSocket, sqlFile,
	)
}

// configureControlStorage provisions phpMyAdmin's configuration storage on the
// local MySQL server and writes the matching control-user config. It is
// best-effort: if MySQL is unreachable (e.g. phpMyAdmin deployed before the
// database engine is online) it removes any stale control config and returns nil
// so the deploy still succeeds — a later re-deploy enables the feature. The
// control password lives only in the 0640 config file and a short-lived 0600 SQL
// script, never in a process argument list.
func (o *HostOperator) configureControlStorage(ctx context.Context, root string) error {
	controlConfigPath := filepath.Join(root, phpMyAdminControlConfig)
	skip := func() error {
		if err := os.Remove(controlConfigPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("clear stale phpMyAdmin control storage config: %w", err)
		}
		return nil
	}
	if _, err := o.runner.Run(ctx, Command{Name: "sh", Args: []string{"-c", phpMyAdminSQLScript(phpMyAdminCreateTablesSQL)}}); err != nil {
		return skip()
	}
	password := randomID() + randomID()
	grant := fmt.Sprintf(
		"CREATE USER IF NOT EXISTS '%[1]s'@'localhost' IDENTIFIED BY '%[2]s';\n"+
			"ALTER USER '%[1]s'@'localhost' IDENTIFIED BY '%[2]s';\n"+
			"GRANT SELECT, INSERT, UPDATE, DELETE ON `%[3]s`.* TO '%[1]s'@'localhost';\n",
		phpMyAdminControlUser, password, phpMyAdminControlDB,
	)
	grantPath := filepath.Join(root, ".pma-control-grant.sql")
	if err := secureWrite(grantPath, []byte(grant), 0o600); err != nil {
		return fmt.Errorf("stage phpMyAdmin control user grant: %w", err)
	}
	defer func() { _ = os.Remove(grantPath) }()
	if _, err := o.runner.Run(ctx, Command{Name: "sh", Args: []string{"-c", phpMyAdminSQLScript(grantPath)}}); err != nil {
		return skip()
	}
	if err := secureWrite(controlConfigPath, []byte(o.renderControlStorageConfig(password)), 0o640); err != nil {
		return fmt.Errorf("write phpMyAdmin control storage config: %w", err)
	}
	if os.Geteuid() == 0 {
		if err := setRuntimeOwnership(controlConfigPath, 0, runtimeUID(PHPMyAdmin), 0o640); err != nil {
			return fmt.Errorf("secure phpMyAdmin control storage config: %w", err)
		}
	}
	return nil
}

func (o *HostOperator) renderControlStorageConfig(password string) string {
	lines := []string{
		"<?php",
		"$cfg['Servers'][1]['controluser'] = " + phpSingleQuoted(phpMyAdminControlUser) + ";",
		"$cfg['Servers'][1]['controlpass'] = " + phpSingleQuoted(password) + ";",
		"$cfg['Servers'][1]['pmadb'] = " + phpSingleQuoted(phpMyAdminControlDB) + ";",
	}
	for _, entry := range phpMyAdminControlTables {
		lines = append(lines, "$cfg['Servers'][1]["+phpSingleQuoted(entry.key)+"] = "+phpSingleQuoted(entry.table)+";")
	}
	return strings.Join(append(lines, ""), "\n")
}

func (o *HostOperator) renderNativePHPMyAdminUnit() string {
	available := o.phpMyAdminNginxPath()
	enabled := o.phpMyAdminEnabledPath()
	return strings.Join([]string{
		"[Unit]",
		"Description=Nexa native phpMyAdmin gateway",
		"Requires=nginx.service " + phpFPMUnit,
		"After=nginx.service " + phpFPMUnit,
		"",
		"[Service]",
		"Type=oneshot",
		"RemainAfterExit=yes",
		"ExecStart=/usr/bin/ln -sfn " + available + " " + enabled,
		"ExecStart=/usr/sbin/nginx -t",
		"ExecStart=/usr/sbin/nginx -s reload",
		"ExecStop=/usr/bin/rm -f " + enabled,
		"ExecStop=/usr/sbin/nginx -s reload",
		"NoNewPrivileges=yes",
		"PrivateTmp=yes",
		"",
		"[Install]",
		"WantedBy=multi-user.target",
		"",
	}, "\n")
}

func phpSingleQuoted(value string) string {
	return "'" + strings.NewReplacer("\\", "\\\\", "'", "\\'").Replace(value) + "'"
}

func (o *HostOperator) phpMyAdminNginxPath() string {
	return filepath.Join(o.nginxAvailableRoot, "nexa-phpmyadmin.conf")
}

func (o *HostOperator) phpMyAdminEnabledPath() string {
	return filepath.Join(o.nginxEnabledRoot, "nexa-phpmyadmin.conf")
}

func (o *HostOperator) phpMyAdminLoaderPath() string {
	return filepath.Join(o.phpMyAdminConfigRoot, "nexa-panel.php")
}

func (o *HostOperator) nativePHPMyAdminUnitPath() string {
	return filepath.Join(o.systemdRoot, "nexa-phpmyadmin.service")
}

func (o *HostOperator) phpMyAdminSessionRoot() string {
	return filepath.Join(o.configRoot, string(PHPMyAdmin), "sessions")
}

func (o *HostOperator) phpFPMDropInPath() string {
	return filepath.Join(o.systemdRoot, phpFPMUnit+".d", "nexa-phpmyadmin.conf")
}
