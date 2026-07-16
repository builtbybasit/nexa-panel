package postgres

import (
	"errors"

	"strings"

	"github.com/nexa-panel/nexa-panel/internal/platform/jobs"

	"context"
	postgresoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/postgres"
)

func (m *Module) ListRoles(ctx context.Context, instanceID string) ([]Role, error) {
	models := []roleModel{}
	query := m.database.NewSelect().Model(&models).OrderExpr("name ASC")
	if instanceID != "" {
		query = query.Where("instance_id = ?", instanceID)
	}
	if err := query.Scan(ctx); err != nil {
		return nil, err
	}
	items := make([]Role, 0, len(models))
	for _, model := range models {
		items = append(items, model.toRole())
	}
	return items, nil
}

func (m *Module) CreateRole(ctx context.Context, request CreateRoleRequest, actor *string) (Role, jobs.Job, error) {
	request.InstanceID = strings.TrimSpace(request.InstanceID)
	request.Name = strings.ToLower(strings.TrimSpace(request.Name))
	if !resourceNamePattern.MatchString(request.Name) {
		return Role{}, jobs.Job{}, errors.New("role must contain 2-63 lowercase letters, numbers, or underscores")
	}
	if _, err := m.activeInstance(ctx, request.InstanceID); err != nil {
		return Role{}, jobs.Job{}, err
	}
	id := randomResourceID("role")
	secret, ciphertext, digest, err := m.newCredential(id)
	if err != nil {
		return Role{}, jobs.Job{}, err
	}
	defer clear(secret)
	now := m.now().UTC()
	model := &roleModel{ID: id, InstanceID: request.InstanceID, Name: request.Name, Status: string(StatusPlanning), PendingCredentialCiphertext: &ciphertext, PendingSecretDigest: &digest, CreatedAt: now, UpdatedAt: now}
	if _, err := m.database.NewInsert().Model(model).Exec(ctx); err != nil {
		return Role{}, jobs.Job{}, friendlyUnique(err, "role name is already managed on this instance")
	}
	job, err := m.submitPlan(ctx, resourceRole, id, postgresoperator.ActionCreateRole, actor)
	if err != nil {
		m.failResource(ctx, resourceRole, id, err)
		return model.toRole(), jobs.Job{}, err
	}
	model.LastJobID = &job.ID
	_, err = m.attachJob(ctx, resourceRole, id, job.ID, StatusPlanning)
	return model.toRole(), job, err
}

func (m *Module) RotateRole(ctx context.Context, id string, actor *string) (Role, jobs.Job, error) {
	model, err := m.getRoleModel(ctx, id)
	if err != nil {
		return Role{}, jobs.Job{}, err
	}
	if Status(model.Status) != StatusActive || model.CredentialCiphertext == nil {
		return Role{}, jobs.Job{}, errors.New("only an active role can rotate its credential")
	}
	secret, ciphertext, digest, err := m.newCredential(id)
	if err != nil {
		return Role{}, jobs.Job{}, err
	}
	defer clear(secret)
	now := m.now().UTC()
	_, err = m.database.NewUpdate().Model((*roleModel)(nil)).Set("pending_credential_ciphertext = ?", ciphertext).Set("pending_secret_digest = ?", digest).Set("status = ?", StatusPlanning).Set("failure = NULL").Set("updated_at = ?", now).Where("id = ?", id).Exec(ctx)
	if err != nil {
		return Role{}, jobs.Job{}, err
	}
	job, err := m.submitPlan(ctx, resourceRole, id, postgresoperator.ActionRotateRole, actor)
	if err != nil {
		m.failResource(ctx, resourceRole, id, err)
		return model.toRole(), jobs.Job{}, err
	}
	_, err = m.attachJob(ctx, resourceRole, id, job.ID, StatusPlanning)
	model.LastJobID, model.Status, model.UpdatedAt = &job.ID, string(StatusPlanning), now
	return model.toRole(), job, err
}

func (m *Module) RevealCredential(ctx context.Context, id string) (string, error) {
	model, err := m.getRoleModel(ctx, id)
	if err != nil {
		return "", err
	}
	if Status(model.Status) != StatusActive || model.CredentialCiphertext == nil || model.CredentialRevealed {
		return "", errors.New("credential is unavailable or has already been revealed")
	}
	plaintext, err := m.cipher.Decrypt(credentialLabel(id), *model.CredentialCiphertext)
	if err != nil {
		return "", err
	}
	result, err := m.database.NewUpdate().Model((*roleModel)(nil)).Set("credential_revealed = TRUE").Set("updated_at = ?", m.now().UTC()).Where("id = ?", id).Where("credential_revealed = FALSE").Exec(ctx)
	if err != nil {
		clear(plaintext)
		return "", err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		clear(plaintext)
		return "", errors.New("credential has already been revealed")
	}
	secret := string(plaintext)
	clear(plaintext)
	return secret, nil
}

func (m *Module) getRoleModel(ctx context.Context, id string) (roleModel, error) {
	var model roleModel
	err := m.database.NewSelect().Model(&model).Where("id = ?", id).Scan(ctx)
	return model, err
}
