package applications

import (
	"context"

	"github.com/nexa-panel/nexa-panel/internal/modules/admintools"
	packagesoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/packages"
)

// AdminToolsReader is the narrow view of the admin-tools module the catalog
// needs to surface phpMyAdmin/pgAdmin status without a second install path.
// Sync, not List: the admin_tools rows are reconciled from the node rather than
// seeded by a migration, so a panel that has never opened the Databases page has
// an empty table and would drop the web-client cards entirely.
type AdminToolsReader interface {
	Sync(context.Context) ([]admintools.Tool, error)
}

// catalogID is the stable UI/URL identifier for a catalog entry.
func catalogID(app, version string) string {
	if version == "" {
		return app
	}
	return app + "-" + version
}

// catalogByID resolves a UI identifier back to its packages catalog entry. The
// catalog lives on the node (only it knows what its repositories offer), so this
// is an agent call rather than a table lookup.
func (m *Module) catalogByID(ctx context.Context, id string) (packagesoperator.CatalogEntry, bool, error) {
	entries, err := m.operator.Catalog(ctx)
	if err != nil {
		return packagesoperator.CatalogEntry{}, false, err
	}
	for _, entry := range entries {
		if catalogID(entry.App, entry.Version) == id {
			return entry, true, nil
		}
	}
	return packagesoperator.CatalogEntry{}, false, nil
}

// webClientApplication maps an admin tool into an Applications card. The card's
// actions drive the admin-tools plan/apply interface, which hides whether the
// implementation is native (phpMyAdmin) or isolated (pgAdmin). Managed remains
// false because neither follows the generic Applications package workflow.
func webClientApplication(tool admintools.Tool) Application {
	label, summary := "phpMyAdmin", "Web UI for MySQL and MariaDB databases"
	if string(tool.Kind) == "pgadmin" {
		label, summary = "pgAdmin", "Web UI for PostgreSQL databases"
	}
	return Application{
		ID: string(tool.Kind), App: string(tool.Kind), Label: label, Summary: summary,
		Category: "web-client", Status: tool.Status, Managed: false,
		LastJobID: tool.LastJobID, Failure: tool.Failure,
	}
}
