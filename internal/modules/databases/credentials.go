package databases

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"unicode/utf8"
)

// ResolveAdminToolCredential resolves a database plus optional user to the
// login a containerized admin tool (phpMyAdmin, pgAdmin) signs in with. The
// engine key comes from the launch request and must match where the database
// actually lives, so a launch cannot cross engines.
func (m *Module) ResolveAdminToolCredential(ctx context.Context, engineKey, databaseID, userID string) (AdminToolCredential, error) {
	eng, err := m.engineByKey(engineKey)
	if err != nil {
		return AdminToolCredential{}, err
	}
	database, err := eng.store.GetDatabase(ctx, strings.TrimSpace(databaseID))
	if err != nil || Status(database.Status) != StatusActive {
		return AdminToolCredential{}, errors.New(eng.spec.DisplayName + " database must be active")
	}
	// A one-click launch from the database table sends no user, so log in as
	// the database's owner — the user that already holds privileges on it.
	userID = strings.TrimSpace(userID)
	if userID == "" {
		userID = database.OwnerUserID
	}
	user, err := eng.store.GetUser(ctx, userID)
	if err != nil || Status(user.Status) != StatusActive || user.ServerID != database.ServerID || user.CredentialCiphertext == nil {
		return AdminToolCredential{}, errors.New(eng.spec.DisplayName + " user must be active on the selected database server")
	}
	server, err := m.activeServer(ctx, eng, database.ServerID)
	if err != nil {
		return AdminToolCredential{}, err
	}
	secret, err := m.cipher.Decrypt(eng.spec.CredentialLabelPrefix+user.ID, *user.CredentialCiphertext)
	if err != nil {
		return AdminToolCredential{}, err
	}
	// The host each tool dials is an engine trait: phpMyAdmin receives the
	// host's MySQL socket read-only so managed user@localhost scopes keep
	// working, while pgAdmin reaches the instance over the container gateway.
	return AdminToolCredential{Host: eng.spec.AdminToolHost, Port: server.Port, Database: database.Name, Username: user.Name, Secret: secret}, nil
}

// sealPassword validates a client-chosen password and returns its ciphertext
// and digest. The password is generated (or typed) in the browser and applied
// to the engine as-is; the panel keeps only what admin-tool launches need.
func (m *Module) sealPassword(eng *engine, userID, password string) (ciphertext, digest string, err error) {
	if err := validatePassword(password); err != nil {
		return "", "", err
	}
	secret := []byte(password)
	ciphertext, err = m.cipher.Encrypt(eng.spec.CredentialLabelPrefix+userID, secret)
	if err != nil {
		clear(secret)
		return "", "", err
	}
	sum := sha256.Sum256(secret)
	clear(secret)
	return ciphertext, hex.EncodeToString(sum[:]), nil
}

func validatePassword(password string) error {
	length := utf8.RuneCountInString(password)
	if length < 8 {
		return errors.New("password must be at least 8 characters")
	}
	if length > 128 {
		return errors.New("password must be at most 128 characters")
	}
	return nil
}
