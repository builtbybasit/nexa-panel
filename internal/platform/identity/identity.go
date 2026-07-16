package identity

import (
	"io"

	"time"

	"context"
	"net/http"

	"regexp"

	"github.com/nexa-panel/nexa-panel/internal/platform/audit"
	"github.com/nexa-panel/nexa-panel/internal/platform/module"

	"errors"
	"github.com/nexa-panel/nexa-panel/internal/platform/persistence"
	"log/slog"

	"github.com/nexa-panel/nexa-panel/internal/platform/secrets"

	"github.com/uptrace/bun"
)

const (
	cookieName     = "nexa_session"
	identitySchema = `
		CREATE TABLE identity_users (
			id TEXT PRIMARY KEY,
			username TEXT NOT NULL UNIQUE COLLATE NOCASE,
			password_hash TEXT NOT NULL,
			created_at TIMESTAMP NOT NULL,
			last_login_at TIMESTAMP
		);
		CREATE TABLE identity_sessions (
			id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL REFERENCES identity_users(id) ON DELETE CASCADE,
			token_hash BLOB NOT NULL UNIQUE,
			created_at TIMESTAMP NOT NULL,
			expires_at TIMESTAMP NOT NULL,
			last_seen_at TIMESTAMP NOT NULL,
			remote_address TEXT NOT NULL DEFAULT '',
			user_agent TEXT NOT NULL DEFAULT ''
		);
		CREATE INDEX identity_sessions_expires_at_idx ON identity_sessions (expires_at);
	`
	identityMFASchema = `
		ALTER TABLE identity_users ADD COLUMN role TEXT NOT NULL DEFAULT 'admin';
		ALTER TABLE identity_users ADD COLUMN totp_secret_encrypted TEXT;
		ALTER TABLE identity_users ADD COLUMN totp_confirmed_at TIMESTAMP;
		ALTER TABLE identity_users ADD COLUMN totp_last_step INTEGER NOT NULL DEFAULT 0;
		ALTER TABLE identity_users ADD COLUMN recovery_code_hashes TEXT NOT NULL DEFAULT '[]';
		ALTER TABLE identity_sessions ADD COLUMN mfa_verified_at TIMESTAMP;
	`
	identitySiteGrantSchema = `
		CREATE TABLE identity_site_grants (
			user_id TEXT NOT NULL REFERENCES identity_users(id) ON DELETE CASCADE,
			site_id TEXT NOT NULL,
			created_at TIMESTAMP NOT NULL,
			PRIMARY KEY (user_id, site_id)
		);
	`
)

var usernamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{2,63}$`)

var errAlreadyBootstrapped = errors.New("identity is already bootstrapped")

type Config struct {
	SessionTTL         time.Duration
	PasswordMemoryKiB  uint32
	PasswordIterations uint32
	PasswordThreads    uint8
	AttemptLimit       int
	AttemptWindow      time.Duration
}

func DefaultConfig() Config {
	return Config{
		SessionTTL:         24 * time.Hour,
		PasswordMemoryKiB:  defaultPasswordParameters.memory,
		PasswordIterations: defaultPasswordParameters.iterations,
		PasswordThreads:    defaultPasswordParameters.parallelism,
		AttemptLimit:       5,
		AttemptWindow:      5 * time.Minute,
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
}

type principal struct {
	User
	SessionID       string
	TokenHash       []byte
	MFAVerifiedAt   *time.Time
	TOTPConfirmedAt *time.Time
}

type principalContextKey struct{}

func UserFromContext(ctx context.Context) (User, bool) {
	value, ok := ctx.Value(principalContextKey{}).(principal)
	return value.User, ok
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
	siteDirectory SiteDirectory
}

func New(ctx context.Context, database *bun.DB, recorder audit.Recorder, cryptography secrets.Cipher, logger *slog.Logger) (*Module, error) {
	return NewWithConfig(ctx, database, recorder, cryptography, logger, DefaultConfig())
}

func NewWithConfig(ctx context.Context, database *bun.DB, recorder audit.Recorder, cryptography secrets.Cipher, logger *slog.Logger, config Config) (*Module, error) {
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
	if config.SessionTTL <= 0 || config.PasswordMemoryKiB == 0 || config.PasswordIterations == 0 || config.PasswordThreads == 0 || config.AttemptLimit <= 0 || config.AttemptWindow <= 0 {
		return nil, errors.New("identity configuration values must be positive")
	}
	if err := persistence.Migrate(ctx, database, "identity", []string{identitySchema, identityMFASchema, identitySiteGrantSchema}); err != nil {
		return nil, err
	}
	return &Module{
		database: database,
		audit:    recorder,
		logger:   logger,
		now:      time.Now,
		config:   config,
		secrets:  cryptography,
		attempts: newAttemptLimiter(config.AttemptLimit, config.AttemptWindow),
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
		if person.MFAVerifiedAt == nil {
			writeError(w, http.StatusUnauthorized, "mfa_required", "Complete multi-factor authentication to continue.")
			return
		}
		if !validRequestOrigin(r) {
			writeError(w, http.StatusForbidden, "invalid_origin", "The request origin is not allowed.")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalContextKey{}, person)))
	})
}

type credentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
}
