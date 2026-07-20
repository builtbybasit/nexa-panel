package postgres

import (
	"encoding/json"
	"time"

	postgresoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/postgres"
	"github.com/uptrace/bun"
)

const schema = `
	CREATE TABLE postgresql_instances (
		id TEXT PRIMARY KEY,
		version TEXT NOT NULL,
		cluster_name TEXT NOT NULL,
		port INTEGER NOT NULL UNIQUE,
		status TEXT NOT NULL,
		owner TEXT NOT NULL DEFAULT 'postgres',
		data_path TEXT NOT NULL UNIQUE,
		socket_path TEXT NOT NULL,
		log_path TEXT NOT NULL UNIQUE,
		config_path TEXT NOT NULL UNIQUE,
		systemd_unit TEXT NOT NULL UNIQUE,
		managed_by_nexa BOOLEAN NOT NULL DEFAULT FALSE,
		last_job_id INTEGER REFERENCES jobs(id),
		failure TEXT,
		created_at TIMESTAMP NOT NULL,
		updated_at TIMESTAMP NOT NULL,
		UNIQUE(version, cluster_name)
	);
	CREATE TABLE database_roles (
		id TEXT PRIMARY KEY,
		instance_id TEXT NOT NULL REFERENCES postgresql_instances(id) ON DELETE CASCADE,
		name TEXT NOT NULL,
		status TEXT NOT NULL,
		credential_ciphertext TEXT,
		pending_credential_ciphertext TEXT,
		pending_secret_digest TEXT,
		credential_revealed BOOLEAN NOT NULL DEFAULT FALSE,
		credential_version INTEGER NOT NULL DEFAULT 0,
		last_job_id INTEGER REFERENCES jobs(id),
		failure TEXT,
		created_at TIMESTAMP NOT NULL,
		updated_at TIMESTAMP NOT NULL,
		UNIQUE(instance_id, name)
	);
	CREATE TABLE managed_databases (
		id TEXT PRIMARY KEY,
		instance_id TEXT NOT NULL REFERENCES postgresql_instances(id) ON DELETE CASCADE,
		name TEXT NOT NULL,
		owner_role_id TEXT NOT NULL REFERENCES database_roles(id),
		status TEXT NOT NULL,
		last_job_id INTEGER REFERENCES jobs(id),
		failure TEXT,
		created_at TIMESTAMP NOT NULL,
		updated_at TIMESTAMP NOT NULL,
		UNIQUE(instance_id, name)
	);
	CREATE TABLE database_grants (
		id TEXT PRIMARY KEY,
		database_id TEXT NOT NULL REFERENCES managed_databases(id) ON DELETE CASCADE,
		role_id TEXT NOT NULL REFERENCES database_roles(id) ON DELETE CASCADE,
		access TEXT NOT NULL,
		status TEXT NOT NULL,
		last_job_id INTEGER REFERENCES jobs(id),
		failure TEXT,
		created_at TIMESTAMP NOT NULL,
		updated_at TIMESTAMP NOT NULL,
		UNIQUE(database_id, role_id)
	);
	CREATE TABLE database_restore_points (
		id TEXT PRIMARY KEY,
		database_id TEXT NOT NULL REFERENCES managed_databases(id) ON DELETE CASCADE,
		status TEXT NOT NULL,
		path TEXT NOT NULL UNIQUE,
		sha256 TEXT,
		size_bytes INTEGER,
		verified_at TIMESTAMP,
		last_job_id INTEGER REFERENCES jobs(id),
		failure TEXT,
		created_at TIMESTAMP NOT NULL,
		updated_at TIMESTAMP NOT NULL
	);
	CREATE TABLE postgresql_plans (
		id TEXT PRIMARY KEY,
		resource_type TEXT NOT NULL,
		resource_id TEXT NOT NULL,
		operation TEXT NOT NULL,
		plan_json TEXT NOT NULL,
		created_at TIMESTAMP NOT NULL,
		expires_at TIMESTAMP NOT NULL
	);
	CREATE INDEX postgresql_plans_resource_idx ON postgresql_plans(resource_type, resource_id, created_at DESC);
`

// Appended rather than folded into schema: persistence.Migrate versions
// migrations by slice index, so editing an already-applied element would skip
// this column on every existing install while fresh databases got it.
// size_observed_at drives both the refresh interval and the "measured at"
// hint in the UI; both columns stay nullable because a database that has never
// been probed is not the same as one measured at zero bytes.
const databaseSizeSchema = `
	ALTER TABLE managed_databases ADD COLUMN size_bytes INTEGER;
	ALTER TABLE managed_databases ADD COLUMN size_observed_at TIMESTAMP;
`

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

