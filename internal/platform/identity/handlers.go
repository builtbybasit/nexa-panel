package identity

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"github.com/nexa-panel/nexa-panel/internal/platform/audit"

	"github.com/uptrace/bun"
)

func (m *Module) statusHTTP(w http.ResponseWriter, r *http.Request) {
	bootstrapRequired, err := m.bootstrapRequired(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "identity_unavailable", "Identity status could not be loaded.")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	response := map[string]any{
		"bootstrapRequired": bootstrapRequired, "authenticated": false,
		"mfaEnabled": false, "mfaChallengeRequired": false, "mfaEnrollmentRequired": false,
	}
	// Tell the setup UI whether this caller must present the out-of-band bootstrap
	// token: a loopback operator may bootstrap directly, a remote one must supply
	// the token the server printed to its owner-only file.
	if bootstrapRequired {
		response["bootstrapTokenRequired"] = !m.bootstrapRequestAllowed(r, "")
	}
	if person, err := m.authenticate(r.Context(), r); err == nil {
		response["user"] = person.User
		enrolled := person.TOTPConfirmedAt != nil
		response["mfaEnabled"] = enrolled
		// MFA is optional, so enrollment is never demanded. An enrolled account
		// must still complete its outstanding challenge; anything else is
		// authenticated on the password session alone.
		if enrolled && person.MFAVerifiedAt == nil {
			response["mfaChallengeRequired"] = true
		} else {
			response["authenticated"] = true
		}
	}
	writeJSON(w, http.StatusOK, response)
}

type bootstrapRequest struct {
	credentials
	// BootstrapToken is the body-field alternative to the X-Nexa-Bootstrap-Token
	// header for callers that cannot set custom headers.
	BootstrapToken string `json:"bootstrapToken,omitempty"`
}

func (m *Module) bootstrapHTTP(w http.ResponseWriter, r *http.Request) {
	var input bootstrapRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	// An unauthenticated REMOTE client must never be able to claim the admin
	// account on a public bind: require either loopback access or the bootstrap
	// token before any account is created.
	if !m.bootstrapRequestAllowed(r, input.BootstrapToken) {
		m.recordAudit(r.Context(), audit.Entry{Action: "identity.bootstrap", Subject: "user:pending", RemoteAddress: remoteAddress(r), Metadata: map[string]any{"result": "forbidden"}})
		writeError(w, http.StatusForbidden, "bootstrap_forbidden", "First-run setup must be proven from the server: supply the bootstrap token or connect from the local host.")
		return
	}
	creds := input.credentials
	creds.Username = strings.TrimSpace(creds.Username)
	if err := validateCredentials(creds); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid_credentials", err.Error())
		return
	}
	passwordHash, err := hashPassword(creds.Password, m.parameters)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "identity_unavailable", "Administrator could not be created.")
		return
	}

	user := User{ID: randomID(16), Username: creds.Username, Role: "admin"}
	now := m.now().UTC()
	err = m.database.RunInTx(r.Context(), nil, func(ctx context.Context, tx bun.Tx) error {
		exists, err := tx.NewSelect().Model((*userModel)(nil)).Exists(ctx)
		if err != nil {
			return err
		}
		if exists {
			return errAlreadyBootstrapped
		}
		model := &userModel{ID: user.ID, Username: user.Username, PasswordHash: passwordHash, Role: user.Role, CreatedAt: now, RecoveryCodeHashes: "[]"}
		_, err = tx.NewInsert().Model(model).Exec(ctx)
		return err
	})
	if errors.Is(err, errAlreadyBootstrapped) {
		writeError(w, http.StatusConflict, "already_bootstrapped", "An administrator already exists.")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "identity_unavailable", "Administrator could not be created.")
		return
	}
	if err := m.startSession(w, r, user); err != nil {
		writeError(w, http.StatusInternalServerError, "session_unavailable", "Administrator was created, but a session could not be started.")
		return
	}
	// The administrator now exists: permanently close the bootstrap window and
	// remove the on-disk token so it can never be replayed.
	m.clearBootstrapToken()
	m.recordAudit(r.Context(), audit.Entry{ActorUserID: &user.ID, Action: "identity.bootstrap", Subject: "user:" + user.ID, RemoteAddress: remoteAddress(r)})
	// MFA is optional, so a freshly bootstrapped administrator is signed straight
	// in. They can enable a second factor later from account security.
	writeJSON(w, http.StatusCreated, map[string]any{"user": user, "next": "authenticated"})
}

