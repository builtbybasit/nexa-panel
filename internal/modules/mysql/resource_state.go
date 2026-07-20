package mysql

import (
	"context"
	"database/sql"
	"errors"
)

func (m *Module) resourceStatus(ctx context.Context, resourceType, id string) (Status, error) {
	table, err := resourceTable(resourceType)
	if err != nil {
		return "", err
	}
	var status string
	err = m.database.NewRaw("SELECT status FROM "+table+" WHERE id = ?", id).Scan(ctx, &status)
	return Status(status), err
}

func (m *Module) setResourceStatus(ctx context.Context, resourceType, id string, status Status, failure *string) (sql.Result, error) {
	table, err := resourceTable(resourceType)
	if err != nil {
		return nil, err
	}
	return m.database.ExecContext(ctx, "UPDATE "+table+" SET status = ?, failure = ?, updated_at = ? WHERE id = ?", status, failure, m.now().UTC(), id)
}

func (m *Module) attachJob(ctx context.Context, resourceType, id string, jobID int64, status Status) (sql.Result, error) {
	table, err := resourceTable(resourceType)
	if err != nil {
		return nil, err
	}
	return m.database.ExecContext(ctx, "UPDATE "+table+" SET status = ?, last_job_id = ?, failure = NULL, updated_at = ? WHERE id = ?", status, jobID, m.now().UTC(), id)
}

func (m *Module) failResource(ctx context.Context, resourceType, id string, failure error) {
	message := failure.Error()
	if len(message) > 300 {
		message = message[:300]
	}
	if resourceType == resourceAccount {
		model, err := m.getAccountModel(ctx, id)
		if err == nil && model.CredentialCiphertext != nil {
			_, _ = m.database.NewUpdate().Model((*accountModel)(nil)).Set("status = ?", StatusActive).Set("pending_credential_ciphertext = NULL").Set("pending_secret_digest = NULL").Set("failure = ?", message).Set("updated_at = ?", m.now().UTC()).Where("id = ?", id).Exec(ctx)
			return
		}
	}
	if resourceType == resourceRestorePoint {
		model, err := m.getRestorePointModel(ctx, id)
		if err == nil && model.VerifiedAt != nil {
			_, _ = m.setResourceStatus(ctx, resourceType, id, StatusVerified, &message)
			return
		}
	}
	_, _ = m.setResourceStatus(ctx, resourceType, id, StatusFailed, &message)
}

func resourceTable(resourceType string) (string, error) {
	switch resourceType {
	case resourceEngine:
		return "mysql_family_engines", nil
	case resourceAccount:
		return "mysql_accounts", nil
	case resourceDatabase:
		return "mysql_databases", nil
	case resourceGrant:
		return "mysql_grants", nil
	case resourceRestorePoint:
		return "mysql_restore_points", nil
	default:
		return "", errors.New("MySQL-family resource type is unsupported")
	}
}
