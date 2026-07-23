package identity

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/platform/audit"
	"github.com/uptrace/bun"
)

// ErrUnknownAccount reports that no account carries the requested username. The
// break-glass caller is a root operator at a terminal, not a login form, so
// naming the miss is help rather than an enumeration oracle.
var ErrUnknownAccount = errors.New("no account with that username")

// BreakGlassReset describes what a second-factor reset actually changed, so the
// operator sees whether they hit the account they meant to.
type BreakGlassReset struct {
	UserID string
	// Username is read back from the row rather than echoed from the argument,
	// so a case difference between what was typed and what is stored is visible.
	Username string
	Role     string
	// WasEnrolled is false when the account had no confirmed factor to begin
	// with. The reset still runs (it clears half-finished enrollment material),
	// but nothing was locked out and the operator should know that.
	WasEnrolled bool
	// SessionsRevoked counts the sessions signed out. Every session is ended,
	// including any the attacker who triggered the lockout may hold.
	SessionsRevoked int64
}

// ResetSecondFactor is the offline break-glass path for an account that enrolled
// MFA and then lost both its authenticator and its recovery codes. Enrollment is
// optional by policy, so the account is not locked out of the panel afterwards —
// it returns to the unenrolled state, signs in on its password alone, and the
// status endpoint asks it to enroll again (mfaEnrollmentRecommended), which is
// what "forces a fresh enrollment" can honestly mean under an optional policy.
//
// It is deliberately a direct database operation with no HTTP surface and no
// authentication of its own: the only credential it can require is root on the
// node, which the caller proves before reaching here. It is safe to run while
// the panel API is serving — every write is one transaction against the same
// WAL database, and the audit record is chained by the same writer the API uses.
//
// recorder may be nil only in tests that assert the data change alone; the CLI
// always supplies one, because an unaudited factor removal is exactly the event
// an incident review needs to find.
func ResetSecondFactor(ctx context.Context, database *bun.DB, recorder audit.Recorder, username string, now time.Time) (BreakGlassReset, error) {
	if database == nil {
		return BreakGlassReset{}, errors.New("identity database is required")
	}
	username = strings.TrimSpace(username)
	if username == "" {
		return BreakGlassReset{}, errors.New("a username is required")
	}

	var result BreakGlassReset
	err := database.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		model := new(userModel)
		err := tx.NewSelect().Model(model).
			Column("id", "username", "role", "totp_confirmed_at").
			Where("username = ?", username).Scan(ctx)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: %s", ErrUnknownAccount, username)
		}
		if err != nil {
			return err
		}
		result = BreakGlassReset{
			UserID: model.ID, Username: model.Username, Role: model.Role,
			WasEnrolled: model.TOTPConfirmedAt != nil,
		}
		if _, err := tx.NewUpdate().Model((*userModel)(nil)).
			Set("totp_secret_encrypted = NULL").Set("totp_confirmed_at = NULL").
			Set("totp_last_step = 0").Set("recovery_code_hashes = '[]'").
			Where("id = ?", model.ID).Exec(ctx); err != nil {
			return err
		}
		// Every session goes, not just the unverified ones. A session that already
		// answered the old factor would otherwise stay privileged after the factor
		// it answered has been destroyed.
		deleted, err := tx.NewDelete().Model((*sessionModel)(nil)).Where("user_id = ?", model.ID).Exec(ctx)
		if err != nil {
			return err
		}
		result.SessionsRevoked, err = deleted.RowsAffected()
		return err
	})
	if err != nil {
		return BreakGlassReset{}, err
	}

	if recorder != nil {
		// The actor is the root operator on the host, who has no panel identity, so
		// ActorUserID stays nil and the subject names the account that was reset.
		// A failure to record is returned: the whole point of a break-glass path is
		// that it leaves evidence, so a silent one is worse than a refused one.
		entry := audit.Entry{
			Action: "identity.mfa_break_glass_reset", Subject: "user:" + result.UserID,
			Metadata: map[string]any{
				"username": result.Username, "role": result.Role,
				"wasEnrolled": result.WasEnrolled, "sessionsRevoked": result.SessionsRevoked,
				"via": "cli", "resetAt": now.UTC().Format(time.RFC3339),
			},
		}
		if err := recorder.Record(ctx, entry); err != nil {
			return result, fmt.Errorf("record break-glass audit event: %w", err)
		}
	}
	return result, nil
}
