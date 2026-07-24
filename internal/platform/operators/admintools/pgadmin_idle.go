package admintools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	pgAdminIdleTimerUnit   = "nexa-pgadmin-idle-stop.timer"
	pgAdminIdleServiceUnit = "nexa-pgadmin-idle-stop.service"
)

func (o *HostOperator) writePGAdminIdleUnits() error {
	if err := os.MkdirAll(o.systemdRoot, 0o755); err != nil {
		return fmt.Errorf("create systemd unit directory: %w", err)
	}
	service := strings.Join([]string{
		"[Unit]",
		"Description=Stop idle Nexa pgAdmin",
		"",
		"[Service]",
		"Type=oneshot",
		"ExecStart=/bin/systemctl stop nexa-pgadmin.service",
		"ExecStart=/usr/bin/truncate --size=0 " + filepath.Join(o.configRoot, string(PGAdmin), "pgpass"),
		"ExecStart=/usr/bin/truncate --size=0 " + filepath.Join(o.configRoot, string(PGAdmin), "config_local.py"),
		"NoNewPrivileges=yes",
		"PrivateTmp=yes",
		"ProtectSystem=strict",
		"ReadWritePaths=" + filepath.Join(o.configRoot, string(PGAdmin)),
		"",
	}, "\n")
	timer := strings.Join([]string{
		"[Unit]",
		"Description=Stop Nexa pgAdmin after its session window",
		"",
		"[Timer]",
		"OnActiveSec=15min",
		"AccuracySec=30s",
		"Unit=" + pgAdminIdleServiceUnit,
		"",
	}, "\n")
	if err := secureWrite(o.pgAdminIdleServicePath(), []byte(service), 0o640); err != nil {
		return fmt.Errorf("write pgAdmin idle-stop service: %w", err)
	}
	if err := secureWrite(o.pgAdminIdleTimerPath(), []byte(timer), 0o640); err != nil {
		return fmt.Errorf("write pgAdmin idle-stop timer: %w", err)
	}
	return nil
}

// scrubPGAdminLaunchState removes the password and trusted-header mapping once
// the tool stops. A later launch rewrites both immediately before starting it.
func (o *HostOperator) scrubPGAdminLaunchState() error {
	files := []struct {
		path string
		mode os.FileMode
		uid  int
		gid  int
	}{
		{path: filepath.Join(o.configRoot, string(PGAdmin), "pgpass"), mode: 0o600, uid: 5050, gid: 5050},
		{path: filepath.Join(o.configRoot, string(PGAdmin), "config_local.py"), mode: 0o640, uid: 0, gid: 5050},
	}
	for _, file := range files {
		if _, err := os.Lstat(file.path); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return fmt.Errorf("inspect pgAdmin launch state: %w", err)
		}
		if err := secureWrite(file.path, nil, file.mode); err != nil {
			return fmt.Errorf("clear pgAdmin launch state: %w", err)
		}
		if os.Geteuid() == 0 {
			if err := setRuntimeOwnership(file.path, file.uid, file.gid, file.mode); err != nil {
				return fmt.Errorf("secure cleared pgAdmin launch state: %w", err)
			}
		}
	}
	return nil
}

// KeepAlive re-arms the idle-stop timer so an actively proxied tool is not
// stopped on a countdown that began at its launch. The panel calls it as it
// slides a live tool session forward, keeping the container and the panel
// session aligned so they expire together on real inactivity.
func (o *HostOperator) KeepAlive(ctx context.Context, kind Kind) error {
	if kind != PGAdmin {
		return nil
	}
	return o.schedulePGAdminStop(ctx)
}

func (o *HostOperator) schedulePGAdminStop(ctx context.Context) error {
	if _, err := os.Stat(o.pgAdminIdleTimerPath()); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if output, err := o.runner.Run(ctx, Command{Name: "systemctl", Args: []string{"restart", pgAdminIdleTimerUnit}}); err != nil {
		return commandError("schedule pgAdmin idle stop", output, err)
	}
	return nil
}

func (o *HostOperator) stopPGAdminTimer(ctx context.Context) error {
	if _, err := os.Stat(o.pgAdminIdleTimerPath()); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if output, err := o.runner.Run(ctx, Command{Name: "systemctl", Args: []string{"stop", pgAdminIdleTimerUnit}}); err != nil {
		return commandError("stop pgAdmin idle timer", output, err)
	}
	return nil
}

func (o *HostOperator) pgAdminIdleServicePath() string {
	return filepath.Join(o.systemdRoot, pgAdminIdleServiceUnit)
}

func (o *HostOperator) pgAdminIdleTimerPath() string {
	return filepath.Join(o.systemdRoot, pgAdminIdleTimerUnit)
}
