package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/adapters/podman"
	"github.com/nexa-panel/nexa-panel/internal/modules/admintools"
	"github.com/nexa-panel/nexa-panel/internal/modules/applications"
	"github.com/nexa-panel/nexa-panel/internal/modules/backups"
	"github.com/nexa-panel/nexa-panel/internal/modules/certificates"
	"github.com/nexa-panel/nexa-panel/internal/modules/domains"
	"github.com/nexa-panel/nexa-panel/internal/modules/files"
	"github.com/nexa-panel/nexa-panel/internal/modules/firewall"
	"github.com/nexa-panel/nexa-panel/internal/modules/logs"
	"github.com/nexa-panel/nexa-panel/internal/modules/mysql"
	"github.com/nexa-panel/nexa-panel/internal/modules/php"
	"github.com/nexa-panel/nexa-panel/internal/modules/postgres"
	"github.com/nexa-panel/nexa-panel/internal/modules/runtimes"
	"github.com/nexa-panel/nexa-panel/internal/modules/schedules"
	"github.com/nexa-panel/nexa-panel/internal/modules/services"
	sftpmodule "github.com/nexa-panel/nexa-panel/internal/modules/sftp"
	"github.com/nexa-panel/nexa-panel/internal/modules/sites"
	"github.com/nexa-panel/nexa-panel/internal/modules/system"
	"github.com/nexa-panel/nexa-panel/internal/platform/audit"
	"github.com/nexa-panel/nexa-panel/internal/platform/authorization"
	"github.com/nexa-panel/nexa-panel/internal/platform/capacity"
	"github.com/nexa-panel/nexa-panel/internal/platform/controlplane"
	"github.com/nexa-panel/nexa-panel/internal/platform/httpapi"
	"github.com/nexa-panel/nexa-panel/internal/platform/identity"
	"github.com/nexa-panel/nexa-panel/internal/platform/jobs"
	"github.com/nexa-panel/nexa-panel/internal/platform/module"
	admintooloperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/admintools"
	backupoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/backups"
	certificateoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/certificates"
	filesoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/files"
	firewalloperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/firewall"
	logsoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/logs"
	mysqloperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/mysql"
	packagesoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/packages"
	phpoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/php"
	postgresoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/postgres"
	scheduleoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/schedules"
	selfupdateoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/selfupdate"
	servicesoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/services"
	sftpoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/sftp"
	siteoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/sites"
	"github.com/nexa-panel/nexa-panel/internal/platform/persistence"
	"github.com/nexa-panel/nexa-panel/internal/platform/secrets"
	"github.com/nexa-panel/nexa-panel/internal/platform/version"
)

