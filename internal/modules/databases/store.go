package databases

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/uptrace/bun"
)

// store reads and writes one engine's state tables. The tables predate the
// unified module and differ only in names, three foreign-key columns, and the
// MySQL-only host column, so one implementation serves every engine by
// splicing those identifiers — all fixed strings from the Spec, never request
// input — into its SQL.
type store struct {
	db   *bun.DB
	spec Spec
	now  func() time.Time
}

type userRow struct {
	ID                          string    `bun:"id"`
	ServerID                    string    `bun:"server_id"`
	Name                        string    `bun:"name"`
	Host                        string    `bun:"host"`
	Status                      string    `bun:"status"`
	CredentialCiphertext        *string   `bun:"credential_ciphertext"`
	PendingCredentialCiphertext *string   `bun:"pending_credential_ciphertext"`
	PendingSecretDigest         *string   `bun:"pending_secret_digest"`
	CredentialRevealed          bool      `bun:"credential_revealed"`
	CredentialVersion           int       `bun:"credential_version"`
	LastJobID                   *int64    `bun:"last_job_id"`
	Failure                     *string   `bun:"failure"`
	CreatedAt                   time.Time `bun:"created_at"`
	UpdatedAt                   time.Time `bun:"updated_at"`
}

type databaseRow struct {
	ID             string     `bun:"id"`
	ServerID       string     `bun:"server_id"`
	Name           string     `bun:"name"`
	OwnerUserID    string     `bun:"owner_user_id"`
	SiteID         *string    `bun:"site_id"`
	Status         string     `bun:"status"`
	SizeBytes      *int64     `bun:"size_bytes"`
	SizeObservedAt *time.Time `bun:"size_observed_at"`
	LastJobID      *int64     `bun:"last_job_id"`
	Failure        *string    `bun:"failure"`
	CreatedAt      time.Time  `bun:"created_at"`
	UpdatedAt      time.Time  `bun:"updated_at"`
}

type grantRow struct {
	ID         string    `bun:"id"`
	DatabaseID string    `bun:"database_id"`
	UserID     string    `bun:"user_id"`
	Access     string    `bun:"access"`
	Status     string    `bun:"status"`
	LastJobID  *int64    `bun:"last_job_id"`
	Failure    *string   `bun:"failure"`
	CreatedAt  time.Time `bun:"created_at"`
	UpdatedAt  time.Time `bun:"updated_at"`
}

type restorePointRow struct {
	ID         string     `bun:"id"`
	DatabaseID string     `bun:"database_id"`
	Status     string     `bun:"status"`
	Path       string     `bun:"path"`
	SHA256     *string    `bun:"sha256"`
	SizeBytes  *int64     `bun:"size_bytes"`
	VerifiedAt *time.Time `bun:"verified_at"`
	LastJobID  *int64     `bun:"last_job_id"`
	Failure    *string    `bun:"failure"`
	CreatedAt  time.Time  `bun:"created_at"`
	UpdatedAt  time.Time  `bun:"updated_at"`
}

// hostColumn is the host expression for user selects: the real column on
// engines with host-scoped users, an empty literal elsewhere.
func (s *store) hostColumn() string {
	if s.spec.UserScopedByHost {
		return "host"
	}
	return "''"
}

func (s *store) userSelect() string {
	return "SELECT id, " + s.spec.Columns.ServerFK + " AS server_id, name, " + s.hostColumn() + " AS host, status, credential_ciphertext, pending_credential_ciphertext, pending_secret_digest, credential_revealed, credential_version, last_job_id, failure, created_at, updated_at FROM " + s.spec.Tables.Users
}

func (s *store) databaseSelect() string {
	return "SELECT id, " + s.spec.Columns.ServerFK + " AS server_id, name, " + s.spec.Columns.OwnerFK + " AS owner_user_id, site_id, status, size_bytes, size_observed_at, last_job_id, failure, created_at, updated_at FROM " + s.spec.Tables.Databases
}

func (s *store) grantSelect() string {
	return "SELECT id, database_id, " + s.spec.Columns.UserFK + " AS user_id, access, status, last_job_id, failure, created_at, updated_at FROM " + s.spec.Tables.Grants
}

func (s *store) restorePointSelect() string {
	return "SELECT id, database_id, status, path, sha256, size_bytes, verified_at, last_job_id, failure, created_at, updated_at FROM " + s.spec.Tables.RestorePoints
}

func (s *store) ListUsers(ctx context.Context, serverID string) ([]userRow, error) {
	rows := []userRow{}
	query, args := s.userSelect()+" ORDER BY name ASC", []any{}
	if serverID != "" {
		query, args = s.userSelect()+" WHERE "+s.spec.Columns.ServerFK+" = ? ORDER BY name ASC", []any{serverID}
	}
	err := s.db.NewRaw(query, args...).Scan(ctx, &rows)
	return rows, err
}

