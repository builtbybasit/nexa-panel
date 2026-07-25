package domains

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/modules/sites"
	"github.com/nexa-panel/nexa-panel/internal/platform/audit"
	"github.com/nexa-panel/nexa-panel/internal/platform/jobs"
	"github.com/nexa-panel/nexa-panel/internal/platform/persistence"
)

// A site created after the module was constructed still gets the implicit
// primary-domain row, and listing twice does not produce a second one. The
// backfill only runs the insert when a site is missing its row, so this is what
// proves the probe still sees a site the module has never listed.
func TestListBackfillsPrimaryDomainForSiteCreatedAfterStartup(t *testing.T) {
	ctx := context.Background()
	database, err := persistence.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := persistence.RunMigrations(ctx, database); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	auditLog, _ := audit.New(ctx, database)
	queue, err := jobs.NewWithConfig(ctx, database, auditLog, slog.New(slog.NewTextHandler(io.Discard, nil)), jobs.Config{PollInterval: 5 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	operator := fakeOperator{}
	sitesModule, err := sites.New(ctx, database, queue, runtimeCatalog{}, operator)
	if err != nil {
		t.Fatal(err)
	}
	module, err := New(ctx, database, queue, sitesModule, operator, fakeResolver{})
	if err != nil {
		t.Fatal(err)
	}
	site, _, err := sitesModule.Create(ctx, sites.CreateRequest{Slug: "demo-site", DisplayName: "Demo", PrimaryDomain: "demo.example.com", PHPVersion: "8.4"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	items, err := module.List(ctx, site.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Kind != KindPrimary || items[0].Hostname != "demo.example.com" {
		t.Fatalf("first listing = %+v", items)
	}
	again, err := module.List(ctx, site.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 1 || again[0].ID != items[0].ID {
		t.Fatalf("second listing = %+v", again)
	}
}
