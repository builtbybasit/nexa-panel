package databases

import (
	"time"
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

// Server is one database server on the node: the sole MySQL-family engine or
// one PostgreSQL instance. Engine is the family key; Kind is the concrete
// flavor ("mysql", "mariadb", "postgresql").
type Server struct {
	ID            string    `json:"id"`
	Engine        string    `json:"engine"`
	Kind          string    `json:"kind"`
	Version       string    `json:"version"`
	VersionText   string    `json:"versionText,omitempty"`
	Cluster       string    `json:"cluster,omitempty"`
	Port          int       `json:"port"`
	Status        string    `json:"status"`
	SocketPath    string    `json:"socketPath"`
	SystemdUnit   string    `json:"systemdUnit"`
	ManagedByNexa bool      `json:"managedByNexa,omitempty"`
	LastJobID     *int64    `json:"lastJobId,omitempty"`
	Failure       string    `json:"failure,omitempty"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

type User struct {
	ID       string `json:"id"`
	ServerID string `json:"serverId"`
	Engine   string `json:"engine"`
	Name     string `json:"name"`
	Host     string `json:"host,omitempty"`
	Status   Status `json:"status"`
	// CredentialVersion counts applied password changes; the passwords
	// themselves live with whoever set them.
	CredentialVersion int       `json:"credentialVersion"`
	LastJobID         *int64    `json:"lastJobId,omitempty"`
	Failure           string    `json:"failure,omitempty"`
	CreatedAt         time.Time `json:"createdAt"`
	UpdatedAt         time.Time `json:"updatedAt"`
}

type Database struct {
	ID          string `json:"id"`
	ServerID    string `json:"serverId"`
	Engine      string `json:"engine"`
	Name        string `json:"name"`
	OwnerUserID string `json:"ownerUserId"`
	// The site this database was created for, empty when it belongs to no site.
	// It is the relation site teardown blocks on, so it travels with the row
	// rather than being re-derived from the name by whoever needs it.
	SiteID string `json:"siteId,omitempty"`
	Status Status `json:"status"`
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
	ID         string    `json:"id"`
	DatabaseID string    `json:"databaseId"`
	UserID     string    `json:"userId"`
	Engine     string    `json:"engine"`
	Access     string    `json:"access"`
	Status     Status    `json:"status"`
	LastJobID  *int64    `json:"lastJobId,omitempty"`
	Failure    string    `json:"failure,omitempty"`
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

type RestorePoint struct {
	ID         string     `json:"id"`
	DatabaseID string     `json:"databaseId"`
	Engine     string     `json:"engine"`
	Status     Status     `json:"status"`
	SHA256     string     `json:"sha256,omitempty"`
	SizeBytes  int64      `json:"sizeBytes,omitempty"`
	VerifiedAt *time.Time `json:"verifiedAt,omitempty"`
	LastJobID  *int64     `json:"lastJobId,omitempty"`
	Failure    string     `json:"failure,omitempty"`
	CreatedAt  time.Time  `json:"createdAt"`
	UpdatedAt  time.Time  `json:"updatedAt"`
}

func (row userRow) toUser(engine string) User {
	return User{ID: row.ID, ServerID: row.ServerID, Engine: engine, Name: row.Name, Host: row.Host, Status: Status(row.Status), CredentialVersion: row.CredentialVersion, LastJobID: row.LastJobID, Failure: pointerString(row.Failure), CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}
}

func (row databaseRow) toDatabase(engine string) Database {
	return Database{ID: row.ID, ServerID: row.ServerID, Engine: engine, Name: row.Name, OwnerUserID: row.OwnerUserID, SiteID: pointerString(row.SiteID), Status: Status(row.Status), SizeBytes: row.SizeBytes, SizeObservedAt: row.SizeObservedAt, LastJobID: row.LastJobID, Failure: pointerString(row.Failure), CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}
}

func (row grantRow) toGrant(engine string) Grant {
	return Grant{ID: row.ID, DatabaseID: row.DatabaseID, UserID: row.UserID, Engine: engine, Access: row.Access, Status: Status(row.Status), LastJobID: row.LastJobID, Failure: pointerString(row.Failure), CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}
}

func (row restorePointRow) toRestorePoint(engine string) RestorePoint {
	var checksum string
	var size int64
	if row.SHA256 != nil {
		checksum = *row.SHA256
	}
	if row.SizeBytes != nil {
		size = *row.SizeBytes
	}
	return RestorePoint{ID: row.ID, DatabaseID: row.DatabaseID, Engine: engine, Status: Status(row.Status), SHA256: checksum, SizeBytes: size, VerifiedAt: row.VerifiedAt, LastJobID: row.LastJobID, Failure: pointerString(row.Failure), CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}
}

func pointerString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
