package main

import (
	mysqloperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/mysql"

	"flag"
	postgresoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/postgres"
	"github.com/nexa-panel/nexa-panel/internal/platform/secrets"

	"os/signal"
	"syscall"

	"github.com/nexa-panel/nexa-panel/internal/modules/postgres"

	"github.com/nexa-panel/nexa-panel/internal/modules/runtimes"
	"github.com/nexa-panel/nexa-panel/internal/modules/sites"

	"context"
	"github.com/nexa-panel/nexa-panel/internal/platform/capacity"

	"fmt"

	"net/http"

	admintooloperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/admintools"

	"github.com/nexa-panel/nexa-panel/internal/modules/certificates"
	"github.com/nexa-panel/nexa-panel/internal/platform/nodeoperations"

	"github.com/nexa-panel/nexa-panel/internal/platform/persistence"

	"github.com/nexa-panel/nexa-panel/internal/platform/identity"
	siteoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/sites"
	"log/slog"

	"github.com/nexa-panel/nexa-panel/internal/modules/mysql"

	"github.com/nexa-panel/nexa-panel/internal/modules/system"
	"os"

	certificateoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/certificates"

	"errors"
	nodeoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/nodes"

	"github.com/nexa-panel/nexa-panel/internal/adapters/podman"
	"github.com/nexa-panel/nexa-panel/internal/modules/admintools"
	"github.com/nexa-panel/nexa-panel/internal/modules/applications"
	"github.com/nexa-panel/nexa-panel/internal/modules/backups"
	backupoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/backups"
	packagesoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/packages"

	"github.com/nexa-panel/nexa-panel/internal/modules/files"
	filesoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/files"

	"github.com/nexa-panel/nexa-panel/internal/modules/logs"
	logsoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/logs"

	"github.com/nexa-panel/nexa-panel/internal/modules/schedules"
	scheduleoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/schedules"

	"github.com/nexa-panel/nexa-panel/internal/modules/domains"

	"github.com/nexa-panel/nexa-panel/internal/platform/authorization"

	"github.com/nexa-panel/nexa-panel/internal/platform/controlplane"
	"github.com/nexa-panel/nexa-panel/internal/platform/version"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/platform/audit"

	"github.com/nexa-panel/nexa-panel/internal/platform/jobs"
	"github.com/nexa-panel/nexa-panel/internal/platform/module"
)