func (s *store) GetUser(ctx context.Context, id string) (userRow, error) {
	var row userRow
	err := s.db.NewRaw(s.userSelect()+" WHERE id = ?", id).Scan(ctx, &row)
	return row, err
}

// GetUserByName resolves a user by server and name; on host-scoped engines an
// ambiguous name (same user on two hosts) resolves to the first by host order.
func (s *store) GetUserByName(ctx context.Context, serverID, name string) (userRow, error) {
	var row userRow
	err := s.db.NewRaw(s.userSelect()+" WHERE "+s.spec.Columns.ServerFK+" = ? AND name = ? ORDER BY host ASC LIMIT 1", serverID, name).Scan(ctx, &row)
	return row, err
}

func (s *store) InsertUser(ctx context.Context, row userRow) error {
	if s.spec.UserScopedByHost {
		_, err := s.db.ExecContext(ctx, "INSERT INTO "+s.spec.Tables.Users+" (id, "+s.spec.Columns.ServerFK+", name, host, status, pending_credential_ciphertext, pending_secret_digest, credential_revealed, credential_version, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, FALSE, 0, ?, ?)",
			row.ID, row.ServerID, row.Name, row.Host, row.Status, row.PendingCredentialCiphertext, row.PendingSecretDigest, row.CreatedAt, row.UpdatedAt)
		return err
	}
	_, err := s.db.ExecContext(ctx, "INSERT INTO "+s.spec.Tables.Users+" (id, "+s.spec.Columns.ServerFK+", name, status, pending_credential_ciphertext, pending_secret_digest, credential_revealed, credential_version, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, FALSE, 0, ?, ?)",
		row.ID, row.ServerID, row.Name, row.Status, row.PendingCredentialCiphertext, row.PendingSecretDigest, row.CreatedAt, row.UpdatedAt)
	return err
}

// SetUserPendingCredential stages a new secret and marks the user busy while
// the engine applies it.
func (s *store) SetUserPendingCredential(ctx context.Context, id, ciphertext, digest string) error {
	_, err := s.db.ExecContext(ctx, "UPDATE "+s.spec.Tables.Users+" SET pending_credential_ciphertext = ?, pending_secret_digest = ?, status = ?, failure = NULL, updated_at = ? WHERE id = ?",
		ciphertext, digest, StatusApplying, s.now().UTC(), id)
	return err
}

// SwapUserCredential promotes the pending secret after a verified apply.
func (s *store) SwapUserCredential(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, "UPDATE "+s.spec.Tables.Users+" SET credential_ciphertext = pending_credential_ciphertext, pending_credential_ciphertext = NULL, pending_secret_digest = NULL, credential_revealed = FALSE, credential_version = credential_version + 1, status = ?, failure = NULL, updated_at = ? WHERE id = ?",
		StatusActive, s.now().UTC(), id)
	return err
}

// AbandonUserPendingCredential drops a staged secret after a failed change on
// a user that already has a working credential, restoring it to active.
func (s *store) AbandonUserPendingCredential(ctx context.Context, id, failure string) error {
	_, err := s.db.ExecContext(ctx, "UPDATE "+s.spec.Tables.Users+" SET status = ?, pending_credential_ciphertext = NULL, pending_secret_digest = NULL, failure = ?, updated_at = ? WHERE id = ?",
		StatusActive, failure, s.now().UTC(), id)
	return err
}

func (s *store) DeleteUser(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM "+s.spec.Tables.Users+" WHERE id = ?", id)
	return err
}

func (s *store) ListDatabases(ctx context.Context, serverID string) ([]databaseRow, error) {
	rows := []databaseRow{}
	query, args := s.databaseSelect()+" ORDER BY name ASC", []any{}
	if serverID != "" {
		query, args = s.databaseSelect()+" WHERE "+s.spec.Columns.ServerFK+" = ? ORDER BY name ASC", []any{serverID}
	}
	err := s.db.NewRaw(query, args...).Scan(ctx, &rows)
	return rows, err
}

func (s *store) GetDatabase(ctx context.Context, id string) (databaseRow, error) {
	var row databaseRow
	err := s.db.NewRaw(s.databaseSelect()+" WHERE id = ?", id).Scan(ctx, &row)
	return row, err
}

func (s *store) ListDatabasesOwnedBy(ctx context.Context, userID string) ([]databaseRow, error) {
	rows := []databaseRow{}
	err := s.db.NewRaw(s.databaseSelect()+" WHERE "+s.spec.Columns.OwnerFK+" = ?", userID).Scan(ctx, &rows)
	return rows, err
}

