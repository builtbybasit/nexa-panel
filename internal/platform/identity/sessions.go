package identity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/platform/httpapi"
)

// errCSRFRejected is deliberately distinct from the "session is missing/expired"
// errors: a CSRF failure means a live session was presented by something that
// could not prove it was this panel's own UI, which is an attack signal rather
// than the routine sign-in prompt.
var errCSRFRejected = errors.New("csrf verification failed")

func (m *Module) bootstrapRequired(ctx context.Context) (bool, error) {
	exists, err := m.database.NewSelect().Model((*userModel)(nil)).Exists(ctx)
	return !exists, err
}

func (m *Module) startSession(w http.ResponseWriter, r *http.Request, user User) error {
	token := make([]byte, 32)
	rand.Read(token)
	rawToken := hex.EncodeToString(token)
	hashedToken := sha256.Sum256([]byte(rawToken))
	csrfToken := make([]byte, 32)
	if _, err := rand.Read(csrfToken); err != nil {
		return fmt.Errorf("generate csrf token: %w", err)
	}
	rawCSRF := hex.EncodeToString(csrfToken)
	hashedCSRF := sha256.Sum256([]byte(rawCSRF))
	now := m.now().UTC()
	expiresAt := now.Add(m.config.SessionTTL)
	model := &sessionModel{
		ID: randomID(16), UserID: user.ID, TokenHash: hashedToken[:], CreatedAt: now,
		ExpiresAt: expiresAt, LastSeenAt: now, RemoteAddress: remoteAddress(r), UserAgent: r.UserAgent(),
		CSRFTokenHash: hashedCSRF[:],
	}
	_, err := m.database.NewInsert().Model(model).Exec(r.Context())
	if err != nil {
		return fmt.Errorf("store session: %w", err)
	}
	secure := requestIsHTTPS(r)
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: rawToken, Path: "/", HttpOnly: true,
		Secure: secure, SameSite: http.SameSiteStrictMode,
		Expires: expiresAt, MaxAge: int(m.config.SessionTTL.Seconds()),
	})
	// The CSRF cookie mirrors the session cookie in every attribute except
	// HttpOnly: the UI has to read it to echo the token back in a header, which is
	// what makes the double submit meaningful. Only the hash is stored, so the
	// cookie's value is worthless to anyone who reads the database.
	http.SetCookie(w, &http.Cookie{
		Name: csrfCookieName, Value: rawCSRF, Path: "/", HttpOnly: false,
		Secure: secure, SameSite: http.SameSiteStrictMode,
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
		LastSeenAt      time.Time  `bun:"last_seen_at"`
		CSRFTokenHash   []byte     `bun:"csrf_token_hash"`
		MFAVerifiedAt   *time.Time `bun:"mfa_verified_at"`
		TOTPConfirmedAt *time.Time `bun:"totp_confirmed_at"`
	}
	err = m.database.NewSelect().TableExpr("identity_sessions AS session").
		ColumnExpr("identity_user.id").ColumnExpr("identity_user.username").ColumnExpr("identity_user.role").
		ColumnExpr("identity_user.totp_confirmed_at").ColumnExpr("session.id AS session_id").
		ColumnExpr("session.expires_at").ColumnExpr("session.last_seen_at").
		ColumnExpr("session.csrf_token_hash").ColumnExpr("session.mfa_verified_at").
		Join("JOIN identity_users AS identity_user ON identity_user.id = session.user_id").
		Where("session.token_hash = ?", hashedToken[:]).Scan(ctx, &row)
	if err != nil {
		return principal{}, errors.New("session not found")
	}
	now := m.now().UTC()
	if !row.ExpiresAt.After(now) {
		_, _ = m.database.NewDelete().Model((*sessionModel)(nil)).Where("token_hash = ?", hashedToken[:]).Exec(ctx)
		return principal{}, errors.New("session expired")
	}
	// SessionTTL is only the absolute cap. A session nobody has used for
	// IdleTimeout is ended here rather than left usable for the rest of the day.
	if now.Sub(row.LastSeenAt.UTC()) > m.config.IdleTimeout {
		_, _ = m.database.NewDelete().Model((*sessionModel)(nil)).Where("token_hash = ?", hashedToken[:]).Exec(ctx)
		return principal{}, errors.New("session idle timeout")
	}
	// A session minted before CSRF binding existed has no hash to compare against,
	// so it can never satisfy an unsafe request. Ending it here converts a
	// permanent write failure after the upgrade into one clean re-authentication.
	if len(row.CSRFTokenHash) == 0 {
		_, _ = m.database.NewDelete().Model((*sessionModel)(nil)).Where("token_hash = ?", hashedToken[:]).Exec(ctx)
		return principal{}, errors.New("session predates csrf binding")
	}
	person := principal{
		User: User{ID: row.ID, Username: row.Username, Role: row.Role}, SessionID: row.SessionID,
		CSRFTokenHash: row.CSRFTokenHash, MFAVerifiedAt: row.MFAVerifiedAt, TOTPConfirmedAt: row.TOTPConfirmedAt,
	}
	person.TokenHash = hashedToken[:]
	// Only traffic a person caused renews the idle window. The panel polls jobs
	// and status every few seconds for as long as a tab is open, so counting those
	// as use would keep an abandoned browser signed in until the absolute cap and
	// leave IdleTimeout enforcing nothing -- which is the very case it exists for.
	//
	// The session cookie's own expiry stays at the absolute cap and is not
	// reissued: the idle window is enforced from last_seen_at on the server, so
	// refreshing the cookie would add a Set-Cookie to every response without
	// changing when the session actually ends.
	if !backgroundRequest(r) {
		_, _ = m.database.NewUpdate().Model((*sessionModel)(nil)).
			Set("last_seen_at = ?", now).Where("id = ?", person.SessionID).Exec(ctx)
	}
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
	if !validCSRFToken(r, person) {
		return principal{}, errCSRFRejected
	}
	return person, nil
}

