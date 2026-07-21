package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/nexa-panel/nexa-panel/internal/platform/agent"
	"github.com/nexa-panel/nexa-panel/internal/platform/agentauth"
	admintooloperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/admintools"
	backupoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/backups"
	certificateoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/certificates"
	filesoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/files"
	logsoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/logs"
	mysqloperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/mysql"
	packagesoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/packages"
	phpoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/php"
	postgresoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/postgres"
	scheduleoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/schedules"
	selfupdateoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/selfupdate"
	servicesoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/services"
	sftpoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/sftp"
	"github.com/nexa-panel/nexa-panel/internal/platform/operators/sitefs"
	siteoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/sites"
	"github.com/nexa-panel/nexa-panel/internal/platform/version"
)

func runAgent(args []string, logger *slog.Logger) error {
	flags := flag.NewFlagSet("agent", flag.ContinueOnError)
	socket := flags.String("socket", envOrDefault("NEXA_AGENT_SOCKET", "/tmp/nexa-panel/agent.sock"), "Unix socket path")
	tokenPath := flags.String("token", envOrDefault("NEXA_AGENT_TOKEN", "/tmp/nexa-panel/agent.token"), "shared agent credential path")
	if err := flags.Parse(args); err != nil {
		return err
	}
	token, err := agentauth.OpenOrCreate(*tokenPath)
	if err != nil {
		return fmt.Errorf("open agent credential: %w", err)
	}
	siteOperator, err := siteoperator.NewHostOperator(siteoperator.Renderer{}, "/etc/nginx/sites-enabled", siteoperator.NewHostSystem())
	if err != nil {
		return fmt.Errorf("create site operator: %w", err)
	}
	certificateOperator, err := certificateoperator.NewHostOperator(nil, "/srv/nexa/acme", "/etc/letsencrypt/live")
	if err != nil {
		return fmt.Errorf("create certificate operator: %w", err)
	}
	postgresOperator, err := postgresoperator.NewHostOperator(nil, postgresoperator.HostConfig{})
	if err != nil {
		return fmt.Errorf("create PostgreSQL operator: %w", err)
	}
	mysqlOperator, err := mysqloperator.NewHostOperator(nil, mysqloperator.HostConfig{})
	if err != nil {
		return fmt.Errorf("create MySQL-family operator: %w", err)
	}
	adminToolOperator, err := admintooloperator.NewHostOperator(nil, admintooloperator.HostConfig{})
	if err != nil {
		return fmt.Errorf("create admin tool operator: %w", err)
	}
	packagesOperator, err := packagesoperator.NewHostOperator(nil)
	if err != nil {
		return fmt.Errorf("create packages operator: %w", err)
	}
	phpOperator, err := phpoperator.NewHostOperator(nil, phpoperator.HostConfig{})
	if err != nil {
		return fmt.Errorf("create PHP operator: %w", err)
	}
	filesOperator, err := filesoperator.NewHostOperator("/srv/nexa/sites", sitefs.HostOwnership{})
	if err != nil {
		return fmt.Errorf("create files operator: %w", err)
	}
	logsOperator, err := logsoperator.NewHostOperator("/srv/nexa/sites")
	if err != nil {
		return fmt.Errorf("create logs operator: %w", err)
	}
	scheduleOperator, err := scheduleoperator.NewHostOperator(scheduleoperator.HostConfig{}, scheduleoperator.HostOwnership{}, nil)
	if err != nil {
		return fmt.Errorf("create schedules operator: %w", err)
	}
	backupOperator, err := backupoperator.NewHostOperator(nil)
	if err != nil {
		return fmt.Errorf("create backups operator: %w", err)
	}
	servicesOperator, err := servicesoperator.NewHostOperator(nil)
	if err != nil {
		return fmt.Errorf("create services operator: %w", err)
	}
	sftpOperator, err := sftpoperator.NewHostOperator(nil)
	if err != nil {
		return fmt.Errorf("create SFTP operator: %w", err)
	}
	selfUpdateOperator, err := selfupdateoperator.NewHostOperator(selfupdateoperator.HostConfig{
		InstalledVersion: version.Version,
		// The swap target is /usr/bin/nexa on a real host; NEXA_SELFUPDATE_BINARY
		// overrides it for environments where the live binary is not directly
		// rewritable (e.g. the bind-mounted test node in compose.yaml).
		BinaryPath: os.Getenv("NEXA_SELFUPDATE_BINARY"),
	})
	if err != nil {
		return fmt.Errorf("create self-update operator: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	server := agent.New(*socket, version.Version, token, logger, agent.WithSiteOperator(siteOperator), agent.WithCertificateOperator(certificateOperator), agent.WithPostgresOperator(postgresOperator), agent.WithMySQLOperator(mysqlOperator), agent.WithAdminToolOperator(adminToolOperator), agent.WithPackagesOperator(packagesOperator), agent.WithPHPOperator(phpOperator), agent.WithFilesOperator(filesOperator), agent.WithLogsOperator(logsOperator), agent.WithScheduleOperator(scheduleOperator), agent.WithBackupOperator(backupOperator), agent.WithServicesOperator(servicesOperator), agent.WithSFTPOperator(sftpOperator), agent.WithSelfUpdateOperator(selfUpdateOperator))
	return server.Serve(ctx)
}