func (s *store) InsertDatabase(ctx context.Context, row databaseRow) error {
	_, err := s.db.ExecContext(ctx, "INSERT INTO "+s.spec.Tables.Databases+" (id, "+s.spec.Columns.ServerFK+", name, "+s.spec.Columns.OwnerFK+", site_id, status, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
		row.ID, row.ServerID, row.Name, row.OwnerUserID, row.SiteID, row.Status, row.CreatedAt, row.UpdatedAt)
	return err
}

func (s *store) SetDatabaseSize(ctx context.Context, id string, size int64, observedAt time.Time) error {
	_, err := s.db.ExecContext(ctx, "UPDATE "+s.spec.Tables.Databases+" SET size_bytes = ?, size_observed_at = ? WHERE id = ?", size, observedAt, id)
	return err
}

func (s *store) SetDatabaseOwner(ctx context.Context, id, ownerUserID string) error {
	_, err := s.db.ExecContext(ctx, "UPDATE "+s.spec.Tables.Databases+" SET "+s.spec.Columns.OwnerFK+" = ?, updated_at = ? WHERE id = ?", ownerUserID, s.now().UTC(), id)
	return err
}

// SetDatabaseActive is the restore-commit transition; it clears any failure
// left by earlier attempts.
func (s *store) SetDatabaseActive(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, "UPDATE "+s.spec.Tables.Databases+" SET status = ?, failure = NULL, updated_at = ? WHERE id = ?", StatusActive, s.now().UTC(), id)
	return err
}

// DeleteDatabaseCascade removes a dropped database and every managed row that
// hangs off it.
func (s *store) DeleteDatabaseCascade(ctx context.Context, id string) error {
	if _, err := s.db.ExecContext(ctx, "DELETE FROM "+s.spec.Tables.Grants+" WHERE database_id = ?", id); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, "DELETE FROM "+s.spec.Tables.RestorePoints+" WHERE database_id = ?", id); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, "DELETE FROM "+s.spec.Tables.Databases+" WHERE id = ?", id)
	return err
}

func (s *store) ListGrants(ctx context.Context, databaseID string) ([]grantRow, error) {
	rows := []grantRow{}
	query, args := s.grantSelect()+" ORDER BY created_at ASC", []any{}
	if databaseID != "" {
		query, args = s.grantSelect()+" WHERE database_id = ? ORDER BY created_at ASC", []any{databaseID}
	}
	err := s.db.NewRaw(query, args...).Scan(ctx, &rows)
	return rows, err
}

func (s *store) GetGrant(ctx context.Context, id string) (grantRow, error) {
	var row grantRow
	err := s.db.NewRaw(s.grantSelect()+" WHERE id = ?", id).Scan(ctx, &row)
	return row, err
}

func (s *store) FindGrant(ctx context.Context, databaseID, userID string) (grantRow, error) {
	var row grantRow
	err := s.db.NewRaw(s.grantSelect()+" WHERE database_id = ? AND "+s.spec.Columns.UserFK+" = ?", databaseID, userID).Scan(ctx, &row)
	return row, err
}

func (s *store) ListGrantsForUser(ctx context.Context, userID string) ([]grantRow, error) {
	rows := []grantRow{}
	err := s.db.NewRaw(s.grantSelect()+" WHERE "+s.spec.Columns.UserFK+" = ?", userID).Scan(ctx, &rows)
	return rows, err
}

// CountOtherGrants counts grants on a database held by anyone but the given
// user — the quantity the last-user guard and ownership transfer hinge on.
func (s *store) CountOtherGrants(ctx context.Context, databaseID, userID string) (int, error) {
	var count int
	err := s.db.NewRaw("SELECT COUNT(*) FROM "+s.spec.Tables.Grants+" WHERE database_id = ? AND "+s.spec.Columns.UserFK+" != ?", databaseID, userID).Scan(ctx, &count)
	return count, err
}

// FindReplacementGrant returns another user's grant on the database, the one
// that inherits ownership when the current owner is dropped.
func (s *store) FindReplacementGrant(ctx context.Context, databaseID, userID string) (grantRow, error) {
	var row grantRow
	err := s.db.NewRaw(s.grantSelect()+" WHERE database_id = ? AND "+s.spec.Columns.UserFK+" != ? ORDER BY created_at ASC LIMIT 1", databaseID, userID).Scan(ctx, &row)
	return row, err
}

func (s *store) InsertGrant(ctx context.Context, row grantRow) error {
	_, err := s.db.ExecContext(ctx, "INSERT INTO "+s.spec.Tables.Grants+" (id, database_id, "+s.spec.Columns.UserFK+", access, status, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)",
		row.ID, row.DatabaseID, row.UserID, row.Access, row.Status, row.CreatedAt, row.UpdatedAt)
	return err
}