func (m *Module) loginHTTP(w http.ResponseWriter, r *http.Request) {
	var input credentials
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	input.Username = strings.TrimSpace(input.Username)
	attemptKeys := loginAttemptKeys(input.Username, remoteAddress(r))
	if !m.allowAttempts(attemptKeys) {
		writeError(w, http.StatusTooManyRequests, "too_many_attempts", "Too many sign-in attempts. Try again later.")
		return
	}
	model := new(userModel)
	err := m.database.NewSelect().Model(model).
		Column("id", "username", "role", "password_hash", "totp_confirmed_at").Where("username = ?", input.Username).Scan(r.Context())
	if errors.Is(err, sql.ErrNoRows) {

		_ = argon2Dummy(input.Password, m.parameters)
		m.invalidLogin(w, r, input.Username)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "identity_unavailable", "Sign in is temporarily unavailable.")
		return
	}
	user := User{ID: model.ID, Username: model.Username, Role: model.Role}
	valid, err := verifyPassword(input.Password, model.PasswordHash)
	if err != nil || !valid {
		m.invalidLogin(w, r, input.Username)
		return
	}
	if err := m.startSession(w, r, user); err != nil {
		writeError(w, http.StatusInternalServerError, "session_unavailable", "A session could not be started.")
		return
	}
	m.resetAttempts(attemptKeys)
	lastLogin := m.now().UTC()
	_, _ = m.database.NewUpdate().Model((*userModel)(nil)).
		Set("last_login_at = ?", lastLogin).Where("id = ?", user.ID).Exec(r.Context())
	m.recordAudit(r.Context(), audit.Entry{ActorUserID: &user.ID, Action: "identity.password_accepted", Subject: "user:" + user.ID, RemoteAddress: remoteAddress(r)})
	// MFA is optional: an account without a confirmed factor is authenticated on
	// the password alone. Only an enrolled account is sent to its challenge.
	next := "mfa_challenge"
	if model.TOTPConfirmedAt == nil {
		next = "authenticated"
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": user, "next": next})
}

func loginAttemptKeys(username, peerAddress string) []string {
	account := strings.ToLower(strings.TrimSpace(username))
	return []string{
		"password-account:" + account,
		"password-peer-account:" + peerAddress + ":" + account,
	}
}

func (m *Module) allowAttempts(keys []string) bool {
	for _, key := range keys {
		if !m.attempts.Allow(key, m.now()) {
			return false
		}
	}
	return true
}

func (m *Module) resetAttempts(keys []string) {
	for _, key := range keys {
		m.attempts.Reset(key)
	}
}

func (m *Module) invalidLogin(w http.ResponseWriter, r *http.Request, username string) {
	m.recordAudit(r.Context(), audit.Entry{Action: "identity.login", Subject: "username:" + username, RemoteAddress: remoteAddress(r), Metadata: map[string]any{"result": "failure"}})
	writeError(w, http.StatusUnauthorized, "invalid_credentials", "The username or password is incorrect.")
}

func (m *Module) sessionHTTP(w http.ResponseWriter, r *http.Request) {
	user, ok := UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication_required", "Sign in to continue.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": user})
}

type changePasswordRequest struct {
	CurrentPassword string `json:"currentPassword"`
	NewPassword     string `json:"newPassword"`
}

func (m *Module) changePasswordHTTP(w http.ResponseWriter, r *http.Request) {
	person, ok := principalFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication_required", "Sign in to continue.")
		return
	}
	var input changePasswordRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if input.CurrentPassword == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "Your current password is required.")
		return
	}
	if err := validatePassword(input.NewPassword); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid_credentials", err.Error())
		return
	}
	attemptKey := "password-change:" + person.ID
	if !m.attempts.Allow(attemptKey, m.now()) {
		writeError(w, http.StatusTooManyRequests, "too_many_attempts", "Too many attempts. Try again later.")
		return
	}
	model := new(userModel)
	if err := m.database.NewSelect().Model(model).Column("id", "password_hash").
		Where("id = ?", person.ID).Scan(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "identity_unavailable", "The password could not be updated.")
		return
	}
	valid, err := verifyPassword(input.CurrentPassword, model.PasswordHash)
	if err != nil || !valid {
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "The current password is incorrect.")
		return
	}
	if same, _ := verifyPassword(input.NewPassword, model.PasswordHash); same {
		writeError(w, http.StatusUnprocessableEntity, "password_unchanged", "The new password must be different from your current password.")
		return
	}
	newHash, err := hashPassword(input.NewPassword, m.parameters)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "identity_unavailable", "The password could not be updated.")
		return
	}
	err = m.database.RunInTx(r.Context(), nil, func(ctx context.Context, tx bun.Tx) error {
		if _, err := tx.NewUpdate().Model((*userModel)(nil)).
			Set("password_hash = ?", newHash).Where("id = ?", person.ID).Exec(ctx); err != nil {
			return err
		}
		// A password change signs out every other session; the current one stays live.
		_, err := tx.NewDelete().Model((*sessionModel)(nil)).
			Where("user_id = ?", person.ID).Where("id != ?", person.SessionID).Exec(ctx)
		return err
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "identity_unavailable", "The password could not be updated.")
		return
	}
	m.attempts.Reset(attemptKey)
	m.recordAudit(r.Context(), audit.Entry{ActorUserID: &person.ID, Action: "identity.password_changed", Subject: "user:" + person.ID, RemoteAddress: remoteAddress(r)})
	w.WriteHeader(http.StatusNoContent)
}

func (m *Module) logoutHTTP(w http.ResponseWriter, r *http.Request) {
	person, err := m.requireSession(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "authentication_required", "Sign in to continue.")
		return
	}
	if _, err := m.database.NewDelete().Model((*sessionModel)(nil)).Where("id = ?", person.SessionID).Exec(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "session_unavailable", "The session could not be ended.")
		return
	}
	clearSessionCookie(w, r)
	m.recordAudit(r.Context(), audit.Entry{ActorUserID: &person.ID, Action: "identity.logout", Subject: "user:" + person.ID, RemoteAddress: remoteAddress(r)})
	w.WriteHeader(http.StatusNoContent)
}
