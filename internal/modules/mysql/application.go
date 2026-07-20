package mysql

import (
	"context"
	"encoding/json"
	"errors"

	mysqloperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/mysql"
)

func (m *Module) applyJob(ctx context.Context, raw json.RawMessage, report func(int, string) error) (any, error) {
	var request struct {
		PlanID string `json:"planId"`
	}
	if json.Unmarshal(raw, &request) != nil || request.PlanID == "" {
		return nil, errors.New("invalid MySQL-family apply request")
	}
	model := new(planModel)
	if err := m.database.NewSelect().Model(model).Where("id = ?", request.PlanID).Scan(ctx); err != nil {
		return nil, err
	}
	stored, err := model.toStoredPlan()
	if err != nil {
		return nil, err
	}
	if err := report(20, "Executing the signed MySQL-family plan."); err != nil {
		return nil, err
	}
	var secret string
	if stored.Operation == mysqloperator.ActionCreateAccount || stored.Operation == mysqloperator.ActionRotateAccount {
		account, getErr := m.getAccountModel(ctx, stored.ResourceID)
		if getErr != nil || account.PendingCredentialCiphertext == nil {
			return nil, errors.New("pending MySQL-family credential is unavailable")
		}
		plaintext, decryptErr := m.cipher.Decrypt(credentialLabel(account.ID), *account.PendingCredentialCiphertext)
		if decryptErr != nil {
			return nil, decryptErr
		}
		secret = string(plaintext)
		clear(plaintext)
		defer func() { secret = "" }()
	}
	observation, err := m.operator.Apply(ctx, mysqloperator.Execution{Plan: stored.AgentPlan, Secret: secret})
	secret = ""
	if err != nil {
		m.failResource(context.WithoutCancel(ctx), stored.ResourceType, stored.ResourceID, err)
		return nil, err
	}
	if err := report(85, "Persisting verified MySQL-family observed state."); err != nil {
		return nil, err
	}
	if err := m.commitObservation(ctx, stored, observation); err != nil {
		return nil, err
	}
	return observation, nil
}

func (m *Module) changeFor(ctx context.Context, resourceType, resourceID string, action mysqloperator.Action) (mysqloperator.Change, error) {
	switch resourceType {
	case resourceEngine:
		return mysqloperator.Change{}, errors.New("MySQL-family engines are discovered, not changed through resource plans")
	case resourceAccount:
		account, err := m.getAccountModel(ctx, resourceID)
		if err != nil {
			return mysqloperator.Change{}, err
		}
		change := mysqloperator.Change{Action: action, EngineID: account.EngineID, Account: account.Name, AccountHost: account.Host}
		if action == mysqloperator.ActionCreateAccount || action == mysqloperator.ActionRotateAccount {
			if account.PendingSecretDigest == nil {
				return mysqloperator.Change{}, errors.New("pending account credential is unavailable")
			}
			change.SecretSHA256 = *account.PendingSecretDigest
		}
		return change, nil
	case resourceDatabase:
		database, err := m.getDatabaseModel(ctx, resourceID)
		if err != nil {
			return mysqloperator.Change{}, err
		}
		change := mysqloperator.Change{Action: action, EngineID: database.EngineID, Database: database.Name}
		// Dropping needs only the schema name; the owner account may already be
		// gone and is irrelevant to DROP DATABASE.
		if action != mysqloperator.ActionDropDatabase {
			owner, err := m.getAccountModel(ctx, database.OwnerAccountID)
			if err != nil {
				return mysqloperator.Change{}, err
			}
			change.Account, change.AccountHost = owner.Name, owner.Host
		}
		return change, nil
	case resourceGrant:
		grant, err := m.getGrantModel(ctx, resourceID)
		if err != nil {
			return mysqloperator.Change{}, err
		}
		database, err := m.getDatabaseModel(ctx, grant.DatabaseID)
		if err != nil {
			return mysqloperator.Change{}, err
		}
		account, err := m.getAccountModel(ctx, grant.AccountID)
		if err != nil {
			return mysqloperator.Change{}, err
		}
		return mysqloperator.Change{Action: action, EngineID: database.EngineID, Database: database.Name, Account: account.Name, AccountHost: account.Host, Access: mysqloperator.AccessLevel(grant.Access)}, nil
	case resourceRestorePoint:
		point, err := m.getRestorePointModel(ctx, resourceID)
		if err != nil {
			return mysqloperator.Change{}, err
		}
		database, err := m.getDatabaseModel(ctx, point.DatabaseID)
		if err != nil {
			return mysqloperator.Change{}, err
		}
		change := mysqloperator.Change{Action: action, EngineID: database.EngineID, Database: database.Name, BackupID: point.ID, BackupPath: point.Path}
		if action == mysqloperator.ActionRestoreBackup {
			if point.SHA256 == nil {
				return mysqloperator.Change{}, errors.New("restore point checksum is unavailable")
			}
			change.BackupSHA256, change.RestoreToken = *point.SHA256, randomToken(12)
		}
		return change, nil
	default:
		return mysqloperator.Change{}, errors.New("MySQL-family resource type is unsupported")
	}
}

func (m *Module) commitObservation(ctx context.Context, stored StoredPlan, observation mysqloperator.Observation) error {
	now := m.now().UTC()
	if isDropOperation(stored.Operation) {
		return m.commitDrop(ctx, stored)
	}
	switch stored.ResourceType {
	case resourceAccount:
		_, err := m.database.NewUpdate().Model((*accountModel)(nil)).Set("credential_ciphertext = pending_credential_ciphertext").Set("pending_credential_ciphertext = NULL").Set("pending_secret_digest = NULL").Set("credential_revealed = FALSE").Set("credential_version = credential_version + 1").Set("status = ?", StatusActive).Set("failure = NULL").Set("updated_at = ?", now).Where("id = ?", stored.ResourceID).Exec(ctx)
		return err
	case resourceDatabase, resourceGrant:
		_, err := m.setResourceStatus(ctx, stored.ResourceType, stored.ResourceID, StatusActive, nil)
		return err
	case resourceRestorePoint:
		if stored.Operation == mysqloperator.ActionCreateBackup {
			if observation.Backup == nil || !observation.Backup.Verified {
				return errors.New("MySQL-family backup verification is missing")
			}
			_, err := m.database.NewUpdate().Model((*restorePointModel)(nil)).Set("status = ?", StatusVerified).Set("sha256 = ?", observation.Backup.SHA256).Set("size_bytes = ?", observation.Backup.SizeBytes).Set("verified_at = ?", observation.Backup.CreatedAt).Set("failure = NULL").Set("updated_at = ?", now).Where("id = ?", stored.ResourceID).Exec(ctx)
			return err
		}
		point, err := m.getRestorePointModel(ctx, stored.ResourceID)
		if err != nil {
			return err
		}
		_, err = m.database.NewUpdate().Model((*databaseModel)(nil)).Set("status = ?", StatusActive).Set("failure = NULL").Set("updated_at = ?", now).Where("id = ?", point.DatabaseID).Exec(ctx)
		if err != nil {
			return err
		}
		_, err = m.setResourceStatus(ctx, resourceRestorePoint, stored.ResourceID, StatusVerified, nil)
		return err
	default:
		return errors.New("MySQL-family observation resource is unsupported")
	}
}
