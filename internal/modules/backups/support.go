package backups

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/nexa-panel/nexa-panel/internal/platform/httpapi"
	backupoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/backups"
	"github.com/nexa-panel/nexa-panel/internal/platform/secureid"
)

var allowedTypes = map[string]struct{}{
	TypeLocal: {}, TypeS3: {}, TypeSFTP: {}, TypeDrive: {},
}

// accountLabel binds an account's ciphertext to its row so a secret blob can
// never be decrypted against a different account (GCM additional data).
func accountLabel(id string) string { return "backup-account:" + id }

func validateAccount(request AccountRequest) error {
	name := strings.TrimSpace(request.Name)
	if name == "" || len(name) > 80 {
		return errors.New("name must contain 1-80 characters")
	}
	if _, ok := allowedTypes[request.Type]; !ok {
		return errors.New("type must be one of local, s3, sftp, drive")
	}
	path := strings.TrimSpace(request.Path)
	switch request.Type {
	case TypeLocal:
		if !strings.HasPrefix(path, "/") {
			return errors.New("a local account path must be an absolute directory")
		}
	case TypeS3:
		if path == "" {
			return errors.New("an S3 account path must name a bucket (bucket or bucket/prefix)")
		}
	}
	return nil
}

// toAccount maps a stored row to its safe, secret-free JSON view.
func (model accountModel) toAccount() Account {
	config := map[string]string{}
	if model.ConfigJSON != "" {
		_ = json.Unmarshal([]byte(model.ConfigJSON), &config)
	}
	return Account{
		ID: model.ID, Name: model.Name, Type: model.Type, Path: model.Path,
		Config: config, HasSecret: model.SecretCiphertext != nil,
		CreatedAt: model.CreatedAt, UpdatedAt: model.UpdatedAt,
	}
}

// resolveAccount decrypts the row's secrets and merges config + secret into the
// flat param map the operator hands to rclone.
func (m *Module) resolveAccount(model accountModel) (backupoperator.Account, error) {
	params := map[string]string{}
	if model.ConfigJSON != "" {
		if err := json.Unmarshal([]byte(model.ConfigJSON), &params); err != nil {
			return backupoperator.Account{}, errors.New("stored account config is corrupt")
		}
	}
	if model.SecretCiphertext != nil {
		plaintext, err := m.cipher.Decrypt(accountLabel(model.ID), *model.SecretCiphertext)
		if err != nil {
			return backupoperator.Account{}, err
		}
		secret := map[string]string{}
		if err := json.Unmarshal(plaintext, &secret); err != nil {
			return backupoperator.Account{}, errors.New("stored account secret is corrupt")
		}
		for key, value := range secret {
			params[key] = value
		}
	}
	return backupoperator.Account{Name: model.Name, Type: model.Type, Path: model.Path, Params: params}, nil
}

// encryptSecret marshals and encrypts a non-empty secret map for an account.
func (m *Module) encryptSecret(id string, secret map[string]string) (*string, error) {
	if len(secret) == 0 {
		return nil, nil
	}
	plaintext, err := json.Marshal(secret)
	if err != nil {
		return nil, err
	}
	ciphertext, err := m.cipher.Encrypt(accountLabel(id), plaintext)
	if err != nil {
		return nil, err
	}
	return &ciphertext, nil
}

func marshalConfig(config map[string]string) (string, error) {
	if config == nil {
		config = map[string]string{}
	}
	encoded, err := json.Marshal(config)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func randomID() string { return "bkacct_" + randomToken() }

func randomToken() string { return secureid.Hex(12) }

func decodeJSON(w http.ResponseWriter, r *http.Request, destination any) error {
	return httpapi.DecodeJSONLimit(w, r, destination, 32*1024)
}

var (
	writeJSON  = httpapi.WriteJSON
	writeError = httpapi.WriteError
)

// getAccountModel is shared by the get/update/delete/test handlers.
func (m *Module) getAccountModel(ctx context.Context, id string) (*accountModel, error) {
	model := new(accountModel)
	err := m.database.NewSelect().Model(model).Where("id = ?", strings.TrimSpace(id)).Scan(ctx)
	return model, err
}