func runAPI(args []string, logger *slog.Logger) error {
	flags := flag.NewFlagSet("api", flag.ContinueOnError)
	address := flags.String("address", envOrDefault("NEXA_API_ADDRESS", "127.0.0.1:8080"), "HTTP listen address")
	state := flags.String("state", envOrDefault("NEXA_STATE_DATABASE", "/tmp/nexa-panel/control.db"), "SQLite control-plane state path")
	masterKey := flags.String("master-key", envOrDefault("NEXA_MASTER_KEY", "/tmp/nexa-panel/master.key"), "AES master key path")
	agentSocket := flags.String("agent-socket", envOrDefault("NEXA_AGENT_SOCKET", "/tmp/nexa-panel/agent.sock"), "privileged agent Unix socket")
	agentToken := flags.String("agent-token", envOrDefault("NEXA_AGENT_TOKEN", "/tmp/nexa-panel/agent.token"), "shared agent credential path")
	if err := flags.Parse(args); err != nil {
		return err
	}

	database, err := persistence.Open(*state)
	if err != nil {
		return fmt.Errorf("open control-plane state: %w", err)
	}
	defer database.Close()
	secretBox, err := secrets.OpenKeyFile(*masterKey)
	if err != nil {
		return fmt.Errorf("open control-plane master key: %w", err)
	}

	setupCtx, cancelSetup := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelSetup()
	auditModule, err := audit.New(setupCtx, database)
	if err != nil {
		return fmt.Errorf("initialize audit module: %w", err)
	}
	identityModule, err := identity.New(setupCtx, database, auditModule, secretBox, logger)
	if err != nil {
		return fmt.Errorf("initialize identity module: %w", err)
	}
	jobsModule, err := jobs.New(setupCtx, database, auditModule, logger)
	if err != nil {
		return fmt.Errorf("initialize jobs module: %w", err)
	}
	nodeOperationsModule, err := nodeoperations.New(nodeoperator.NewUnixClient(*agentSocket, *agentToken), jobsModule)
	if err != nil {
		return fmt.Errorf("initialize node operations module: %w", err)
	}
	runtimesModule, err := runtimes.New(runtimes.FilesystemDiscoverer{PHPConfigRoot: "/etc/php"})
	if err != nil {
		return fmt.Errorf("initialize runtimes module: %w", err)
	}
	sitesModule, err := sites.New(setupCtx, database, jobsModule, runtimesModule, siteoperator.NewUnixClient(*agentSocket, *agentToken))
	if err != nil {
		return fmt.Errorf("initialize sites module: %w", err)
	}
	identityModule.SetSiteDirectory(sitesModule)
	sitesModule.SetAccessPolicy(identityModule)
	domainsModule, err := domains.New(setupCtx, database, jobsModule, sitesModule, siteoperator.NewUnixClient(*agentSocket, *agentToken), nil)
	if err != nil {
		return fmt.Errorf("initialize domains module: %w", err)
	}
	certificatesModule, err := certificates.New(setupCtx, database, jobsModule, sitesModule, domainsModule, certificateoperator.NewUnixClient(*agentSocket, *agentToken), siteoperator.NewUnixClient(*agentSocket, *agentToken), nil)
	if err != nil {
		return fmt.Errorf("initialize certificates module: %w", err)
	}
	domainsModule.SetTLSProvider(certificatesModule)
	postgresDatabasesModule, err := postgres.New(setupCtx, database, jobsModule, secretBox, postgresoperator.NewUnixClient(*agentSocket, *agentToken))
	if err != nil {
		return fmt.Errorf("initialize PostgreSQL databases module: %w", err)
	}
	mysqlDatabasesModule, err := mysql.New(setupCtx, database, jobsModule, secretBox, mysqloperator.NewUnixClient(*agentSocket, *agentToken))
	if err != nil {
		return fmt.Errorf("initialize MySQL-family databases module: %w", err)
	}
	credentialResolver := func(ctx context.Context, engine, databaseID, accountID string) (admintools.Credential, error) {
		switch engine {
		case "postgresql":
			credential, resolveErr := postgresDatabasesModule.ResolveAdminToolCredential(ctx, databaseID, accountID)
			return admintools.Credential{Host: credential.Host, Port: credential.Port, Database: credential.Database, Username: credential.Username, Secret: credential.Secret}, resolveErr
		case "mysql":
			credential, resolveErr := mysqlDatabasesModule.ResolveAdminToolCredential(ctx, databaseID, accountID)
			return admintools.Credential{Host: credential.Host, Port: credential.Port, Database: credential.Database, Username: credential.Username, Secret: credential.Secret}, resolveErr
		default:
			return admintools.Credential{}, errors.New("database engine is unsupported for admin tool launch")
		}
	}
	adminToolsModule, err := admintools.New(setupCtx, database, jobsModule, admintooloperator.NewUnixClient(*agentSocket, *agentToken), admintools.WithLaunchGateway(secretBox, credentialResolver, auditModule))
	if err != nil {
		return fmt.Errorf("initialize admin tools module: %w", err)
	}
	filesModule, err := files.New(jobsModule, sitesModule, identityModule, filesoperator.NewUnixClient(*agentSocket, *agentToken), auditModule)
	if err != nil {
		return fmt.Errorf("initialize files module: %w", err)
	}
	logsModule, err := logs.New(sitesModule, identityModule, logsoperator.NewUnixClient(*agentSocket, *agentToken))
	if err != nil {
		return fmt.Errorf("initialize logs module: %w", err)
	}
	schedulesModule, err := schedules.New(setupCtx, database, jobsModule, sitesModule, identityModule, scheduleoperator.NewUnixClient(*agentSocket, *agentToken))
	if err != nil {
		return fmt.Errorf("initialize schedules module: %w", err)
	}
	applicationsModule, err := applications.New(setupCtx, database, jobsModule, packagesoperator.NewUnixClient(*agentSocket, *agentToken), adminToolsModule)
	if err != nil {
		return fmt.Errorf("initialize applications module: %w", err)
	}
	backupsModule, err := backups.New(setupCtx, backups.Dependencies{
		Database: database, Jobs: jobsModule, Cipher: secretBox,
		Operator:    backupoperator.NewUnixClient(*agentSocket, *agentToken),
		Sites:       siteBackupResolver{sites: sitesModule},
		Postgres:    postgresBackupResolver{postgres: postgresDatabasesModule},
		Mysql:       mysqlBackupResolver{mysql: mysqlDatabasesModule},
		StateDBPath: *state,
		Logger:      logger,
	})
	if err != nil {
		return fmt.Errorf("initialize backups module: %w", err)
	}

	jobsModule.Start(context.Background())
	defer jobsModule.Close()

	modules := []module.Module{
		auditModule,
		identityModule,
		jobsModule,
		nodeOperationsModule,
		runtimesModule,
		sitesModule,
		domainsModule,
		certificatesModule,
		postgresDatabasesModule,
		mysqlDatabasesModule,
		adminToolsModule,
		filesModule,
		logsModule,
		schedulesModule,
		applicationsModule,
		backupsModule,
		system.New(capacity.NewProcReader(), podman.NewInspector()),
	}

	app, err := controlplane.New(version.Version, modules, logger,
		controlplane.WithAuthentication(identityModule), controlplane.WithAuthorization(authorization.New()))
	if err != nil {
		return fmt.Errorf("create control plane: %w", err)
	}

	server := &http.Server{
		Addr:              *address,
		Handler:           app.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	return serveHTTP(server, logger)
}

func serveHTTP(server *http.Server, logger *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		logger.Info("api listening", "address", server.Addr)
		errCh <- server.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	}
}

