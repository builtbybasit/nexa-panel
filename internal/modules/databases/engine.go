package databases

import (
	"context"
	"encoding/json"
	"time"
)

// Neutral change actions. The core plans, stores, audits, and commits in these
// terms; each adapter translates them into its operator's dialect.
const (
	ActionProvisionServer = "server.provision"
	ActionCreateUser      = "user.create"
	ActionRotateUser      = "user.rotate"
	ActionDropUser        = "user.drop"
	ActionCreateDatabase  = "database.create"
	ActionDropDatabase    = "database.drop"
	ActionApplyGrant      = "grant.apply"
	ActionRevokeGrant     = "grant.revoke"
	ActionCreateBackup    = "backup.create"
	ActionRestoreBackup   = "backup.restore"
)

const (
	AccessConnect   = "connect"
	AccessReadOnly  = "read_only"
	AccessReadWrite = "read_write"
)

// Spec is the static description of one engine family: naming, table layout,
// and capabilities. Everything the shared core needs to know about an engine
// that is data rather than behavior lives here.
type Spec struct {
	// Engine is the family key used across the API ("mysql", "postgresql").
	Engine string
	// DisplayName appears in user-facing messages and job titles.
	DisplayName string
	// JobKind prefixes the queue handler names ("mysql_family", "postgresql"),
	// preserved from the pre-merge modules so queued jobs keep resolving.
	JobKind string
	// CredentialLabelPrefix binds ciphertexts to their user row. It must never
	// change for an engine: existing secrets are encrypted under it.
	CredentialLabelPrefix string
	// AdminToolHost is the database host a containerized admin tool dials.
	AdminToolHost string
	// BackupRoot is where logical restore points are written on the node.
	BackupRoot string
	// UserScopedByHost marks engines whose user identity includes a client
	// host scope (MySQL's user@host).
	UserScopedByHost bool
	// Provisionable marks engines that can create new server instances, as
	// opposed to only discovering what the node runs.
	Provisionable bool
	Tables        Tables
	Columns       Columns
}

// Tables names the engine's state tables. They predate the unified module and
// stay as-is so no data migration is needed.
type Tables struct {
	Servers       string
	Users         string
	Databases     string
	Grants        string
	RestorePoints string
}

// Columns names the three foreign-key columns whose names differ per engine.
type Columns struct {
	ServerFK string // engine_id | instance_id
	OwnerFK  string // owner_account_id | owner_role_id
	UserFK   string // account_id | role_id
}

// Change is the engine-neutral description of one desired mutation. Adapters
// translate it into their operator's typed change; the core stores it next to
// the signed agent plan so commit can mirror exactly what was planned.
type Change struct {
	Action   string `json:"action"`
	ServerID string `json:"serverId"`
	Database string `json:"database,omitempty"`
	// Owner names the database's owning user; OwnerHost carries its MySQL
	// host scope when the engine has one.
	Owner     string `json:"owner,omitempty"`
	OwnerHost string `json:"ownerHost,omitempty"`
	// User names the user the change targets (create, rotate, drop, grant).
	User         string `json:"user,omitempty"`
	UserHost     string `json:"userHost,omitempty"`
	Access       string `json:"access,omitempty"`
	SecretSHA256 string `json:"secretSha256,omitempty"`
	BackupID     string `json:"backupId,omitempty"`
	BackupPath   string `json:"backupPath,omitempty"`
	BackupSHA256 string `json:"backupSha256,omitempty"`
	RestoreToken string `json:"restoreToken,omitempty"`
	// UserDatabases lists every database a user about to be dropped touches,
	// with the replacement owner for those it owns. Snapshotted at plan time so
	// commit mirrors the same transfers the engine performed.
	UserDatabases []UserDatabase `json:"userDatabases,omitempty"`
	// Provision carries the identity of a server being provisioned.
	Provision *ProvisionSpec `json:"provision,omitempty"`
}

// UserDatabase is one database entangled with a user being dropped. NewOwner
// fields are set only for databases the user owns.
type UserDatabase struct {
	DatabaseID string `json:"databaseId,omitempty"`
	Name       string `json:"name"`
	NewOwnerID string `json:"newOwnerId,omitempty"`
	NewOwner   string `json:"newOwner,omitempty"`
}

type ProvisionSpec struct {
	Version string `json:"version"`
	Cluster string `json:"cluster,omitempty"`
	Port    int    `json:"port,omitempty"`
}

// AgentPlan is the signed plan an engine operator produced, kept raw so its
// signature survives replay byte-exactly, plus the steps it announced — which
// the execute job streams as progress.
type AgentPlan struct {
	Raw   json.RawMessage
	Steps []string
}

// Observation is the engine-neutral result of applying a plan. Raw retains the
// operator's full observation for the job record and for adapter-side commits.
type Observation struct {
	Verified bool
	Restored bool
	Backup   *BackupObservation
	Raw      json.RawMessage
}

type BackupObservation struct {
	SHA256    string
	SizeBytes int64
	CreatedAt time.Time
	Verified  bool
}

// engineAdapter is the only surface an engine family implements. Everything
// else — state, jobs, HTTP, guards, credentials — is shared core.
type engineAdapter interface {
	Spec() Spec
	// SyncServers observes the node, persists what it sees, and returns the
	// stored servers; ListServers returns stored state only.
	SyncServers(ctx context.Context) ([]Server, error)
	ListServers(ctx context.Context) ([]Server, error)
	GetServer(ctx context.Context, id string) (Server, error)
	// CreateServer records a new instance in planning state. Engines that only
	// discover (MySQL family) reject it.
	CreateServer(ctx context.Context, request CreateServerRequest) (Server, error)
	// CommitServer persists the observed state of a freshly provisioned server.
	CommitServer(ctx context.Context, serverID string, observation Observation) error
	// Sizes measures every database on one server; engines with a single
	// per-node instance may ignore the ID.
	Sizes(ctx context.Context, serverID string) (map[string]int64, error)
	// PlanChange asks the engine operator for a signed plan for the neutral
	// change.
	PlanChange(ctx context.Context, change Change) (AgentPlan, error)
	// ApplyPlan executes a previously signed raw plan.
	ApplyPlan(ctx context.Context, raw json.RawMessage, secret string) (Observation, error)
}
