package mysql

import (
	"context"

	"github.com/nexa-panel/nexa-panel/internal/platform/jobs"

	"errors"
	mysqloperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/mysql"

	"strings"
)

func (m *Module) ListAccounts(ctx context.Context, engineID string) ([]Account, error) {
	models := []accountModel{}
	query := m.database.NewSelect().Model(&models).OrderExpr("name ASC")
	if engineID != "" {
		query = query.Where("engine_id = ?", engineID)
	}
	if err := query.Scan(ctx); err != nil {
		return nil, err
	}
	items := make([]Account, 0, len(models))
	for _, model := range models {
		items = append(items, model.toAccount())
	}
	return items, nil
}

func (m *Module) CreateAccount(ctx context.Context, request CreateAccountRequest, actor *string) (Account, jobs.Job, error) {
	request.EngineID = strings.TrimSpace(request.EngineID)
	request.Name = strings.ToLower(strings.TrimSpace(request.Name))
	request.Host = strings.TrimSpace(request.Host)
	if request.Host == "" {
		request.Host = "localhost"
	}
	if !resourceNamePattern.MatchString(request.Name) {
		return Account{}, jobs.Job{}, errors.New("account must contain 2-63 lowercase letters, numbers, or underscores")
	}
	if _, err := m.activeEngine(ctx, request.EngineID); err != nil {
		return Account{}, jobs.Job{}, err
	}
	id := randomResourceID("account")
	secret, ciphertext, digest, err := m.newCredential(id)
	if err != nil {
		return Account{}, jobs.Job{}, err
	}
	defer clear(secret)
	now := m.now().UTC()
	model := &accountModel{ID: id, EngineID: request.EngineID, Name: request.Name, Host: request.Host, Status: string(StatusPlanning), PendingCredentialCiphertext: &ciphertext, PendingSecretDigest: &digest, CreatedAt: now, UpdatedAt: now}
	if _, err := m.database.NewInsert().Model(model).Exec(ctx); err != nil {
		return Account{}, jobs.Job{}, friendlyUnique(err, "account name is already managed on this engine")
	}
	job, err := m.submitPlan(ctx, resourceAccount, id, mysqloperator.ActionCreateAccount, actor)
	if err != nil {
		m.failResource(ctx, resourceAccount, id, err)
		return model.toAccount(), jobs.Job{}, err
	}
	model.LastJobID = &job.ID
	_, err = m.attachJob(ctx, resourceAccount, id, job.ID, StatusPlanning)
	return model.toAccount(), job, err
}

func (m *Module) RotateAccount(ctx context.Context, id string, actor *string) (Account, jobs.Job, error) {
	model, err := m.getAccountModel(ctx, id)
	if err != nil {
		return Account{}, jobs.Job{}, err
	}
	if Status(model.Status) != StatusActive || model.CredentialCiphertext == nil {
		return Account{}, jobs.Job{}, errors.New("only an active account can rotate its credential")
	}
	secret, ciphertext, digest, err := m.newCredential(id)
	if err != nil {
		return Account{}, jobs.Job{}, err
	}
	defer clear(secret)
	now := m.now().UTC()
	_, err = m.database.NewUpdate().Model((*accountModel)(nil)).Set("pending_credential_ciphertext = ?", ciphertext).Set("pending_secret_digest = ?", digest).Set("status = ?", StatusPlanning).Set("failure = NULL").Set("updated_at = ?", now).Where("id = ?", id).Exec(ctx)
	if err != nil {
		return Account{}, jobs.Job{}, err
	}
	job, err := m.submitPlan(ctx, resourceAccount, id, mysqloperator.ActionRotateAccount, actor)
	if err != nil {
		m.failResource(ctx, resourceAccount, id, err)
		return model.toAccount(), jobs.Job{}, err
	}
	_, err = m.attachJob(ctx, resourceAccount, id, job.ID, StatusPlanning)
	model.LastJobID, model.Status, model.UpdatedAt = &job.ID, string(StatusPlanning), now
	return model.toAccount(), job, err
}

func (m *Module) RevealCredential(ctx context.Context, id string) (string, error) {
	model, err := m.getAccountModel(ctx, id)
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
	result, err := m.database.NewUpdate().Model((*accountModel)(nil)).Set("credential_revealed = TRUE").Set("updated_at = ?", m.now().UTC()).Where("id = ?", id).Where("credential_revealed = FALSE").Exec(ctx)
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

func (m *Module) getAccountModel(ctx context.Context, id string) (accountModel, error) {
	var model accountModel
	err := m.database.NewSelect().Model(&model).Where("id = ?", id).Scan(ctx)
	return model, err
}