func requestIsHTTPS(r *http.Request) bool {
	return httpapi.IsHTTPS(r)
}

func clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	// Only delete a cookie the client actually presented. Sending the deletion on
	// a request that carried no session cookie would wipe a valid cookie the
	// browser still holds but merely withheld on this one request (a stale
	// duplicate for the same host sent first, or a SameSite navigation), turning a
	// single transient miss into a permanent sign-out on the next reload.
	if _, err := r.Cookie(cookieName); err != nil {
		return
	}
	secure := requestIsHTTPS(r)
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: "", Path: "/", HttpOnly: true,
		Secure: secure, SameSite: http.SameSiteStrictMode,
		Expires: time.Unix(1, 0), MaxAge: -1,
	})
	// The CSRF cookie is cleared under the same caution and only when the client
	// presented it: the two cookies are issued together but a browser can withhold
	// one of them, and deleting a token the client still holds would break the
	// double submit on a session that is otherwise fine.
	if _, err := r.Cookie(csrfCookieName); err != nil {
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: csrfCookieName, Value: "", Path: "/", HttpOnly: false,
		Secure: secure, SameSite: http.SameSiteStrictMode,
		Expires: time.Unix(1, 0), MaxAge: -1,
	})
}

// validRequestOrigin establishes that an unsafe request was issued by this
// panel's own origin. It fails CLOSED: a request that proves nothing is
// rejected, because SameSite=Strict is a browser courtesy and not a guarantee.
func validRequestOrigin(r *http.Request) bool {
	if safeMethod(r.Method) {
		return true
	}
	// "null" is what a sandboxed or privacy-stripped context sends; it names no
	// origin, so it can never establish this one.
	if rawOrigin := r.Header.Get("Origin"); rawOrigin != "" && rawOrigin != "null" {
		origin, err := url.Parse(rawOrigin)
		return err == nil && strings.EqualFold(origin.Host, r.Host)
	}
	// Fetch metadata is written by the browser and cannot be forged by page
	// script. "none" means the request was initiated by the user directly (address
	// bar, bookmark), which no attacker page is able to cause.
	switch r.Header.Get("Sec-Fetch-Site") {
	case "same-origin", "none":
		return true
	case "same-site", "cross-site":
		return false
	}
	// Neither header: the last thing that can still name an origin is the Referer.
	if referer := r.Header.Get("Referer"); referer != "" {
		parsed, err := url.Parse(referer)
		return err == nil && strings.EqualFold(parsed.Host, r.Host)
	}
	return false
}

// validCSRFToken completes the double submit: the header value must match the
// token bound to THIS session, so a token harvested from another session (or
// from an attacker's own login) is useless. Cookies alone cannot prove this,
// because a network attacker can set a cookie the origin checks would accept.
func validCSRFToken(r *http.Request, person principal) bool {
	if safeMethod(r.Method) {
		return true
	}
	presented := strings.TrimSpace(r.Header.Get(csrfHeaderName))
	if presented == "" {
		return false
	}
	presentedHash := sha256.Sum256([]byte(presented))
	return subtle.ConstantTimeCompare(presentedHash[:], person.CSRFTokenHash) == 1
}

// backgroundHeaderName is set by the panel on the requests it issues by itself
// -- poll refreshes and the like -- as opposed to the ones a person triggered.
const backgroundHeaderName = "X-Nexa-Background"

// backgroundRequest fails SAFE, in the direction opposite to the CSRF checks: a
// caller that says nothing is taken to be a person at the keyboard, so a client
// that never learns to send the marker keeps sliding its idle window instead of
// being signed out mid-task. Only an explicit marker forfeits the renewal, and
// forfeiting it is never to the caller's advantage.
func backgroundRequest(r *http.Request) bool {
	return strings.TrimSpace(r.Header.Get(backgroundHeaderName)) == "1"
}

func safeMethod(method string) bool {
	return method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions
}