type Instance struct {
	postgresoperator.Instance
	LastJobID *int64    `json:"lastJobId,omitempty"`
	Failure   string    `json:"failure,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type Role struct {
	ID                  string    `json:"id"`
	InstanceID          string    `json:"instanceId"`
	Name                string    `json:"name"`
	Status              Status    `json:"status"`
	CredentialAvailable bool      `json:"credentialAvailable"`
	CredentialVersion   int       `json:"credentialVersion"`
	LastJobID           *int64    `json:"lastJobId,omitempty"`
	Failure             string    `json:"failure,omitempty"`
	CreatedAt           time.Time `json:"createdAt"`
	UpdatedAt           time.Time `json:"updatedAt"`
}

type Database struct {
	ID          string `json:"id"`
	InstanceID  string `json:"instanceId"`
	Name        string `json:"name"`
	OwnerRoleID string `json:"ownerRoleId"`
	Status      Status `json:"status"`
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
	ID         string                       `json:"id"`
	DatabaseID string                       `json:"databaseId"`
	RoleID     string                       `json:"roleId"`
	Access     postgresoperator.AccessLevel `json:"access"`
	Status     Status                       `json:"status"`
	LastJobID  *int64                       `json:"lastJobId,omitempty"`
	Failure    string                       `json:"failure,omitempty"`
	CreatedAt  time.Time                    `json:"createdAt"`
	UpdatedAt  time.Time                    `json:"updatedAt"`
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
	ID           string                  `json:"id"`
	ResourceType string                  `json:"resourceType"`
	ResourceID   string                  `json:"resourceId"`
	Operation    postgresoperator.Action `json:"operation"`
	AgentPlan    postgresoperator.Plan   `json:"agentPlan"`
	CreatedAt    time.Time               `json:"createdAt"`
	ExpiresAt    time.Time               `json:"expiresAt"`
}

type instanceModel struct {
	bun.BaseModel `bun:"table:postgresql_instances,alias:instance"`
	ID            string `bun:",pk"`
	Version       string
	ClusterName   string
	Port          int
	Status        string
	Owner         string
	DataPath      string
	SocketPath    string
	LogPath       string
	ConfigPath    string
	SystemdUnit   string
	ManagedByNexa bool
	LastJobID     *int64
	Failure       *string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

type roleModel struct {
	bun.BaseModel               `bun:"table:database_roles,alias:role"`
	ID                          string `bun:",pk"`
	InstanceID                  string
	Name                        string
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
	bun.BaseModel  `bun:"table:managed_databases,alias:database"`
	ID             string `bun:",pk"`
	InstanceID     string
	Name           string
	OwnerRoleID    string
	Status         string
	SizeBytes      *int64
	SizeObservedAt *time.Time
	LastJobID      *int64
	Failure        *string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type grantModel struct {
	bun.BaseModel `bun:"table:database_grants,alias:database_grant"`
	ID            string `bun:",pk"`
	DatabaseID    string
	RoleID        string
	Access        string
	Status        string
	LastJobID     *int64
	Failure       *string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

type restorePointModel struct {
	bun.BaseModel `bun:"table:database_restore_points,alias:restore_point"`
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
	bun.BaseModel `bun:"table:postgresql_plans,alias:postgresql_plan"`
	ID            string `bun:",pk"`
	ResourceType  string
	ResourceID    string
	Operation     string
	PlanJSON      string
	CreatedAt     time.Time
	ExpiresAt     time.Time
}

func (m instanceModel) toInstance() Instance {
	return Instance{Instance: postgresoperator.Instance{ID: m.ID, Version: m.Version, Cluster: m.ClusterName, Port: m.Port, Status: m.Status, Owner: m.Owner, DataPath: m.DataPath, SocketPath: m.SocketPath, LogPath: m.LogPath, ConfigPath: m.ConfigPath, SystemdUnit: m.SystemdUnit, ManagedByNexa: m.ManagedByNexa}, LastJobID: m.LastJobID, Failure: pointerString(m.Failure), CreatedAt: m.CreatedAt, UpdatedAt: m.UpdatedAt}
}

func (m roleModel) toRole() Role {
	return Role{ID: m.ID, InstanceID: m.InstanceID, Name: m.Name, Status: Status(m.Status), CredentialAvailable: m.CredentialCiphertext != nil && !m.CredentialRevealed, CredentialVersion: m.CredentialVersion, LastJobID: m.LastJobID, Failure: pointerString(m.Failure), CreatedAt: m.CreatedAt, UpdatedAt: m.UpdatedAt}
}

func (m databaseModel) toDatabase() Database {
	return Database{ID: m.ID, InstanceID: m.InstanceID, Name: m.Name, OwnerRoleID: m.OwnerRoleID, Status: Status(m.Status), SizeBytes: m.SizeBytes, SizeObservedAt: m.SizeObservedAt, LastJobID: m.LastJobID, Failure: pointerString(m.Failure), CreatedAt: m.CreatedAt, UpdatedAt: m.UpdatedAt}
}

func (m grantModel) toGrant() Grant {
	return Grant{ID: m.ID, DatabaseID: m.DatabaseID, RoleID: m.RoleID, Access: postgresoperator.AccessLevel(m.Access), Status: Status(m.Status), LastJobID: m.LastJobID, Failure: pointerString(m.Failure), CreatedAt: m.CreatedAt, UpdatedAt: m.UpdatedAt}
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
	var plan postgresoperator.Plan
	if err := json.Unmarshal([]byte(m.PlanJSON), &plan); err != nil {
		return StoredPlan{}, err
	}
	return StoredPlan{ID: m.ID, ResourceType: m.ResourceType, ResourceID: m.ResourceID, Operation: postgresoperator.Action(m.Operation), AgentPlan: plan, CreatedAt: m.CreatedAt, ExpiresAt: m.ExpiresAt}, nil
}

func pointerString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
