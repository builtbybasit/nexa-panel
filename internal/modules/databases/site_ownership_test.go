package databases

import (
	"context"
	"testing"
	"time"
)

// ownershipHarness seeds a site and an active user so tests can go straight at
// CreateDatabase. The site-ownership logic is shared core, so one engine
// (PostgreSQL) stands in for both.
func ownershipHarness(t *testing.T) (*testHarness, string) {
	t.Helper()
	h := newTestModule(t)
	h.start(t)
	h.seedUser(t, "postgresql", "user_1", "app")
	now := time.Now().UTC()
	if _, err := h.module.database.ExecContext(context.Background(), `
		INSERT INTO sites (id, slug, display_name, primary_domain, php_version, unix_user, root_path, socket_path, status, deployment_mode, created_at, updated_at)
		VALUES ('site_1','shop','Shop','shop.example.com','8.4','nexa_shop','/srv/nexa/sites/shop','/run/php/nexa-shop.sock','active','standard',?,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	return h, h.serverID("postgresql")
}

// A database created for a site records the site, whatever it is called: the
// name is no longer what ties the two together.
func TestCreateDatabaseRecordsTheOwningSite(t *testing.T) {
	ctx := context.Background()
	h, server := ownershipHarness(t)
	created, _, err := h.module.CreateDatabase(ctx, CreateDatabaseRequest{ServerID: server, Name: "analytics_prod", OwnerUserID: "user_1", SiteID: "site_1"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if created.SiteID != "site_1" {
		t.Fatalf("siteId = %q, want the owning site recorded", created.SiteID)
	}
	stored, err := h.engine(t, "postgresql").store.GetDatabase(ctx, created.ID)
	if err != nil || stored.SiteID == nil || *stored.SiteID != "site_1" {
		t.Fatalf("stored site_id = %v, err %v", stored.SiteID, err)
	}
}

func TestCreateDatabaseRefusesAnUnknownSite(t *testing.T) {
	ctx := context.Background()
	h, server := ownershipHarness(t)
	if _, _, err := h.module.CreateDatabase(ctx, CreateDatabaseRequest{ServerID: server, Name: "orphan_db", OwnerUserID: "user_1", SiteID: "site_missing"}, nil); err == nil {
		t.Fatal("a database was accepted for a site that does not exist")
	}
	databases, err := h.module.ListDatabases(ctx, server)
	if err != nil || len(databases) != 0 {
		t.Fatalf("databases = %+v, err %v, want the refused row not persisted", databases, err)
	}
}

// A database with no site is still legitimate: not every managed database
// belongs to a site, and those must keep working exactly as before.
func TestCreateDatabaseWithoutASiteStoresNoOwner(t *testing.T) {
	ctx := context.Background()
	h, server := ownershipHarness(t)
	created, _, err := h.module.CreateDatabase(ctx, CreateDatabaseRequest{ServerID: server, Name: "standalone_db", OwnerUserID: "user_1"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if created.SiteID != "" {
		t.Fatalf("siteId = %q, want no owner", created.SiteID)
	}
}