// --- backups resolver adapters ---------------------------------------------
// The backups module reads site and database details through narrow interfaces
// so it need not import those modules. These composition-root adapters bridge
// each module's own getter to the operator's target types.

type siteBackupResolver struct{ sites *sites.Module }

func (r siteBackupResolver) BackupSite(ctx context.Context, id string) (backupoperator.SiteTarget, error) {
	site, err := r.sites.Get(ctx, id)
	if err != nil {
		return backupoperator.SiteTarget{}, err
	}
	return backupoperator.SiteTarget{Slug: site.Slug, RootPath: site.RootPath, UnixUser: site.UnixUser}, nil
}

type postgresBackupResolver struct{ postgres *postgres.Module }

func (r postgresBackupResolver) BackupDatabase(ctx context.Context, id string) (backupoperator.DatabaseTarget, error) {
	name, version, port, socket, err := r.postgres.BackupTarget(ctx, id)
	if err != nil {
		return backupoperator.DatabaseTarget{}, err
	}
	return backupoperator.DatabaseTarget{Engine: "postgres", Name: name, Version: version, Port: port, Socket: socket}, nil
}

type mysqlBackupResolver struct{ mysql *mysql.Module }

func (r mysqlBackupResolver) BackupDatabase(ctx context.Context, id string) (backupoperator.DatabaseTarget, error) {
	name, kind, socket, err := r.mysql.BackupTarget(ctx, id)
	if err != nil {
		return backupoperator.DatabaseTarget{}, err
	}
	return backupoperator.DatabaseTarget{Engine: kind, Name: name, Socket: socket}, nil
}
