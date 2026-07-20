package backups

import (
	"context"
	"errors"
	"net/http"
	"strings"

	backupoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/backups"
)

// errAccountInUse prevents storage credentials from disappearing while a plan
// or stored copy still depends on them.
var errAccountInUse = errors.New("this account is still used by one or more backup plans")

// validationError carries a client-facing 400 message out of the business layer.
type validationError struct{ message string }

func (e validationError) Error() string { return e.message }

func writeAccountError(w http.ResponseWriter, err error) {
	var invalid validationError
	if errors.As(err, &invalid) {
		writeError(w, http.StatusUnprocessableEntity, "backup_account_invalid", invalid.message)
		return
	}
	writeError(w, http.StatusInternalServerError, "backup_account_failed", "The backup account could not be saved.")
}

func (m *Module) CreateAccount(ctx context.Context, request AccountRequest) (Account, error) {
	request.Name = strings.TrimSpace(request.Name)
	request.Path = strings.TrimSpace(request.Path)
	if err := validateAccount(request); err != nil {
		return Account{}, validationError{err.Error()}
	}
	id := randomID()
	ciphertext, err := m.encryptSecret(id, request.Secret)
	if err != nil {
		return Account{}, err
	}
	configJSON, err := marshalConfig(request.Config)
	if err != nil {
		return Account{}, err
	}
	now := m.now().UTC()
	model := &accountModel{
		ID: id, Name: request.Name, Type: request.Type, Path: request.Path,
		ConfigJSON: configJSON, SecretCiphertext: ciphertext, CreatedAt: now, UpdatedAt: now,
	}
	if _, err := m.database.NewInsert().Model(model).Exec(ctx); err != nil {
		if isUniqueViolation(err) {
			return Account{}, validationError{"a backup account with that name already exists"}
		}
		return Account{}, err
	}
	return model.toAccount(), nil
}

func (m *Module) UpdateAccount(ctx context.Context, id string, request AccountRequest) (Account, error) {
	model, err := m.getAccountModel(ctx, id)
	if err != nil {
		return Account{}, err
	}
	request.Name = strings.TrimSpace(request.Name)
	request.Path = strings.TrimSpace(request.Path)
	if err := validateAccount(request); err != nil {
		return Account{}, validationError{err.Error()}
	}
	configJSON, err := marshalConfig(request.Config)
	if err != nil {
		return Account{}, err
	}
	model.Name = request.Name
	model.Type = request.Type
	model.Path = request.Path
	model.ConfigJSON = configJSON
	// An empty Secret map means "leave the stored secret untouched"; any keys
	// present replace it wholesale.
	if len(request.Secret) > 0 {
		ciphertext, err := m.encryptSecret(model.ID, request.Secret)
		if err != nil {
			return Account{}, err
		}
		model.SecretCiphertext = ciphertext
	}
	model.UpdatedAt = m.now().UTC()
	if _, err := m.database.NewUpdate().Model(model).WherePK().Exec(ctx); err != nil {
		if isUniqueViolation(err) {
			return Account{}, validationError{"a backup account with that name already exists"}
		}
		return Account{}, err
	}
	return model.toAccount(), nil
}

func (m *Module) DeleteAccount(ctx context.Context, id string) error {
	id = strings.TrimSpace(id)
	inUse, err := m.database.NewSelect().Model((*planModel)(nil)).Where("account_id = ?", id).Exists(ctx)
	if err != nil {
		return err
	}
	if inUse {
		return errAccountInUse
	}
	result, err := m.database.NewDelete().Model((*accountModel)(nil)).Where("id = ?", id).Exec(ctx)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return errors.New("backup account not found")
	}
	return nil
}

// TestAccount resolves the stored account and asks the agent to prove the
// storage is reachable and writable via rclone.
func (m *Module) TestAccount(ctx context.Context, model accountModel) (backupoperator.TestResult, error) {
	account, err := m.resolveAccount(model)
	if err != nil {
		return backupoperator.TestResult{}, err
	}
	return m.operator.TestAccount(ctx, account)
}

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "unique")
}