// ResetGrant re-points an existing grant at a new access level and marks it
// busy, which is how a repeated grant request becomes an update.
func (s *store) ResetGrant(ctx context.Context, id, access string) error {
	_, err := s.db.ExecContext(ctx, "UPDATE "+s.spec.Tables.Grants+" SET access = ?, status = ?, failure = NULL, updated_at = ? WHERE id = ?", access, StatusApplying, s.now().UTC(), id)
	return err
}

func (s *store) DeleteGrant(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM "+s.spec.Tables.Grants+" WHERE id = ?", id)
	return err
}

func (s *store) DeleteGrantsForUser(ctx context.Context, userID string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM "+s.spec.Tables.Grants+" WHERE "+s.spec.Columns.UserFK+" = ?", userID)
	return err
}

func (s *store) ListRestorePoints(ctx context.Context, databaseID string) ([]restorePointRow, error) {
	rows := []restorePointRow{}
	query, args := s.restorePointSelect()+" ORDER BY created_at DESC", []any{}
	if databaseID != "" {
		query, args = s.restorePointSelect()+" WHERE database_id = ? ORDER BY created_at DESC", []any{databaseID}
	}
	err := s.db.NewRaw(query, args...).Scan(ctx, &rows)
	return rows, err
}

func (s *store) GetRestorePoint(ctx context.Context, id string) (restorePointRow, error) {
	var row restorePointRow
	err := s.db.NewRaw(s.restorePointSelect()+" WHERE id = ?", id).Scan(ctx, &row)
	return row, err
}

func (s *store) InsertRestorePoint(ctx context.Context, row restorePointRow) error {
	_, err := s.db.ExecContext(ctx, "INSERT INTO "+s.spec.Tables.RestorePoints+" (id, database_id, status, path, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)",
		row.ID, row.DatabaseID, row.Status, row.Path, row.CreatedAt, row.UpdatedAt)
	return err
}

func (s *store) MarkRestorePointVerified(ctx context.Context, id, sha256 string, sizeBytes int64, verifiedAt time.Time) error {
	_, err := s.db.ExecContext(ctx, "UPDATE "+s.spec.Tables.RestorePoints+" SET status = ?, sha256 = ?, size_bytes = ?, verified_at = ?, failure = NULL, updated_at = ? WHERE id = ?",
		StatusVerified, sha256, sizeBytes, verifiedAt, s.now().UTC(), id)
	return err
}

func (s *store) resourceTable(resourceType string) (string, error) {
	switch resourceType {
	case resourceServer:
		return s.spec.Tables.Servers, nil
	case resourceUser:
		return s.spec.Tables.Users, nil
	case resourceDatabase:
		return s.spec.Tables.Databases, nil
	case resourceGrant:
		return s.spec.Tables.Grants, nil
	case resourceRestorePoint:
		return s.spec.Tables.RestorePoints, nil
	default:
		return "", errors.New("database resource type is unsupported")
	}
}

func (s *store) ResourceExists(ctx context.Context, resourceType, id string) (bool, error) {
	table, err := s.resourceTable(resourceType)
	if err != nil {
		return false, err
	}
	var count int
	if err := s.db.NewRaw("SELECT COUNT(*) FROM "+table+" WHERE id = ?", id).Scan(ctx, &count); err != nil {
		return false, err
	}
	return count > 0, nil
}

func (s *store) ResourceStatus(ctx context.Context, resourceType, id string) (Status, error) {
	table, err := s.resourceTable(resourceType)
	if err != nil {
		return "", err
	}
	var status string
	err = s.db.NewRaw("SELECT status FROM "+table+" WHERE id = ?", id).Scan(ctx, &status)
	return Status(status), err
}

func (s *store) SetResourceStatus(ctx context.Context, resourceType, id string, status Status, failure *string) (sql.Result, error) {
	table, err := s.resourceTable(resourceType)
	if err != nil {
		return nil, err
	}
	return s.db.ExecContext(ctx, "UPDATE "+table+" SET status = ?, failure = ?, updated_at = ? WHERE id = ?", status, failure, s.now().UTC(), id)
}

func (s *store) AttachJob(ctx context.Context, resourceType, id string, jobID int64, status Status) (sql.Result, error) {
	table, err := s.resourceTable(resourceType)
	if err != nil {
		return nil, err
	}
	return s.db.ExecContext(ctx, "UPDATE "+table+" SET status = ?, last_job_id = ?, failure = NULL, updated_at = ? WHERE id = ?", status, jobID, s.now().UTC(), id)
}
