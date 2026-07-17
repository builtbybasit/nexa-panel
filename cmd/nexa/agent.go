package main

import (
	siteoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/sites"
	"log/slog"

	"github.com/nexa-panel/nexa-panel/internal/platform/agentauth"
	"os"

	"github.com/nexa-panel/nexa-panel/internal/platform/agent"

	certificateoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/certificates"

	nodeoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/nodes"

	mysqloperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/mysql"
	"github.com/nexa-panel/nexa-panel/internal/platform/version"

	"flag"
	postgresoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/postgres"

	"context"
	"os/signal"
	"syscall"

	"fmt"

	admintooloperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/admintools"

	packagesoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/packages"

	filesoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/files"
	logsoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/logs"
	scheduleoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/schedules"
	"github.com/nexa-panel/nexa-panel/internal/platform/operators/sitefs"
)

func runAgent(args []string, logger *slog.Logger) error {
	flags := flag.NewFlagSet("agent", flag.ContinueOnError)
	socket := flags.String("socket", envOrDefault("NEXA_AGENT_SOCKET", "/tmp/nexa-panel/agent.sock"), "Unix socket path")
	tokenPath := flags.String("token", envOrDefault("NEXA_AGENT_TOKEN", "/tmp/nexa-panel/agent.token"), "shared agent credential path")
	probePath := flags.String("probe-path", envOrDefault("NEXA_AGENT_PROBE_PATH", "/tmp/nexa-panel/probe.conf"), "fixed managed probe path")
	if err := flags.Parse(args); err != nil {
		return err
	}
	token, err := agentauth.OpenOrCreate(*tokenPath)
	if err != nil {
		return fmt.Errorf("open agent credential: %w", err)
	}
	operator, err := nodeoperator.NewFileOperator(*probePath)
	if err != nil {
		return fmt.Errorf("create node operator: %w", err)
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

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	server := agent.New(*socket, version.Version, token, operator, logger, agent.WithSiteOperator(siteOperator), agent.WithCertificateOperator(certificateOperator), agent.WithPostgresOperator(postgresOperator), agent.WithMySQLOperator(mysqlOperator), agent.WithAdminToolOperator(adminToolOperator), agent.WithPackagesOperator(packagesOperator), agent.WithFilesOperator(filesOperator), agent.WithLogsOperator(logsOperator), agent.WithScheduleOperator(scheduleOperator))
	return server.Serve(ctx)
}
