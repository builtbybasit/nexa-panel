package postgres

import (
	"context"
	"path/filepath"
	"regexp"
	"time"

	"errors"
)

const PlanKind = "nexa.postgresql.v1"

type Action string

const (
	ActionProvision      Action = "instance.provision"
	ActionCreateRole     Action = "role.create"
	ActionRotateRole     Action = "role.rotate"
	ActionCreateDatabase Action = "database.create"
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

type Instance struct {
	ID            string `json:"id"`
	Version       string `json:"version"`
	Cluster       string `json:"cluster"`
	Port          int    `json:"port"`
	Status        string `json:"status"`
	Owner         string `json:"owner"`
	DataPath      string `json:"dataPath"`
	SocketPath    string `json:"socketPath"`
	LogPath       string `json:"logPath"`
	ConfigPath    string `json:"configPath"`
	SystemdUnit   string `json:"systemdUnit"`
	ManagedByNexa bool   `json:"managedByNexa"`
}

type Change struct {
	Action       Action      `json:"action"`
	InstanceID   string      `json:"instanceId"`
	Version      string      `json:"version,omitempty"`
	Cluster      string      `json:"cluster,omitempty"`
	Port         int         `json:"port,omitempty"`
	Database     string      `json:"database,omitempty"`
	OwnerRole    string      `json:"ownerRole,omitempty"`
	Role         string      `json:"role,omitempty"`
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
	Action   Action    `json:"action"`
	Instance *Instance `json:"instance,omitempty"`
	Database string    `json:"database,omitempty"`
	Role     string    `json:"role,omitempty"`
	Access   string    `json:"access,omitempty"`
	Backup   *Backup   `json:"backup,omitempty"`
	Restored bool      `json:"restored,omitempty"`
	Verified bool      `json:"verified"`
}

type Operator interface {
	Discover(context.Context) ([]Instance, error)
	Plan(context.Context, Change) (Plan, error)
	Apply(context.Context, Execution) (Observation, error)
}

type Command struct {
	Name  string
	Args  []string
	Stdin string
}

type Runner interface {
	Run(context.Context, Command) ([]byte, error)
}

type execRunner struct{}

type cappedOutput struct {
	data  []byte
	limit int
}

type HostOperator struct {
	runner     Runner
	now        func() time.Time
	dataRoot   string
	configRoot string
	logRoot    string
	socketRoot string
	backupRoot string
}

type HostConfig struct {
	DataRoot   string
	ConfigRoot string
	LogRoot    string
	SocketRoot string
	BackupRoot string
}

func NewHostOperator(runner Runner, config HostConfig) (*HostOperator, error) {
	if runner == nil {
		runner = execRunner{}
	}
	defaults := map[*string]string{
		&config.DataRoot: "/var/lib/postgresql", &config.ConfigRoot: "/etc/postgresql",
		&config.LogRoot: "/var/log/postgresql", &config.SocketRoot: "/run/postgresql",
		&config.BackupRoot: "/var/lib/postgresql/nexa-backups",
	}
	for target, fallback := range defaults {
		if *target == "" {
			*target = fallback
		}
		if !filepath.IsAbs(*target) {
			return nil, errors.New("PostgreSQL managed roots must be absolute")
		}
		*target = filepath.Clean(*target)
	}
	return &HostOperator{runner: runner, now: time.Now, dataRoot: config.DataRoot, configRoot: config.ConfigRoot, logRoot: config.LogRoot, socketRoot: config.SocketRoot, backupRoot: config.BackupRoot}, nil
}

var (
	clusterPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{1,30}$`)
	namePattern    = regexp.MustCompile(`^[a-z][a-z0-9_]{1,62}$`)
	hashPattern    = regexp.MustCompile(`^[a-f0-9]{64}$`)
	tokenPattern   = regexp.MustCompile(`^[a-f0-9]{8,64}$`)
)

type jsonString string

type jsonInt int
