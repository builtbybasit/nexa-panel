package identity

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/nexa-panel/nexa-panel/internal/platform/audit"
	"github.com/uptrace/bun"
)

const totpSecretLabelPrefix = "identity.totp."

type mfaCodeRequest struct {
	Code         string `json:"code,omitempty"`
	RecoveryCode string `json:"recoveryCode,omitempty"`
}

func (m *Module) mfaEnrollHTTP(w http.ResponseWriter, r *http.Request) {
	person, ok := m.preAuthentication(w, r)
	if !ok {
		return
	}
	model := new(userModel)
	if err := m.database.NewSelect().Model(model).
		Column("id", "username", "totp_secret_encrypted", "totp_confirmed_at").
		Where("id = ?", person.ID).Scan(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "identity_unavailable", "MFA enrollment could not be loaded.")
		return
	}
	if model.TOTPConfirmedAt != nil {
		writeError(w, http.StatusConflict, "mfa_already_enrolled", "Multi-factor authentication is already enrolled.")
		return
	}

	var secret string
	if model.TOTPSecretEncrypted == nil {
		generated, err := generateTOTPSecret()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "identity_unavailable", "MFA enrollment could not be created.")
			return
		}
		encrypted, err := m.secrets.Encrypt(totpSecretLabelPrefix+person.ID, []byte(generated))
		if err != nil {
			writeError(w, http.StatusInternalServerError, "identity_unavailable", "MFA enrollment could not be protected.")
			return
		}
		if _, err := m.database.NewUpdate().Model((*userModel)(nil)).
			Set("totp_secret_encrypted = ?", encrypted).Where("id = ?", person.ID).Exec(r.Context()); err != nil {
			writeError(w, http.StatusInternalServerError, "identity_unavailable", "MFA enrollment could not be stored.")
			return
		}
		secret = generated
	} else {
		decrypted, err := m.secrets.Decrypt(totpSecretLabelPrefix+person.ID, *model.TOTPSecretEncrypted)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "identity_unavailable", "MFA enrollment could not be decrypted.")
			return
		}
		secret = string(decrypted)
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]string{
		"secret": secret, "provisioningUri": provisioningURI(person.Username, secret),
	})
}

func (m *Module) mfaConfirmHTTP(w http.ResponseWriter, r *http.Request) {
	person, ok := m.preAuthentication(w, r)
	if !ok {
		return
	}
	var input mfaCodeRequest
	if err := decodeJSON(w, r, &input); err != nil || input.Code == "" || input.RecoveryCode != "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "A six-digit authenticator code is required.")
		return
	}
	attemptKey := "mfa:" + person.SessionID
	if !m.attempts.Allow(attemptKey, m.now()) {
		writeError(w, http.StatusTooManyRequests, "too_many_attempts", "Too many verification attempts. Try again later.")
		return
	}
	model, secret, err := m.loadMFAUser(r.Context(), person.ID)
	if err != nil || model.TOTPSecretEncrypted == nil {
		writeError(w, http.StatusConflict, "mfa_enrollment_required", "Start MFA enrollment before confirming it.")
		return
	}
	if model.TOTPConfirmedAt != nil {
		writeError(w, http.StatusConflict, "mfa_already_enrolled", "Multi-factor authentication is already enrolled.")
		return
	}
	step, valid := validateTOTP(secret, input.Code, m.now().UTC(), model.TOTPLastStep)
	if !valid {
		writeError(w, http.StatusUnauthorized, "invalid_mfa_code", "The authenticator code is invalid or expired.")
		return
	}
	codes, hashes, err := generateRecoveryCodes(10)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "identity_unavailable", "Recovery codes could not be created.")
		return
	}
	encodedHashes, _ := json.Marshal(hashes)
	now := m.now().UTC()
	err = m.database.RunInTx(r.Context(), nil, func(ctx context.Context, tx bun.Tx) error {
		result, err := tx.NewUpdate().Model((*userModel)(nil)).
			Set("totp_confirmed_at = ?", now).Set("totp_last_step = ?", step).
			Set("recovery_code_hashes = ?", string(encodedHashes)).
			Where("id = ?", person.ID).Where("totp_confirmed_at IS NULL").Exec(ctx)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil || changed != 1 {
			return errors.New("MFA enrollment changed concurrently")
		}
		_, err = tx.NewUpdate().Model((*sessionModel)(nil)).Set("mfa_verified_at = ?", now).
			Where("id = ?", person.SessionID).Exec(ctx)
		return err
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "identity_unavailable", "MFA enrollment could not be confirmed.")
		return
	}
	m.attempts.Reset(attemptKey)
	m.recordAudit(r.Context(), audit.Entry{ActorUserID: &person.ID, Action: "identity.mfa_enrolled", Subject: "user:" + person.ID, RemoteAddress: remoteAddress(r)})
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"user": person.User, "recoveryCodes": codes})
}

