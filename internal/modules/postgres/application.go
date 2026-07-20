package postgres

import (
	"context"
	"encoding/json"
	"errors"

	postgresoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/postgres"
)

func (m *Module) applyJob(ctx context.Context, raw json.RawMessage, report func(int, string) error) (any, error) {
	var request struct {
		PlanID string `json:"planId"`
	}
	if json.Unmarshal(raw, &request) != nil || request.PlanID == "" {
		return nil, errors.New("invalid PostgreSQL apply request")
	}
	model := new(planModel)
	if err := m.database.NewSelect().Model(model).Where("id = ?", request.PlanID).Scan(ctx); err != nil {
		return nil, err
	}
	stored, err := model.toStoredPlan()
	if err != nil {
		return nil, err
	}
	if err := report(20, "Executing the signed PostgreSQL plan."); err != nil {
		return nil, err
	}
	var secret string
	if stored.Operation == postgresoperator.ActionCreateRole || stored.Operation == postgresoperator.ActionRotateRole {
		role, getErr := m.getRoleModel(ctx, stored.ResourceID)
		if getErr != nil || role.PendingCredentialCiphertext == nil {
			return nil, errors.New("pending PostgreSQL credential is unavailable")
		}
		plaintext, decryptErr := m.cipher.Decrypt(credentialLabel(role.ID), *role.PendingCredentialCiphertext)
		if decryptErr != nil {
			return nil, decryptErr
		}
		secret = string(plaintext)
		clear(plaintext)
		defer func() { secret = "" }()
	}
	observation, err := m.operator.Apply(ctx, postgresoperator.Execution{Plan: stored.AgentPlan, Secret: secret})
	secret = ""
	if err != nil {
		m.failResource(context.WithoutCancel(ctx), stored.ResourceType, stored.ResourceID, err)
		return nil, err
	}
	if err := report(85, "Persisting verified PostgreSQL observed state."); err != nil {
		return nil, err
	}
	if err := m.commitObservation(ctx, stored, observation); err != nil {
		return nil, err
	}
	return observation, nil
}

func (m *Module) changeFor(ctx context.Context, resourceType, resourceID string, action postgresoperator.Action) (postgresoperator.Change, error) {
	switch resourceType {
	case resourceInstance:
		instance, err := m.getInstanceModel(ctx, resourceID)
		if err != nil {
			return postgresoperator.Change{}, err
		}
		return postgresoperator.Change{Action: action, InstanceID: instance.ID, Version: instance.Version, Cluster: instance.ClusterName, Port: instance.Port}, nil
	case resourceRole:
		role, err := m.getRoleModel(ctx, resourceID)
		if err != nil || role.PendingSecretDigest == nil {
			return postgresoperator.Change{}, errors.New("pending role credential is unavailable")
		}
		return postgresoperator.Change{Action: action, InstanceID: role.InstanceID, Role: role.Name, SecretSHA256: *role.PendingSecretDigest}, nil
	case resourceDatabase:
		database, err := m.getDatabaseModel(ctx, resourceID)
		if err != nil {
			return postgresoperator.Change{}, err
		}
		owner, err := m.getRoleModel(ctx, database.OwnerRoleID)
		if err != nil {
			return postgresoperator.Change{}, err
		}
		return postgresoperator.Change{Action: action, InstanceID: database.InstanceID, Database: database.Name, OwnerRole: owner.Name}, nil
	case resourceGrant:
		grant, err := m.getGrantModel(ctx, resourceID)
		if err != nil {
			return postgresoperator.Change{}, err
		}
		database, err := m.getDatabaseModel(ctx, grant.DatabaseID)
		if err != nil {
			return postgresoperator.Change{}, err
		}
		owner, err := m.getRoleModel(ctx, database.OwnerRoleID)
		if err != nil {
			return postgresoperator.Change{}, err
		}
		role, err := m.getRoleModel(ctx, grant.RoleID)
		if err != nil {
			return postgresoperator.Change{}, err
		}
		return postgresoperator.Change{Action: action, InstanceID: database.InstanceID, Database: database.Name, OwnerRole: owner.Name, Role: role.Name, Access: postgresoperator.AccessLevel(grant.Access)}, nil
	case resourceRestorePoint:
		point, err := m.getRestorePointModel(ctx, resourceID)
		if err != nil {
			return postgresoperator.Change{}, err
		}
		database, err := m.getDatabaseModel(ctx, point.DatabaseID)
		if err != nil {
			return postgresoperator.Change{}, err
		}
		owner, err := m.getRoleModel(ctx, database.OwnerRoleID)
		if err != nil {
			return postgresoperator.Change{}, err
		}
		change := postgresoperator.Change{Action: action, InstanceID: database.InstanceID, Database: database.Name, OwnerRole: owner.Name, BackupID: point.ID, BackupPath: point.Path}
		if action == postgresoperator.ActionRestoreBackup {
			if point.SHA256 == nil {
				return postgresoperator.Change{}, errors.New("restore point checksum is unavailable")
			}
			change.BackupSHA256, change.RestoreToken = *point.SHA256, randomToken(12)
		}
		return change, nil
	default:
		return postgresoperator.Change{}, errors.New("PostgreSQL resource type is unsupported")
	}
}

func (m *Module) commitObservation(ctx context.Context, stored StoredPlan, observation postgresoperator.Observation) error {
	now := m.now().UTC()
	switch stored.ResourceType {
	case resourceInstance:
		if observation.Instance == nil {
			return errors.New("PostgreSQL instance observation is missing")
		}
		item := observation.Instance
		_, err := m.database.NewUpdate().Model((*instanceModel)(nil)).Set("version = ?", item.Version).Set("cluster_name = ?", item.Cluster).Set("port = ?", item.Port).Set("status = ?", StatusActive).Set("owner = ?", item.Owner).Set("data_path = ?", item.DataPath).Set("socket_path = ?", item.SocketPath).Set("log_path = ?", item.LogPath).Set("config_path = ?", item.ConfigPath).Set("systemd_unit = ?", item.SystemdUnit).Set("managed_by_nexa = ?", item.ManagedByNexa).Set("failure = NULL").Set("updated_at = ?", now).Where("id = ?", stored.ResourceID).Exec(ctx)
		return err
	case resourceRole:
		_, err := m.database.NewUpdate().Model((*roleModel)(nil)).Set("credential_ciphertext = pending_credential_ciphertext").Set("pending_credential_ciphertext = NULL").Set("pending_secret_digest = NULL").Set("credential_revealed = FALSE").Set("credential_version = credential_version + 1").Set("status = ?", StatusActive).Set("failure = NULL").Set("updated_at = ?", now).Where("id = ?", stored.ResourceID).Exec(ctx)
		return err
	case resourceDatabase, resourceGrant:
		_, err := m.setResourceStatus(ctx, stored.ResourceType, stored.ResourceID, StatusActive, nil)
		return err
	case resourceRestorePoint:
		if stored.Operation == postgresoperator.ActionCreateBackup {
			if observation.Backup == nil || !observation.Backup.Verified {
				return errors.New("PostgreSQL backup verification is missing")
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
		return errors.New("PostgreSQL observation resource is unsupported")
	}
}
