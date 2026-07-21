package mysql

import (
	"encoding/json"
	"time"

	mysqloperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/mysql"
	"github.com/uptrace/bun"
)

type Status string

const (
	StatusPlanning  Status = "planning"
	StatusPlanReady Status = "plan_ready"
	StatusApplying  Status = "applying"
	StatusActive    Status = "active"
	StatusBackingUp Status = "backing_up"
	StatusVerified  Status = "verified"
	StatusRestoring Status = "restoring"
	StatusDeleting  Status = "deleting"
	StatusFailed    Status = "failed"
)

type Engine struct {
	mysqloperator.Engine
	LastJobID *int64    `json:"lastJobId,omitempty"`
	Failure   string    `json:"failure,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type Account struct {
	ID                  string    `json:"id"`
	EngineID            string    `json:"engineId"`
	Name                string    `json:"name"`
	Host                string    `json:"host"`
	Status              Status    `json:"status"`
	CredentialAvailable bool      `json:"credentialAvailable"`
	CredentialVersion   int       `json:"credentialVersion"`
	LastJobID           *int64    `json:"lastJobId,omitempty"`
	Failure             string    `json:"failure,omitempty"`
	CreatedAt           time.Time `json:"createdAt"`
	UpdatedAt           time.Time `json:"updatedAt"`
}

type Database struct {
	ID             string `json:"id"`
	EngineID       string `json:"engineId"`
	Name           string `json:"name"`
	OwnerAccountID string `json:"ownerAccountId"`
	Status         Status `json:"status"`
	// A pointer rather than the int64-with-omitempty used for backup sizes: an
	// empty database really does measure zero bytes, and callers must be able
	// to tell that apart from one that has never been probed.
	SizeBytes      *int64     `json:"sizeBytes,omitempty"`
	SizeObservedAt *time.Time `json:"sizeObservedAt,omitempty"`
	LastJobID      *int64     `json:"lastJobId,omitempty"`
	Failure        string     `json:"failure,omitempty"`
	CreatedAt      time.Time  `json:"createdAt"`
	UpdatedAt      time.Time  `json:"updatedAt"`
}

type Grant struct {
	ID         string                    `json:"id"`
	DatabaseID string                    `json:"databaseId"`
	AccountID  string                    `json:"accountId"`
	Access     mysqloperator.AccessLevel `json:"access"`
	Status     Status                    `json:"status"`
	LastJobID  *int64                    `json:"lastJobId,omitempty"`
	Failure    string                    `json:"failure,omitempty"`
	CreatedAt  time.Time                 `json:"createdAt"`
	UpdatedAt  time.Time                 `json:"updatedAt"`
}

type RestorePoint struct {
	ID         string     `json:"id"`
	DatabaseID string     `json:"databaseId"`
	Status     Status     `json:"status"`
	SHA256     string     `json:"sha256,omitempty"`
	SizeBytes  int64      `json:"sizeBytes,omitempty"`
	VerifiedAt *time.Time `json:"verifiedAt,omitempty"`
	LastJobID  *int64     `json:"lastJobId,omitempty"`
	Failure    string     `json:"failure,omitempty"`
	CreatedAt  time.Time  `json:"createdAt"`
	UpdatedAt  time.Time  `json:"updatedAt"`
}

type StoredPlan struct {
	ID           string               `json:"id"`
	ResourceType string               `json:"resourceType"`
	ResourceID   string               `json:"resourceId"`
	Operation    mysqloperator.Action `json:"operation"`
	AgentPlan    mysqloperator.Plan   `json:"agentPlan"`
	CreatedAt    time.Time            `json:"createdAt"`
	ExpiresAt    time.Time            `json:"expiresAt"`
}

type engineModel struct {
	bun.BaseModel `bun:"table:mysql_family_engines,alias:engine"`
	ID            string `bun:",pk"`
	Kind          string
	Version       string
	VersionText   string
	Port          int
	Status        string
	SocketPath    string
	SystemdUnit   string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

type accountModel struct {
	bun.BaseModel               `bun:"table:mysql_accounts,alias:account"`
	ID                          string `bun:",pk"`
	EngineID                    string
	Name                        string
	Host                        string
	Status                      string
	CredentialCiphertext        *string
	PendingCredentialCiphertext *string
	PendingSecretDigest         *string
	CredentialRevealed          bool
	CredentialVersion           int
	LastJobID                   *int64
	Failure                     *string
	CreatedAt                   time.Time
	UpdatedAt                   time.Time
}

type databaseModel struct {
	bun.BaseModel  `bun:"table:mysql_databases,alias:database"`
	ID             string `bun:",pk"`
	EngineID       string
	Name           string
	OwnerAccountID string
	Status         string
	SizeBytes      *int64
	SizeObservedAt *time.Time
	LastJobID      *int64
	Failure        *string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type grantModel struct {
	bun.BaseModel `bun:"table:mysql_grants,alias:database_grant"`
	ID            string `bun:",pk"`
	DatabaseID    string
	AccountID     string
	Access        string
	Status        string
	LastJobID     *int64
	Failure       *string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

type restorePointModel struct {
	bun.BaseModel `bun:"table:mysql_restore_points,alias:restore_point"`
	ID            string `bun:",pk"`
	DatabaseID    string
	Status        string
	Path          string
	SHA256        *string
	SizeBytes     *int64
	VerifiedAt    *time.Time
	LastJobID     *int64
	Failure       *string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

type planModel struct {
	bun.BaseModel `bun:"table:mysql_family_plans,alias:mysql_family_plan"`
	ID            string `bun:",pk"`
	ResourceType  string
	ResourceID    string
	Operation     string
	PlanJSON      string
	CreatedAt     time.Time
	ExpiresAt     time.Time
}

func (m engineModel) toEngine() Engine {
	return Engine{Engine: mysqloperator.Engine{ID: m.ID, Kind: mysqloperator.EngineKind(m.Kind), Version: m.Version, VersionText: m.VersionText, Port: m.Port, Status: m.Status, SocketPath: m.SocketPath, SystemdUnit: m.SystemdUnit}, CreatedAt: m.CreatedAt, UpdatedAt: m.UpdatedAt}
}

func (m accountModel) toAccount() Account {
	return Account{ID: m.ID, EngineID: m.EngineID, Name: m.Name, Host: m.Host, Status: Status(m.Status), CredentialAvailable: m.CredentialCiphertext != nil && !m.CredentialRevealed, CredentialVersion: m.CredentialVersion, LastJobID: m.LastJobID, Failure: pointerString(m.Failure), CreatedAt: m.CreatedAt, UpdatedAt: m.UpdatedAt}
}

func (m databaseModel) toDatabase() Database {
	return Database{ID: m.ID, EngineID: m.EngineID, Name: m.Name, OwnerAccountID: m.OwnerAccountID, Status: Status(m.Status), SizeBytes: m.SizeBytes, SizeObservedAt: m.SizeObservedAt, LastJobID: m.LastJobID, Failure: pointerString(m.Failure), CreatedAt: m.CreatedAt, UpdatedAt: m.UpdatedAt}
}

func (m grantModel) toGrant() Grant {
	return Grant{ID: m.ID, DatabaseID: m.DatabaseID, AccountID: m.AccountID, Access: mysqloperator.AccessLevel(m.Access), Status: Status(m.Status), LastJobID: m.LastJobID, Failure: pointerString(m.Failure), CreatedAt: m.CreatedAt, UpdatedAt: m.UpdatedAt}
}

func (m restorePointModel) toRestorePoint() RestorePoint {
	var checksum string
	var size int64
	if m.SHA256 != nil {
		checksum = *m.SHA256
	}
	if m.SizeBytes != nil {
		size = *m.SizeBytes
	}
	return RestorePoint{ID: m.ID, DatabaseID: m.DatabaseID, Status: Status(m.Status), SHA256: checksum, SizeBytes: size, VerifiedAt: m.VerifiedAt, LastJobID: m.LastJobID, Failure: pointerString(m.Failure), CreatedAt: m.CreatedAt, UpdatedAt: m.UpdatedAt}
}

func (m planModel) toStoredPlan() (StoredPlan, error) {
	var plan mysqloperator.Plan
	if err := json.Unmarshal([]byte(m.PlanJSON), &plan); err != nil {
		return StoredPlan{}, err
	}
	return StoredPlan{ID: m.ID, ResourceType: m.ResourceType, ResourceID: m.ResourceID, Operation: mysqloperator.Action(m.Operation), AgentPlan: plan, CreatedAt: m.CreatedAt, ExpiresAt: m.ExpiresAt}, nil
}

func pointerString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