type mfaDisableRequest struct {
	Password string `json:"password"`
}

// mfaDisableHTTP turns off the second factor for the signed-in account. It is
// registered as an authenticated route, so the identity middleware has already
// required a completed MFA challenge for this session; the password below is a
// second confirmation that the person at the keyboard owns the account.
func (m *Module) mfaDisableHTTP(w http.ResponseWriter, r *http.Request) {
	person, ok := UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication_required", "Sign in to continue.")
		return
	}
	if person.Role == "admin" {
		writeError(w, http.StatusForbidden, "administrator_mfa_required", "Administrator MFA can only be replaced through the account recovery procedure.")
		return
	}
	var input mfaDisableRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if input.Password == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "Your current password is required to disable MFA.")
		return
	}
	attemptKey := "mfa-disable:" + person.ID
	if !m.attempts.Allow(attemptKey, m.now()) {
		writeError(w, http.StatusTooManyRequests, "too_many_attempts", "Too many attempts. Try again later.")
		return
	}
	model := new(userModel)
	if err := m.database.NewSelect().Model(model).
		Column("id", "password_hash", "totp_confirmed_at").Where("id = ?", person.ID).Scan(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "identity_unavailable", "MFA could not be updated.")
		return
	}
	if model.TOTPConfirmedAt == nil {
		writeError(w, http.StatusConflict, "mfa_not_enrolled", "Multi-factor authentication is not enabled.")
		return
	}
	valid, err := verifyPassword(input.Password, model.PasswordHash)
	if err != nil || !valid {
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "The password is incorrect.")
		return
	}
	if _, err := m.database.NewUpdate().Model((*userModel)(nil)).
		Set("totp_secret_encrypted = NULL").Set("totp_confirmed_at = NULL").
		Set("totp_last_step = 0").Set("recovery_code_hashes = '[]'").
		Where("id = ?", person.ID).Exec(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "identity_unavailable", "MFA could not be disabled.")
		return
	}
	m.attempts.Reset(attemptKey)
	m.recordAudit(r.Context(), audit.Entry{ActorUserID: &person.ID, Action: "identity.mfa_disabled", Subject: "user:" + person.ID, RemoteAddress: remoteAddress(r)})
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"user": person, "mfaEnabled": false})
}

func (m *Module) mfaVerifyHTTP(w http.ResponseWriter, r *http.Request) {
	person, ok := m.preAuthentication(w, r)
	if !ok {
		return
	}
	var input mfaCodeRequest
	if err := decodeJSON(w, r, &input); err != nil || (input.Code == "") == (input.RecoveryCode == "") {
		writeError(w, http.StatusBadRequest, "invalid_request", "Provide either an authenticator code or one recovery code.")
		return
	}
	attemptKey := "mfa:" + person.SessionID
	if !m.attempts.Allow(attemptKey, m.now()) {
		writeError(w, http.StatusTooManyRequests, "too_many_attempts", "Too many verification attempts. Try again later.")
		return
	}

	method := "totp"
	var err error
	if input.Code != "" {
		err = m.verifyTOTPChallenge(r.Context(), person, input.Code)
	} else {
		method = "recovery_code"
		err = m.verifyRecoveryChallenge(r.Context(), person, input.RecoveryCode)
	}
	if err != nil {
		m.recordAudit(r.Context(), audit.Entry{ActorUserID: &person.ID, Action: "identity.mfa_failed", Subject: "user:" + person.ID, RemoteAddress: remoteAddress(r), Metadata: map[string]any{"method": method}})
		writeError(w, http.StatusUnauthorized, "invalid_mfa_code", "The authentication or recovery code is invalid.")
		return
	}
	m.attempts.Reset(attemptKey)
	m.recordAudit(r.Context(), audit.Entry{ActorUserID: &person.ID, Action: "identity.login", Subject: "user:" + person.ID, RemoteAddress: remoteAddress(r), Metadata: map[string]any{"method": method}})
	writeJSON(w, http.StatusOK, map[string]any{"user": person.User})
}

