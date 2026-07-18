// Package backups is the control-plane module for FastPanel-style backups.
// Phase 1 owns backup accounts: the storage destinations (rclone remotes) that
// later phases' plans write copies to. Credentials are encrypted at rest with
// the shared secrets.Box; the node agent does the actual rclone work.
package backups

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/uptrace/bun"

	"github.com/nexa-panel/nexa-panel/internal/platform/jobs"
	"github.com/nexa-panel/nexa-panel/internal/platform/module"
	backupoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/backups"
	"github.com/nexa-panel/nexa-panel/internal/platform/persistence"
	"github.com/nexa-panel/nexa-panel/internal/platform/secrets"
)

// SiteResolver and DatabaseResolver let the backups module read the details a
// run needs (document roots, database connection info) from the sites and
// database modules without importing them — the composition root supplies thin
// adapters. A nil resolver means that target kind is unavailable and a plan
// selecting it fails the run with a clear message.
type SiteResolver interface {
	BackupSite(ctx context.Context, id string) (backupoperator.SiteTarget, error)
}

type DatabaseResolver interface {
	BackupDatabase(ctx context.Context, id string) (backupoperator.DatabaseTarget, error)
}

// Dependencies are the collaborators the module needs. Grouping them keeps the
// constructor readable as the module accreted a job queue and three resolvers.
type Dependencies struct {
	Database    *bun.DB
	Jobs        *jobs.Module
	Cipher      secrets.Cipher
	Operator    backupoperator.Operator
	Sites       SiteResolver
	Postgres    DatabaseResolver
	Mysql       DatabaseResolver
	StagingRoot string
	// StateDBPath is the control-plane database path; it is baked into each
	// plan's generated systemd service so `nexa backup trigger` finds the queue.
	StateDBPath string
	Logger      *slog.Logger
}

// accountsSchema is migration #1 for the "backups" module. Later phases append
// new statements (plans, copies) to the migration slice — never edit this one
// (see nexa-backend-conventions: versions are the slice index).
const accountsSchema = `
	CREATE TABLE backup_accounts (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL UNIQUE,
		type TEXT NOT NULL,
		path TEXT NOT NULL,
		config_json TEXT NOT NULL,
		secret_ciphertext TEXT,
		created_at TIMESTAMP NOT NULL,
		updated_at TIMESTAMP NOT NULL
	);
`

// AccountType values map straight onto rclone backend names.
const (
	TypeLocal = backupoperator.TypeLocal
	TypeS3    = backupoperator.TypeS3
	TypeSFTP  = backupoperator.TypeSFTP
	TypeDrive = backupoperator.TypeDrive
)

// Account is the JSON-facing view of a backup account. It deliberately never
// carries the account's secrets; HasSecret only reports whether any are stored.
type Account struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	Type      string            `json:"type"`
	Path      string            `json:"path"`
	Config    map[string]string `json:"config"`
	HasSecret bool              `json:"hasSecret"`
	CreatedAt time.Time         `json:"createdAt"`
	UpdatedAt time.Time         `json:"updatedAt"`
}

// AccountRequest is the create/update payload. Config holds non-secret rclone
// params (endpoint, host, bucket provider…); Secret holds sensitive ones
// (secret_access_key, sftp pass, drive token) and is encrypted before storage.
// On update, an omitted/empty Secret keeps the existing one; sending Secret with
// keys replaces it.
type AccountRequest struct {
	Name   string            `json:"name"`
	Type   string            `json:"type"`
	Path   string            `json:"path"`
	Config map[string]string `json:"config"`
	Secret map[string]string `json:"secret"`
}

type Module struct {
	database    *bun.DB
	jobs        *jobs.Module
	operator    backupoperator.Operator
	cipher      secrets.Cipher
	sites       SiteResolver
	postgres    DatabaseResolver
	mysql       DatabaseResolver
	stagingRoot string
	stateDBPath string
	logger      *slog.Logger
	now         func() time.Time
}

