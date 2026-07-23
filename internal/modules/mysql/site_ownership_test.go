package mysql

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/platform/audit"
	"github.com/nexa-panel/nexa-panel/internal/platform/jobs"
	mysqloperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/mysql"
	"github.com/nexa-panel/nexa-panel/internal/platform/persistence"
	"github.com/nexa-panel/nexa-panel/internal/platform/secrets"
)

// newOwnershipModule builds a module over a real control database with an
// online engine and an active account already in place, so the tests below can
// go straight at CreateDatabase without driving a credential rotation first.
func newOwnershipModule(t *testing.T) (*Module, string) {
	t.Helper()
	ctx := context.Background()
	database, err := persistence.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := persistence.RunMigrations(ctx, database); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	auditLog, err := audit.New(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	queue, err := jobs.NewWithConfig(ctx, database, auditLog, slog.New(slog.NewTextHandler(io.Discard, nil)), jobs.Config{PollInterval: 5 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := secrets.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	operator := &fakeMySQLOperator{engine: mysqloperator.Engine{ID: "mysql", Kind: mysqloperator.EngineMySQL, Version: "8.4.5", VersionText: "8.4.5", Port: 3306, Status: "online", SocketPath: "/run/mysqld/mysqld.sock", SystemdUnit: "mysql.service"}}
	module, err := New(ctx, database, queue, cipher, operator)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := module.SyncEngines(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, err := database.ExecContext(ctx, `
		INSERT INTO mysql_accounts (id, engine_id, name, host, status, created_at, updated_at)
		VALUES ('account_1','mysql','app','localhost',?,?,?)`, string(StatusActive), now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `
		INSERT INTO sites (id, slug, display_name, primary_domain, php_version, unix_user, root_path, socket_path, status, deployment_mode, created_at, updated_at)
		VALUES ('site_1','shop','Shop','shop.example.com','8.4','nexa_shop','/srv/nexa/sites/shop','/run/php/nexa-shop.sock','active','standard',?,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	return module, "account_1"
}

// A database created for a site records the site, whatever it is called: the
// name is no longer what ties the two together.
func TestCreateDatabaseRecordsTheOwningSite(t *testing.T) {
	ctx := context.Background()
	module, account := newOwnershipModule(t)
	created, _, err := module.CreateDatabase(ctx, CreateDatabaseRequest{EngineID: "mysql", Name: "analytics_prod", OwnerAccountID: account, SiteID: "site_1"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if created.SiteID != "site_1" {
		t.Fatalf("siteId = %q, want the owning site recorded", created.SiteID)
	}
	stored, err := module.getDatabaseModel(ctx, created.ID)
	if err != nil || stored.SiteID == nil || *stored.SiteID != "site_1" {
		t.Fatalf("stored site_id = %v, err %v", stored.SiteID, err)
	}
}

func TestCreateDatabaseRefusesAnUnknownSite(t *testing.T) {
	ctx := context.Background()
	module, account := newOwnershipModule(t)
	if _, _, err := module.CreateDatabase(ctx, CreateDatabaseRequest{EngineID: "mysql", Name: "orphan_db", OwnerAccountID: account, SiteID: "site_missing"}, nil); err == nil {
		t.Fatal("a database was accepted for a site that does not exist")
	}
	databases, err := module.ListDatabases(ctx, "mysql")
	if err != nil || len(databases) != 0 {
		t.Fatalf("databases = %+v, err %v, want the refused row not persisted", databases, err)
	}
}

// A database with no site is still legitimate: not every managed database
// belongs to a site, and those must keep working exactly as before.
func TestCreateDatabaseWithoutASiteStoresNoOwner(t *testing.T) {
	ctx := context.Background()
	module, account := newOwnershipModule(t)
	created, _, err := module.CreateDatabase(ctx, CreateDatabaseRequest{EngineID: "mysql", Name: "standalone_db", OwnerAccountID: account}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if created.SiteID != "" {
		t.Fatalf("siteId = %q, want no owner", created.SiteID)
	}
}