func (m *Module) verifyTOTPChallenge(ctx context.Context, person principal, code string) error {
	model, secret, err := m.loadMFAUser(ctx, person.ID)
	if err != nil || model.TOTPConfirmedAt == nil {
		return errors.New("MFA is not enrolled")
	}
	step, valid := validateTOTP(secret, code, m.now().UTC(), model.TOTPLastStep)
	if !valid {
		return errors.New("invalid TOTP")
	}
	now := m.now().UTC()
	return m.database.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		result, err := tx.NewUpdate().Model((*userModel)(nil)).Set("totp_last_step = ?", step).
			Where("id = ?", person.ID).Where("totp_last_step < ?", step).Exec(ctx)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil || changed != 1 {
			return errors.New("TOTP was already used")
		}
		_, err = tx.NewUpdate().Model((*sessionModel)(nil)).Set("mfa_verified_at = ?", now).
			Where("id = ?", person.SessionID).Exec(ctx)
		return err
	})
}

func (m *Module) verifyRecoveryChallenge(ctx context.Context, person principal, code string) error {
	wantedHash := hashRecoveryCode(code)
	now := m.now().UTC()
	return m.database.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		model := new(userModel)
		if err := tx.NewSelect().Model(model).Column("recovery_code_hashes").Where("id = ?", person.ID).Scan(ctx); err != nil {
			return err
		}
		var hashes []string
		if err := json.Unmarshal([]byte(model.RecoveryCodeHashes), &hashes); err != nil {
			return err
		}
		matched := -1
		for index, hash := range hashes {
			if len(hash) == len(wantedHash) && subtle.ConstantTimeCompare([]byte(hash), []byte(wantedHash)) == 1 {
				matched = index
			}
		}
		if matched < 0 {
			return errors.New("invalid recovery code")
		}
		hashes = append(hashes[:matched], hashes[matched+1:]...)
		encoded, _ := json.Marshal(hashes)
		if _, err := tx.NewUpdate().Model((*userModel)(nil)).Set("recovery_code_hashes = ?", string(encoded)).
			Where("id = ?", person.ID).Exec(ctx); err != nil {
			return err
		}
		_, err := tx.NewUpdate().Model((*sessionModel)(nil)).Set("mfa_verified_at = ?", now).
			Where("id = ?", person.SessionID).Exec(ctx)
		return err
	})
}

func (m *Module) loadMFAUser(ctx context.Context, userID string) (*userModel, string, error) {
	model := new(userModel)
	if err := m.database.NewSelect().Model(model).
		Column("id", "totp_secret_encrypted", "totp_confirmed_at", "totp_last_step", "recovery_code_hashes").
		Where("id = ?", userID).Scan(ctx); err != nil {
		return nil, "", err
	}
	if model.TOTPSecretEncrypted == nil {
		return model, "", nil
	}
	decrypted, err := m.secrets.Decrypt(totpSecretLabelPrefix+userID, *model.TOTPSecretEncrypted)
	if err != nil {
		return nil, "", err
	}
	return model, string(decrypted), nil
}

func (m *Module) preAuthentication(w http.ResponseWriter, r *http.Request) (principal, bool) {
	person, err := m.authenticate(r.Context(), r)
	if err != nil {
		clearSessionCookie(w, r)
		writeError(w, http.StatusUnauthorized, "authentication_required", "Sign in to continue.")
		return principal{}, false
	}
	if !validRequestOrigin(r) {
		writeError(w, http.StatusForbidden, "invalid_origin", "The request origin is not allowed.")
		return principal{}, false
	}
	return person, true
}