type accountModel struct {
	bun.BaseModel    `bun:"table:backup_accounts,alias:backup_account"`
	ID               string `bun:",pk"`
	Name             string
	Type             string
	Path             string
	ConfigJSON       string
	SecretCiphertext *string
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

func New(ctx context.Context, deps Dependencies) (*Module, error) {
	if deps.Database == nil || deps.Cipher == nil || deps.Operator == nil || deps.Jobs == nil {
		return nil, errors.New("backups database, cipher, node operator, and job queue are required")
	}
	if err := persistence.Migrate(ctx, deps.Database, "backups", []string{accountsSchema, plansSchema, copiesSchema}); err != nil {
		return nil, err
	}
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	module := &Module{
		database: deps.Database, jobs: deps.Jobs, operator: deps.Operator, cipher: deps.Cipher,
		sites: deps.Sites, postgres: deps.Postgres, mysql: deps.Mysql,
		stagingRoot: deps.StagingRoot, stateDBPath: deps.StateDBPath, logger: logger, now: time.Now,
	}
	deps.Jobs.RegisterHandler("backup.run", module.runJob)
	deps.Jobs.RegisterHandler("backup.restore", module.restoreJob)
	return module, nil
}

func (m *Module) Descriptor() module.Descriptor {
	return module.Descriptor{
		ID: "backups", Name: "Backups", Version: "0.1.0",
		Description:  "Scheduled backups of sites and databases to local or remote storage via rclone.",
		Dependencies: []string{"identity", "jobs"}, EstimatedIdleBytes: 512 * 1024,
	}
}

func (m *Module) Register(registry module.Registry) error {
	routes := []struct {
		pattern    string
		permission string
		handler    http.Handler
	}{
		{"GET /api/v1/backups/accounts", "backups.read", http.HandlerFunc(m.listAccountsHTTP)},
		{"POST /api/v1/backups/accounts", "backups.write", http.HandlerFunc(m.createAccountHTTP)},
		{"GET /api/v1/backups/accounts/{id}", "backups.read", http.HandlerFunc(m.getAccountHTTP)},
		{"PUT /api/v1/backups/accounts/{id}", "backups.write", http.HandlerFunc(m.updateAccountHTTP)},
		{"DELETE /api/v1/backups/accounts/{id}", "backups.write", http.HandlerFunc(m.deleteAccountHTTP)},
		{"POST /api/v1/backups/accounts/{id}/test", "backups.write", http.HandlerFunc(m.testAccountHTTP)},
		{"GET /api/v1/backups/plans", "backups.read", http.HandlerFunc(m.listPlansHTTP)},
		{"POST /api/v1/backups/plans", "backups.write", http.HandlerFunc(m.createPlanHTTP)},
		{"GET /api/v1/backups/plans/{id}", "backups.read", http.HandlerFunc(m.getPlanHTTP)},
		{"PUT /api/v1/backups/plans/{id}", "backups.write", http.HandlerFunc(m.updatePlanHTTP)},
		{"POST /api/v1/backups/plans/{id}/toggle", "backups.write", http.HandlerFunc(m.togglePlanHTTP)},
		{"POST /api/v1/backups/plans/{id}/run", "backups.write", http.HandlerFunc(m.runPlanHTTP)},
		{"DELETE /api/v1/backups/plans/{id}", "backups.write", http.HandlerFunc(m.deletePlanHTTP)},
		{"GET /api/v1/backups/plans/{id}/copies", "backups.read", http.HandlerFunc(m.listCopiesHTTP)},
		{"POST /api/v1/backups/copies/{copyId}/restore", "backups.write", http.HandlerFunc(m.restoreCopyHTTP)},
		{"DELETE /api/v1/backups/copies/{copyId}", "backups.write", http.HandlerFunc(m.deleteCopyHTTP)},
	}
	for _, route := range routes {
		if err := registry.HandleAuthorized(route.pattern, route.permission, route.handler); err != nil {
			return err
		}
	}
	return nil
}