func runAPI(args []string, logger *slog.Logger) error {
	flags := flag.NewFlagSet("api", flag.ContinueOnError)
	address := flags.String("address", envOrDefault("NEXA_API_ADDRESS", "127.0.0.1:8888"), "HTTP listen address")
	unixSocket := flags.String("unix-socket", envOrDefault("NEXA_API_UNIX_SOCKET", ""), "Unix socket for a trusted local reverse proxy (disables TCP listener)")
	allowInsecureHTTP := flags.Bool("allow-insecure-http", envBool("NEXA_ALLOW_INSECURE_HTTP"), "serve authenticated traffic over plaintext HTTP on a non-loopback bind (unsafe; TLS should front the panel)")
	state := flags.String("state", envOrDefault("NEXA_STATE_DATABASE", "/tmp/nexa-panel/control.db"), "SQLite control-plane state path")
	masterKey := flags.String("master-key", envOrDefault("NEXA_MASTER_KEY", "/tmp/nexa-panel/master.key"), "AES master key path")
	agentSocket := flags.String("agent-socket", envOrDefault("NEXA_AGENT_SOCKET", "/tmp/nexa-panel/agent.sock"), "privileged agent Unix socket")
	agentToken := flags.String("agent-token", envOrDefault("NEXA_AGENT_TOKEN", "/tmp/nexa-panel/agent.token"), "shared agent credential path")
	bootstrapToken := flags.String("bootstrap-token", envOrDefault("NEXA_BOOTSTRAP_TOKEN", "/tmp/nexa-panel/bootstrap.token"), "first-run bootstrap token path (owner-only; gates admin creation on a public bind)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *allowInsecureHTTP {
		logger.Warn("insecure HTTP is allowed (NEXA_ALLOW_INSECURE_HTTP): authenticated traffic and the session cookie may cross the network as cleartext; front the panel with TLS instead")
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

	// Migrations run under their own generous budget rather than the tight 10s
	// module-init budget below: a pre-migration snapshot plus a large upgrade
	// must not deadline out, and neither should starve the module constructors.
	migrateCtx, cancelMigrate := context.WithTimeout(context.Background(), 2*time.Minute)
	if err := persistence.RunMigrations(migrateCtx, database, persistence.WithPreMigrationSnapshot(*state, logger)); err != nil {
		cancelMigrate()
		return fmt.Errorf("run control-plane migrations: %w", err)
	}
	cancelMigrate()

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
	// Provision the first-run bootstrap gate: while no administrator exists the
	// module writes an owner-only token file that a remote operator must present
	// to claim admin, closing the public-bind land-grab window.
	identityModule.SetBootstrapTokenPath(*bootstrapToken)
	if err := identityModule.EnsureBootstrapToken(setupCtx); err != nil {
		return fmt.Errorf("provision bootstrap token: %w", err)
	}
	jobsModule, err := jobs.New(setupCtx, database, auditModule, logger)
	if err != nil {
		return fmt.Errorf("initialize jobs module: %w", err)
	}
	jobsModule.SetSiteAccessPolicy(identityModule)
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
	phpModule, err := php.New(jobsModule, phpoperator.NewUnixClient(*agentSocket, *agentToken), sitesModule, identityModule)
	if err != nil {
		return fmt.Errorf("initialize PHP module: %w", err)
	}
	servicesModule, err := services.New(jobsModule, servicesoperator.NewUnixClient(*agentSocket, *agentToken))
	if err != nil {
		return fmt.Errorf("initialize services module: %w", err)
	}
	sftpModule, err := sftpmodule.New(setupCtx, database, sftpoperator.NewUnixClient(*agentSocket, *agentToken), sitesModule, identityModule)
	if err != nil {
		return fmt.Errorf("initialize SFTP module: %w", err)
	}
	firewallModule, err := firewall.New(jobsModule, firewalloperator.NewUnixClient(*agentSocket, *agentToken))
	if err != nil {
		return fmt.Errorf("initialize firewall module: %w", err)
	}
	backupsModule, err := backups.New(setupCtx, backups.Dependencies{
		Database: database, Jobs: jobsModule, Cipher: secretBox,
		Operator:     backupoperator.NewUnixClient(*agentSocket, *agentToken),
		Sites:        siteBackupResolver{sites: sitesModule},
		Postgres:     postgresBackupResolver{postgres: postgresDatabasesModule},
		Mysql:        mysqlBackupResolver{mysql: mysqlDatabasesModule},
		StateDBPath:  *state,
		Logger:       logger,
		AccessPolicy: identityModule,
	})
	if err != nil {
		return fmt.Errorf("initialize backups module: %w", err)
	}

	modules := []module.Module{
		auditModule,
		identityModule,
		jobsModule,
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
		phpModule,
		servicesModule,
		sftpModule,
		firewallModule,
		backupsModule,
		system.New(capacity.NewProcReader(), podman.NewInspector(), system.WithUpdates(jobsModule, selfupdateoperator.NewUnixClient(*agentSocket, *agentToken))),
	}

	app, err := controlplane.New(
		version.Version, modules, logger,
		controlplane.WithAuthentication(identityModule),
		controlplane.WithAuthorization(authorization.New()),
		controlplane.WithReadiness(apiReadiness(database, authenticatedAgentReadiness(*agentSocket, *agentToken))),
		controlplane.WithInsecureHTTPAllowed(*allowInsecureHTTP),
	)
	if err != nil {
		return fmt.Errorf("create control plane: %w", err)
	}

	// Start background execution only after the complete module graph and every
	// route have validated successfully. Constructors may prepare durable state,
	// but an invalid composition must never begin privileged work.
	app.Start(context.Background())
	defer app.Close()

	server := &http.Server{
		Addr:              *address,
		Handler:           app.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	if *unixSocket != "" {
		listener, cleanup, err := openAPIUnixSocket(*unixSocket)
		if err != nil {
			return err
		}
		defer cleanup()
		return serveHTTPListener(server, listener, logger)
	}
	if err := insecureTCPBindAllowed(*address, *allowInsecureHTTP); err != nil {
		return err
	}
	return serveHTTP(server, logger)
}

func envBool(name string) bool {
	value, err := strconv.ParseBool(strings.TrimSpace(os.Getenv(name)))
	return err == nil && value
}

// insecureTCPBindAllowed refuses a direct TCP listener on a non-loopback
// address unless insecure HTTP is explicitly allowed. Production binds a Unix
// socket behind nginx and never reaches this; it guards the direct
// `nexa api --address` path so a public bind cannot silently serve
// authentication (and the session cookie) over cleartext HTTP. The request-time
// guard in the control plane covers the socket topology.
func insecureTCPBindAllowed(address string, allowInsecure bool) error {
	if allowInsecure || bindIsLoopback(address) {
		return nil
	}
	return fmt.Errorf("refusing to bind %q: authenticated traffic would cross the network as plaintext HTTP; bind a loopback address such as 127.0.0.1, front the panel with TLS, or set NEXA_ALLOW_INSECURE_HTTP=1 to override", address)
}

// bindIsLoopback fails closed: only literal loopback IPs and the name
// "localhost" count as loopback. An empty host, a wildcard bind (0.0.0.0 or ::),
// any routable IP, or an unparseable/DNS host is treated as non-loopback so a
// publicly-resolving hostname is never mistaken for a safe local bind.
func bindIsLoopback(address string) bool {
	host, _, err := net.SplitHostPort(address)
	if err != nil || host == "" {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func serveHTTP(server *http.Server, logger *slog.Logger) error {
	listener, err := net.Listen("tcp", server.Addr)
	if err != nil {
		return err
	}
	return serveHTTPListener(server, listener, logger)
}

func serveHTTPListener(server *http.Server, listener net.Listener, logger *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if listener.Addr().Network() == "unix" {
		parentConnContext := server.ConnContext
		server.ConnContext = func(ctx context.Context, connection net.Conn) context.Context {
			if parentConnContext != nil {
				ctx = parentConnContext(ctx, connection)
			}
			return httpapi.WithTrustedProxy(ctx)
		}
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("api listening", "network", listener.Addr().Network(), "address", listener.Addr().String())
		errCh <- server.Serve(listener)
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
