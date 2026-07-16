package identity

import (
	"net/http"

	"context"
	"strings"

	"database/sql"
	"github.com/nexa-panel/nexa-panel/internal/platform/audit"

	"errors"

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
		"mfaEnrollmentRequired": false, "mfaChallengeRequired": false,
	}
	if person, err := m.authenticate(r.Context(), r); err == nil {
		response["user"] = person.User
		if person.TOTPConfirmedAt == nil {
			response["mfaEnrollmentRequired"] = true
		} else if person.MFAVerifiedAt == nil {
			response["mfaChallengeRequired"] = true
		} else {
			response["authenticated"] = true
		}
	}
	writeJSON(w, http.StatusOK, response)
}

func (m *Module) bootstrapHTTP(w http.ResponseWriter, r *http.Request) {
	var input credentials
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	input.Username = strings.TrimSpace(input.Username)
	if err := validateCredentials(input); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid_credentials", err.Error())
		return
	}
	passwordHash, err := hashPassword(input.Password, m.parameters)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "identity_unavailable", "Administrator could not be created.")
		return
	}

	user := User{ID: randomID(16), Username: input.Username, Role: "admin"}
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
	m.recordAudit(r.Context(), audit.Entry{ActorUserID: &user.ID, Action: "identity.bootstrap", Subject: "user:" + user.ID, RemoteAddress: remoteAddress(r)})
	writeJSON(w, http.StatusCreated, map[string]any{"user": user, "next": "mfa_enrollment"})
}

func (m *Module) loginHTTP(w http.ResponseWriter, r *http.Request) {
	var input credentials
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	input.Username = strings.TrimSpace(input.Username)
	attemptKey := "password:" + remoteAddress(r)
	if !m.attempts.Allow(attemptKey, m.now()) {
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
	m.attempts.Reset(attemptKey)
	lastLogin := m.now().UTC()
	_, _ = m.database.NewUpdate().Model((*userModel)(nil)).
		Set("last_login_at = ?", lastLogin).Where("id = ?", user.ID).Exec(r.Context())
	m.recordAudit(r.Context(), audit.Entry{ActorUserID: &user.ID, Action: "identity.password_accepted", Subject: "user:" + user.ID, RemoteAddress: remoteAddress(r)})
	next := "mfa_challenge"
	if model.TOTPConfirmedAt == nil {
		next = "mfa_enrollment"
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": user, "next": next})
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
