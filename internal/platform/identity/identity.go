package identity

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"sync"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/platform/audit"
	"github.com/nexa-panel/nexa-panel/internal/platform/module"
	"github.com/nexa-panel/nexa-panel/internal/platform/secrets"

	"github.com/uptrace/bun"
)

const (
	cookieName = "nexa_session"
	// csrfCookieName is readable by the UI on purpose; csrfHeaderName is where the
	// UI must echo it back. Only a header can carry the second half of the double
	// submit, because a cross-site form can send cookies but never a header.
	csrfCookieName = "nexa_csrf"
	csrfHeaderName = "X-CSRF-Token"
)

var usernamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{2,63}$`)

var errAlreadyBootstrapped = errors.New("identity is already bootstrapped")

type Config struct {
	SessionTTL time.Duration
	// IdleTimeout ends a session that has gone unused for this long, well before
	// SessionTTL's absolute cap expires it. An unattended browser on a shared
	// machine is the threat: last_seen_at is bumped on every authenticated
	// request, so an actively used session slides forward and never notices.
	IdleTimeout        time.Duration
	PasswordMemoryKiB  uint32
	PasswordIterations uint32
	PasswordThreads    uint8
	AttemptLimit       int
	AttemptWindow      time.Duration
	// MFALockoutLimit and MFALockoutWindow guard the second-factor challenge
	// against brute force. Once an identity fails the second factor this many
	// times inside the window, further verification is locked for the remainder
	// of the window. Because the bucket is keyed on the user identity (not the
	// session), minting a fresh login session does not reset it.
	MFALockoutLimit  int
	MFALockoutWindow time.Duration
}

func DefaultConfig() Config {
	return Config{
		SessionTTL:         24 * time.Hour,
		IdleTimeout:        2 * time.Hour,
		PasswordMemoryKiB:  defaultPasswordParameters.memory,
		PasswordIterations: defaultPasswordParameters.iterations,
		PasswordThreads:    defaultPasswordParameters.parallelism,
		AttemptLimit:       5,
		AttemptWindow:      5 * time.Minute,
		MFALockoutLimit:    10,
		MFALockoutWindow:   15 * time.Minute,
	}
}

type User struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Role     string `json:"role"`
}

type userModel struct {
	bun.BaseModel       `bun:"table:identity_users,alias:identity_user"`
	ID                  string    `bun:",pk"`
	Username            string    `bun:",notnull"`
	PasswordHash        string    `bun:",notnull"`
	CreatedAt           time.Time `bun:",notnull"`
	LastLoginAt         *time.Time
	Role                string
	TOTPSecretEncrypted *string
	TOTPConfirmedAt     *time.Time
	TOTPLastStep        int64
	RecoveryCodeHashes  string
}

type sessionModel struct {
	bun.BaseModel `bun:"table:identity_sessions,alias:identity_session"`
	ID            string    `bun:",pk"`
	UserID        string    `bun:",notnull"`
	TokenHash     []byte    `bun:",notnull"`
	CreatedAt     time.Time `bun:",notnull"`
	ExpiresAt     time.Time `bun:",notnull"`
	LastSeenAt    time.Time `bun:",notnull"`
	RemoteAddress string    `bun:",notnull"`
	UserAgent     string    `bun:",notnull"`
	MFAVerifiedAt *time.Time
	// CSRFTokenHash binds the double-submit cookie to this session, so a token
	// minted for one session cannot be replayed against another.
	CSRFTokenHash []byte
}

type principal struct {
	User
	SessionID       string
	TokenHash       []byte
	CSRFTokenHash   []byte
	MFAVerifiedAt   *time.Time
	TOTPConfirmedAt *time.Time
}

type principalContextKey struct{}

func UserFromContext(ctx context.Context) (User, bool) {
	value, ok := ctx.Value(principalContextKey{}).(principal)
	return value.User, ok
}

// RecentMFA reports whether the current authenticated session completed a
// second-factor challenge within maxAge. Authorization uses this for sensitive
// actions without exposing the session model or identity's context key.
func RecentMFA(ctx context.Context, now time.Time, maxAge time.Duration) bool {
	value, ok := ctx.Value(principalContextKey{}).(principal)
	if !ok || value.MFAVerifiedAt == nil || maxAge <= 0 {
		return false
	}
	verifiedAt := value.MFAVerifiedAt.UTC()
	return !verifiedAt.After(now.UTC()) && now.UTC().Sub(verifiedAt) <= maxAge
}

// MFAEnrolled reports whether the current session's account has a confirmed
// second factor. Authorization uses it to require step-up for sensitive actions
// only when the account opted into MFA: because MFA is optional, a password-only
// account must not be blocked from actions it is otherwise authorized for.
func MFAEnrolled(ctx context.Context) bool {
	value, ok := ctx.Value(principalContextKey{}).(principal)
	return ok && value.TOTPConfirmedAt != nil
}

// principalFromContext returns the full authenticated principal, including the
// session identifier, for handlers that need to act on the current session
// (e.g. keeping it alive while revoking the account's other sessions).
func principalFromContext(ctx context.Context) (principal, bool) {
	value, ok := ctx.Value(principalContextKey{}).(principal)
	return value, ok
}

type Module struct {
	database      *bun.DB
	audit         audit.Recorder
	logger        *slog.Logger
	now           func() time.Time
	config        Config
	parameters    passwordParameters
	secrets       secrets.Cipher
	attempts      *attemptLimiter
	lockouts      *attemptLimiter
	siteDirectory SiteDirectory

	// bootstrapMu guards the first-run bootstrap secret against concurrent
	// bootstrap requests racing the account creation that closes the window.
	bootstrapMu        sync.Mutex
	bootstrapTokenPath string
	bootstrapToken     string
}

func New(ctx context.Context, database *bun.DB, recorder audit.Recorder, cryptography secrets.Cipher, logger *slog.Logger) (*Module, error) {
	return NewWithConfig(ctx, database, recorder, cryptography, logger, DefaultConfig())
}

func NewWithConfig(_ context.Context, database *bun.DB, recorder audit.Recorder, cryptography secrets.Cipher, logger *slog.Logger, config Config) (*Module, error) {
	if database == nil || recorder == nil || cryptography == nil {
		return nil, errors.New("identity database, audit recorder, and secret cipher are required")
	}
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if config.AttemptLimit == 0 {
		config.AttemptLimit = DefaultConfig().AttemptLimit
	}
	if config.AttemptWindow == 0 {
		config.AttemptWindow = DefaultConfig().AttemptWindow
	}
	if config.MFALockoutLimit == 0 {
		config.MFALockoutLimit = DefaultConfig().MFALockoutLimit
	}
	if config.MFALockoutWindow == 0 {
		config.MFALockoutWindow = DefaultConfig().MFALockoutWindow
	}
	if config.IdleTimeout == 0 {
		config.IdleTimeout = DefaultConfig().IdleTimeout
	}
	if config.SessionTTL <= 0 || config.IdleTimeout <= 0 || config.PasswordMemoryKiB == 0 || config.PasswordIterations == 0 || config.PasswordThreads == 0 || config.AttemptLimit <= 0 || config.AttemptWindow <= 0 || config.MFALockoutLimit <= 0 || config.MFALockoutWindow <= 0 {
		return nil, errors.New("identity configuration values must be positive")
	}
	return &Module{
		database: database,
		audit:    recorder,
		logger:   logger,
		now:      time.Now,
		config:   config,
		secrets:  cryptography,
		attempts: newAttemptLimiter(config.AttemptLimit, config.AttemptWindow),
		lockouts: newAttemptLimiter(config.MFALockoutLimit, config.MFALockoutWindow),
		parameters: passwordParameters{
			memory: config.PasswordMemoryKiB, iterations: config.PasswordIterations,
			parallelism: config.PasswordThreads, saltLength: 16, keyLength: 32,
		},
	}, nil
}

func (m *Module) Descriptor() module.Descriptor {
	return module.Descriptor{
		ID: "identity", Name: "Identity", Version: "0.1.0",
		Description:  "Administrator bootstrap, encrypted TOTP, recovery codes, and authenticated sessions.",
		Dependencies: []string{"audit"}, EstimatedIdleBytes: 1024 * 1024,
	}
}

func (m *Module) Register(registry module.Registry) error {
	routes := []struct {
		pattern       string
		handler       http.Handler
		authenticated bool
	}{
		{"GET /api/v1/auth/status", http.HandlerFunc(m.statusHTTP), false},
		{"POST /api/v1/auth/bootstrap", http.HandlerFunc(m.bootstrapHTTP), false},
		{"POST /api/v1/auth/login", http.HandlerFunc(m.loginHTTP), false},
		{"POST /api/v1/auth/mfa/enroll", http.HandlerFunc(m.mfaEnrollHTTP), false},
		{"POST /api/v1/auth/mfa/confirm", http.HandlerFunc(m.mfaConfirmHTTP), false},
		{"POST /api/v1/auth/mfa/verify", http.HandlerFunc(m.mfaVerifyHTTP), false},
		// Disabling requires a fully authenticated session (the middleware enforces
		// a completed MFA challenge when one is enrolled), so a stolen password
		// alone can never strip the second factor.
		{"POST /api/v1/auth/mfa/disable", http.HandlerFunc(m.mfaDisableHTTP), true},
		// Self-service password change: authenticated, and re-confirmed with the
		// current password. A fully authenticated session is required (the
		// middleware enforces a completed MFA challenge when one is enrolled).
		{"POST /api/v1/auth/password", http.HandlerFunc(m.changePasswordHTTP), true},
		{"GET /api/v1/auth/session", http.HandlerFunc(m.sessionHTTP), true},
		{"POST /api/v1/auth/logout", http.HandlerFunc(m.logoutHTTP), false},
	}
	for _, route := range routes {
		var err error
		if route.authenticated {
			err = registry.HandleAuthenticated(route.pattern, route.handler)
		} else {
			err = registry.Handle(route.pattern, route.handler)
		}
		if err != nil {
			return err
		}
	}
	authorized := []struct {
		pattern string
		handler http.Handler
	}{
		{"GET /api/v1/users", http.HandlerFunc(m.listUsersHTTP)},
		{"POST /api/v1/users", http.HandlerFunc(m.createUserHTTP)},
		{"PATCH /api/v1/users/{id}", http.HandlerFunc(m.updateUserHTTP)},
		{"DELETE /api/v1/users/{id}", http.HandlerFunc(m.deleteUserHTTP)},
		{"PUT /api/v1/users/{id}/sites", http.HandlerFunc(m.replaceUserSitesHTTP)},
	}
	for _, route := range authorized {
		if err := registry.HandleAuthorized(route.pattern, "users.manage", route.handler); err != nil {
			return err
		}
	}
	return nil
}

func (m *Module) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		person, err := m.authenticate(r.Context(), r)
		if err != nil {
			clearSessionCookie(w, r)
			writeError(w, http.StatusUnauthorized, "authentication_required", "Sign in to continue.")
			return
		}
		// Multi-factor authentication is optional: an account is never forced to
		// enroll. But once an account HAS enrolled, every request must carry a
		// completed challenge, so a stolen password alone can't ride an
		// MFA-protected account.
		if person.TOTPConfirmedAt != nil && person.MFAVerifiedAt == nil {
			writeError(w, http.StatusUnauthorized, "mfa_required", "Complete multi-factor authentication to continue.")
			return
		}
		if !validRequestOrigin(r) {
			writeError(w, http.StatusForbidden, "invalid_origin", "The request origin is not allowed.")
			return
		}
		if !validCSRFToken(r, person) {
			m.logger.Warn("rejected request without a valid CSRF token", "user", person.Username, "path", r.URL.Path, "remote", remoteAddress(r))
			writeError(w, http.StatusForbidden, "invalid_csrf_token", "The request could not be verified. Reload the page and try again.")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalContextKey{}, person)))
	})
}

type credentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
}
