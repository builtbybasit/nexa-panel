// Package backups owns storage accounts, schedules, backup execution, copy
// health, retention, and restore orchestration. Credentials remain encrypted in
// the control panel; privileged filesystem and database work runs in the agent.
package backups

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"path/filepath"
	"sync"
	"time"

	"github.com/uptrace/bun"

	"github.com/nexa-panel/nexa-panel/internal/platform/identity"
	"github.com/nexa-panel/nexa-panel/internal/platform/jobs"
	"github.com/nexa-panel/nexa-panel/internal/platform/module"
	backupoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/backups"
	"github.com/nexa-panel/nexa-panel/internal/platform/secrets"
	"github.com/nexa-panel/nexa-panel/internal/platform/webhandler"
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

type SiteAccessPolicy interface {
	AccessibleSiteIDs(ctx context.Context, user identity.User) (all bool, ids []string, err error)
}

// Dependencies are the collaborators the module needs. Grouping them keeps the
// constructor readable as the module accreted a job queue and three resolvers.
type Dependencies struct {
	Database     *bun.DB
	Jobs         *jobs.Module
	Cipher       secrets.Cipher
	Operator     backupoperator.Operator
	Sites        SiteResolver
	Postgres     DatabaseResolver
	Mysql        DatabaseResolver
	AccessPolicy SiteAccessPolicy
	// StateDBPath is the control-panel database path; it is baked into each
	// plan's generated systemd service so `nexa backup trigger` finds the queue.
	StateDBPath string
	Logger      *slog.Logger
}

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
	database          *bun.DB
	jobs              *jobs.Module
	operator          backupoperator.Operator
	cipher            secrets.Cipher
	sites             SiteResolver
	postgres          DatabaseResolver
	mysql             DatabaseResolver
	accessPolicy      SiteAccessPolicy
	stateDBPath       string
	logger            *slog.Logger
	now               func() time.Time
	restorePreviewKey []byte
	restorePreviewTTL time.Duration

	scheduleRetryInterval time.Duration
	scheduleFullInterval  time.Duration
	reconcileCancel       context.CancelFunc
	reconcileDone         chan struct{}
	reconcileStartOnce    sync.Once
	reconcileCloseOnce    sync.Once
	scheduleSyncMu        sync.Mutex
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
	if deps.Database == nil || deps.Cipher == nil || deps.Operator == nil || deps.Jobs == nil || deps.AccessPolicy == nil {
		return nil, errors.New("backups database, cipher, node operator, job queue, and site access policy are required")
	}
	if !filepath.IsAbs(deps.StateDBPath) || filepath.Clean(deps.StateDBPath) != deps.StateDBPath {
		return nil, errors.New("backups state database path must be absolute and clean")
	}
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	module := &Module{
		database: deps.Database, jobs: deps.Jobs, operator: deps.Operator, cipher: deps.Cipher,
		sites: deps.Sites, postgres: deps.Postgres, mysql: deps.Mysql, accessPolicy: deps.AccessPolicy,
		stateDBPath: deps.StateDBPath, logger: logger, now: time.Now,
		restorePreviewKey: []byte(randomToken() + randomToken()), restorePreviewTTL: 5 * time.Minute,
		scheduleRetryInterval: time.Minute, scheduleFullInterval: 30 * time.Minute,
		reconcileDone: make(chan struct{}),
	}
	if err := deps.Jobs.RegisterHandlerWithOptions("backup.run", module.runJob, jobs.HandlerOptions{RecoveryPolicy: jobs.RecoveryFail}); err != nil {
		return nil, err
	}
	if err := deps.Jobs.RegisterHandlerWithOptions("backup.restore", module.restoreJob, jobs.HandlerOptions{RecoveryPolicy: jobs.RecoveryFail}); err != nil {
		return nil, err
	}
	if err := deps.Jobs.RegisterHandlerWithOptions("backup.system", module.systemJob, jobs.HandlerOptions{RecoveryPolicy: jobs.RecoveryFail}); err != nil {
		return nil, err
	}
	return module, nil
}

func (m *Module) Descriptor() module.Descriptor {
	return module.Descriptor{
		ID: "backups", Name: "Backups", Version: "0.1.0",
		Description:  "Scheduled backups of sites and databases to local or remote storage via rclone.",
		Dependencies: []string{"identity", "jobs"}, EstimatedIdleBytes: 512 * 1024,
	}
}

// Register binds every handler to the route and permission its operationId
// declares in the OpenAPI contract. Method, path, and required permission come
// from the embedded spec (internal/platform/httpapi/apispec), so this map is the
// whole routing table and a renamed or missing operation fails startup instead
// of drifting from the published contract.
func (m *Module) Register(registry module.Registry) error {
	return webhandler.Register(registry, map[string]http.HandlerFunc{
		"listBackupAccounts":   m.listAccountsHTTP,
		"createBackupAccount":  m.createAccountHTTP,
		"getBackupAccount":     m.getAccountHTTP,
		"updateBackupAccount":  m.updateAccountHTTP,
		"deleteBackupAccount":  m.deleteAccountHTTP,
		"testBackupAccount":    m.testAccountHTTP,
		"listBackupPlans":      m.listPlansHTTP,
		"createBackupPlan":     m.createPlanHTTP,
		"getBackupPlan":        m.getPlanHTTP,
		"updateBackupPlan":     m.updatePlanHTTP,
		"toggleBackupPlan":     m.togglePlanHTTP,
		"runBackupPlan":        m.runPlanHTTP,
		"deleteBackupPlan":     m.deletePlanHTTP,
		"listBackupCopies":     m.listCopiesHTTP,
		"previewBackupRestore": m.previewRestoreCopyHTTP,
		"restoreBackupCopy":    m.restoreCopyHTTP,
		"deleteBackupCopy":     m.deleteCopyHTTP,
		"listSystemBackups":    m.listSystemCopiesHTTP,
		"runSystemBackup":      m.runSystemHTTP,
	})
}
