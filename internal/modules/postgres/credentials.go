package postgres

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
)

func (m *Module) ResolveAdminToolCredential(ctx context.Context, databaseID, roleID string) (AdminToolCredential, error) {
	database, err := m.getDatabaseModel(ctx, strings.TrimSpace(databaseID))
	if err != nil || Status(database.Status) != StatusActive {
		return AdminToolCredential{}, errors.New("PostgreSQL database must be active")
	}
	// A one-click launch from the database table sends no role, so log in as the
	// database's owner role — the role that already holds privileges on it.
	roleID = strings.TrimSpace(roleID)
	if roleID == "" {
		roleID = database.OwnerRoleID
	}
	role, err := m.getRoleModel(ctx, roleID)
	if err != nil || Status(role.Status) != StatusActive || role.InstanceID != database.InstanceID || role.CredentialCiphertext == nil {
		return AdminToolCredential{}, errors.New("PostgreSQL role must be active on the selected database instance")
	}
	instance, err := m.activeInstance(ctx, database.InstanceID)
	if err != nil {
		return AdminToolCredential{}, err
	}
	secret, err := m.cipher.Decrypt(credentialLabel(role.ID), *role.CredentialCiphertext)
	if err != nil {
		return AdminToolCredential{}, err
	}
	return AdminToolCredential{Host: "host.containers.internal", Port: instance.Port, Database: database.Name, Username: role.Name, Secret: secret}, nil
}

func (m *Module) newCredential(roleID string) ([]byte, string, string, error) {
	raw := make([]byte, 32)
	rand.Read(raw)
	secret := []byte(base64.RawURLEncoding.EncodeToString(raw))
	clear(raw)
	ciphertext, err := m.cipher.Encrypt(credentialLabel(roleID), secret)
	if err != nil {
		clear(secret)
		return nil, "", "", err
	}
	digest := sha256.Sum256(secret)
	return secret, ciphertext, hex.EncodeToString(digest[:]), nil
}

func credentialLabel(id string) string { return "postgres-role:" + id }
