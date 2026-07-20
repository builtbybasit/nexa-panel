package identity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/platform/httpapi"
)

func (m *Module) bootstrapRequired(ctx context.Context) (bool, error) {
	exists, err := m.database.NewSelect().Model((*userModel)(nil)).Exists(ctx)
	return !exists, err
}

func (m *Module) startSession(w http.ResponseWriter, r *http.Request, user User) error {
	token := make([]byte, 32)
	rand.Read(token)
	rawToken := hex.EncodeToString(token)
	hashedToken := sha256.Sum256([]byte(rawToken))
	now := m.now().UTC()
	expiresAt := now.Add(m.config.SessionTTL)
	model := &sessionModel{
		ID: randomID(16), UserID: user.ID, TokenHash: hashedToken[:], CreatedAt: now,
		ExpiresAt: expiresAt, LastSeenAt: now, RemoteAddress: remoteAddress(r), UserAgent: r.UserAgent(),
	}
	_, err := m.database.NewInsert().Model(model).Exec(r.Context())
	if err != nil {
		return fmt.Errorf("store session: %w", err)
	}
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: rawToken, Path: "/", HttpOnly: true,
		Secure: requestIsHTTPS(r), SameSite: http.SameSiteStrictMode,
		Expires: expiresAt, MaxAge: int(m.config.SessionTTL.Seconds()),
	})
	return nil
}

func (m *Module) authenticate(ctx context.Context, r *http.Request) (principal, error) {
	cookie, err := r.Cookie(cookieName)
	if err != nil || len(cookie.Value) != 64 {
		return principal{}, errors.New("session cookie is missing")
	}
	hashedToken := sha256.Sum256([]byte(cookie.Value))
	var row struct {
		ID              string
		Username        string
		Role            string
		SessionID       string     `bun:"session_id"`
		ExpiresAt       time.Time  `bun:"expires_at"`
		MFAVerifiedAt   *time.Time `bun:"mfa_verified_at"`
		TOTPConfirmedAt *time.Time `bun:"totp_confirmed_at"`
	}
	err = m.database.NewSelect().TableExpr("identity_sessions AS session").
		ColumnExpr("identity_user.id").ColumnExpr("identity_user.username").ColumnExpr("identity_user.role").
		ColumnExpr("identity_user.totp_confirmed_at").ColumnExpr("session.id AS session_id").
		ColumnExpr("session.expires_at").ColumnExpr("session.mfa_verified_at").
		Join("JOIN identity_users AS identity_user ON identity_user.id = session.user_id").
		Where("session.token_hash = ?", hashedToken[:]).Scan(ctx, &row)
	if err != nil {
		return principal{}, errors.New("session not found")
	}
	if !row.ExpiresAt.After(m.now().UTC()) {
		_, _ = m.database.NewDelete().Model((*sessionModel)(nil)).Where("token_hash = ?", hashedToken[:]).Exec(ctx)
		return principal{}, errors.New("session expired")
	}
	person := principal{
		User: User{ID: row.ID, Username: row.Username, Role: row.Role}, SessionID: row.SessionID,
		MFAVerifiedAt: row.MFAVerifiedAt, TOTPConfirmedAt: row.TOTPConfirmedAt,
	}
	person.TokenHash = hashedToken[:]
	_, _ = m.database.NewUpdate().Model((*sessionModel)(nil)).
		Set("last_seen_at = ?", m.now().UTC()).Where("id = ?", person.SessionID).Exec(ctx)
	return person, nil
}

func (m *Module) requireSession(r *http.Request) (principal, error) {
	person, err := m.authenticate(r.Context(), r)
	if err != nil {
		return principal{}, err
	}
	if !validRequestOrigin(r) {
		return principal{}, errors.New("invalid request origin")
	}
	return person, nil
}

func requestIsHTTPS(r *http.Request) bool {
	return httpapi.IsHTTPS(r)
}

func clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: "", Path: "/", HttpOnly: true,
		Secure: requestIsHTTPS(r), SameSite: http.SameSiteStrictMode,
		Expires: time.Unix(1, 0), MaxAge: -1,
	})
}

func validRequestOrigin(r *http.Request) bool {
	if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
		return true
	}
	rawOrigin := r.Header.Get("Origin")
	if rawOrigin == "" {
		return true
	}
	origin, err := url.Parse(rawOrigin)
	return err == nil && strings.EqualFold(origin.Host, r.Host)
}
