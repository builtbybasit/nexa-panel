// Package migrations holds the control-plane's timestamped up/down SQL
// migrations as an embedded filesystem. Each module contributes its schema as
// `<timestamp>_<name>.tx.up.sql` / `.tx.down.sql`; persistence.RunMigrations
// discovers and applies them in timestamp order as one global timeline.
//
// The files live at the repository root (Laravel-style) rather than beside the
// persistence code because go:embed cannot reach outside its own package
// directory — so this tiny package sits alongside the .sql files and exports
// them for persistence to consume.
package migrations

import "embed"

// FS holds every migration file in this directory.
//
//go:embed *.sql
var FS embed.FS
