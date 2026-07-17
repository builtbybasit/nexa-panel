package mysql

import (
	"path/filepath"
	"regexp"
	"time"

	"context"
	"errors"
)

const PlanKind = "nexa.mysql-family.v1"

type EngineKind string

const (
	EngineMySQL   EngineKind = "mysql"
	EngineMariaDB EngineKind = "mariadb"
)

type Action string

const (
	ActionCreateDatabase Action = "database.create"
	ActionCreateAccount  Action = "account.create"
	ActionRotateAccount  Action = "account.rotate"
	ActionApplyGrant     Action = "grant.apply"
	ActionCreateBackup   Action = "backup.create"
	ActionRestoreBackup  Action = "backup.restore"
)

type AccessLevel string

const (
	AccessConnect   AccessLevel = "connect"
	AccessReadOnly  AccessLevel = "read_only"
	AccessReadWrite AccessLevel = "read_write"
)

type Engine struct {
	ID          string     `json:"id"`
	Kind        EngineKind `json:"kind"`
	Version     string     `json:"version"`
	VersionText string     `json:"versionText"`
	SocketPath  string     `json:"socketPath"`
	Port        int        `json:"port"`
	Status      string     `json:"status"`
	SystemdUnit string     `json:"systemdUnit"`
}

type Change struct {
	Action       Action      `json:"action"`
	EngineID     string      `json:"engineId"`
	Database     string      `json:"database,omitempty"`
	Account      string      `json:"account,omitempty"`
	AccountHost  string      `json:"accountHost,omitempty"`
	Access       AccessLevel `json:"access,omitempty"`
	BackupID     string      `json:"backupId,omitempty"`
	BackupPath   string      `json:"backupPath,omitempty"`
	BackupSHA256 string      `json:"backupSha256,omitempty"`
	SecretSHA256 string      `json:"secretSha256,omitempty"`
	RestoreToken string      `json:"restoreToken,omitempty"`
}

type Plan struct {
	ID                  string    `json:"id"`
	Kind                string    `json:"kind"`
	Change              Change    `json:"change"`
	Steps               []string  `json:"steps"`
	Warnings            []string  `json:"warnings"`
	ObservedFingerprint string    `json:"observedFingerprint"`
	Interruption        bool      `json:"interruption"`
	PlannedAt           time.Time `json:"plannedAt"`
	ExpiresAt           time.Time `json:"expiresAt"`
	Signature           string    `json:"signature,omitempty"`
}

type Execution struct {
	Plan   Plan   `json:"plan"`
	Secret string `json:"secret,omitempty"`
}

type Backup struct {
	ID        string    `json:"id"`
	Path      string    `json:"path"`
	SHA256    string    `json:"sha256"`
	SizeBytes int64     `json:"sizeBytes"`
	CreatedAt time.Time `json:"createdAt"`
	Verified  bool      `json:"verified"`
}

type Observation struct {
	Action   Action  `json:"action"`
	Engine   *Engine `json:"engine,omitempty"`
	Database string  `json:"database,omitempty"`
	Account  string  `json:"account,omitempty"`
	Access   string  `json:"access,omitempty"`
	Backup   *Backup `json:"backup,omitempty"`
	Restored bool    `json:"restored,omitempty"`
	Verified bool    `json:"verified"`
}

type Operator interface {
	Discover(context.Context) (*Engine, error)
	// Sizes takes no engine identifier because a node runs at most one
	// MySQL-family engine, which is the same reason Discover returns one.
	Sizes(context.Context) (map[string]int64, error)
	Plan(context.Context, Change) (Plan, error)
	Apply(context.Context, Execution) (Observation, error)
}

type Command struct {
	Name       string
	Args       []string
	Env        []string
	Stdin      string
	StdinPath  string
	StdoutPath string
}

type Runner interface {
	Run(context.Context, Command) ([]byte, error)
}

type execRunner struct{}

type cappedOutput struct {
	data  []byte
	limit int
}

type HostConfig struct {
	SocketPath string
	BackupRoot string
}

type HostOperator struct {
	runner     Runner
	now        func() time.Time
	socketPath string
	backupRoot string
}

func NewHostOperator(runner Runner, config HostConfig) (*HostOperator, error) {
	if runner == nil {
		runner = execRunner{}
	}
	if config.SocketPath == "" {
		config.SocketPath = "/run/mysqld/mysqld.sock"
	}
	if config.BackupRoot == "" {
		config.BackupRoot = "/var/lib/nexa-panel/backups/mysql"
	}
	if !filepath.IsAbs(config.SocketPath) || !filepath.IsAbs(config.BackupRoot) {
		return nil, errors.New("MySQL-family socket and backup root must be absolute")
	}
	return &HostOperator{runner: runner, now: time.Now, socketPath: filepath.Clean(config.SocketPath), backupRoot: filepath.Clean(config.BackupRoot)}, nil
}

var (
	namePattern  = regexp.MustCompile(`^[a-z][a-z0-9_]{1,62}$`)
	hostPattern  = regexp.MustCompile(`^(localhost|[a-zA-Z0-9_.:%-]{1,255})$`)
	hashPattern  = regexp.MustCompile(`^[a-f0-9]{64}$`)
	tokenPattern = regexp.MustCompile(`^[a-f0-9]{8,64}$`)
)
