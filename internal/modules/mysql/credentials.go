package mysql

import (
	"context"

	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"

	"crypto/rand"
	"errors"

	"strings"
)

func (m *Module) ResolveAdminToolCredential(ctx context.Context, databaseID, accountID string) (AdminToolCredential, error) {
	database, err := m.getDatabaseModel(ctx, strings.TrimSpace(databaseID))
	if err != nil || Status(database.Status) != StatusActive {
		return AdminToolCredential{}, errors.New("MySQL-family database must be active")
	}
	account, err := m.getAccountModel(ctx, strings.TrimSpace(accountID))
	if err != nil || Status(account.Status) != StatusActive || account.EngineID != database.EngineID || account.CredentialCiphertext == nil {
		return AdminToolCredential{}, errors.New("MySQL-family account must be active on the selected database engine")
	}
	engine, err := m.activeEngine(ctx, database.EngineID)
	if err != nil {
		return AdminToolCredential{}, err
	}
	secret, err := m.cipher.Decrypt(credentialLabel(account.ID), *account.CredentialCiphertext)
	if err != nil {
		return AdminToolCredential{}, err
	}
	return AdminToolCredential{Host: "host.containers.internal", Port: engine.Port, Database: database.Name, Username: account.Name, Secret: secret}, nil
}

func (m *Module) newCredential(accountID string) ([]byte, string, string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, "", "", err
	}
	secret := []byte(base64.RawURLEncoding.EncodeToString(raw))
	clear(raw)
	ciphertext, err := m.cipher.Encrypt(credentialLabel(accountID), secret)
	if err != nil {
		clear(secret)
		return nil, "", "", err
	}
	digest := sha256.Sum256(secret)
	return secret, ciphertext, hex.EncodeToString(digest[:]), nil
}

func credentialLabel(id string) string { return "mysql-account:" + id }
