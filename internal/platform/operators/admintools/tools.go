package admintools

import (
	"context"
	"errors"
	"path/filepath"
	"time"
)

const PlanKind = "nexa.admin-tool.v1"

type Kind string

const (
	PHPMyAdmin Kind = "phpmyadmin"
	PGAdmin    Kind = "pgadmin"
)

type Action string

const (
	ActionDeploy  Action = "tool.deploy"
	ActionStart   Action = "tool.start"
	ActionStop    Action = "tool.stop"
	ActionRestart Action = "tool.restart"
	ActionLaunch  Action = "tool.launch"
)

type Launch struct {
	SessionID    string `json:"sessionId,omitempty"`
	PanelUser    string `json:"panelUser,omitempty"`
	DatabaseHost string `json:"databaseHost,omitempty"`
	DatabasePort int    `json:"databasePort,omitempty"`
	Database     string `json:"database,omitempty"`
	Username     string `json:"username,omitempty"`
	SecretSHA256 string `json:"secretSha256,omitempty"`
}

type Tool struct {
	Kind          Kind   `json:"kind"`
	Image         string `json:"image"`
	ContainerName string `json:"containerName"`
	Port          int    `json:"port"`
	MemoryMB      int    `json:"memoryMb"`
	PIDsLimit     int    `json:"pidsLimit"`
	Status        string `json:"status"`
	SystemdUnit   string `json:"systemdUnit"`
	OnDemand      bool   `json:"onDemand"`
}

type Change struct {
	Action Action  `json:"action"`
	Tool   Tool    `json:"tool"`
	Launch *Launch `json:"launch,omitempty"`
}

type Execution struct {
	Plan   Plan   `json:"plan"`
	Secret string `json:"secret,omitempty"`
}

type Plan struct {
	ID                  string    `json:"id"`
	Kind                string    `json:"kind"`
	Change              Change    `json:"change"`
	Steps               []string  `json:"steps"`
	Warnings            []string  `json:"warnings"`
	ObservedFingerprint string    `json:"observedFingerprint"`
	PlannedAt           time.Time `json:"plannedAt"`
	ExpiresAt           time.Time `json:"expiresAt"`
	Signature           string    `json:"signature,omitempty"`
}

type Observation struct {
	Tool                Tool   `json:"tool"`
	Verified            bool   `json:"verified"`
	UpstreamCookieName  string `json:"upstreamCookieName,omitempty"`
	UpstreamCookieValue string `json:"upstreamCookieValue,omitempty"`
}

type Operator interface {
	Discover(context.Context) ([]Tool, error)
	Plan(context.Context, Change) (Plan, error)
	Apply(context.Context, Execution) (Observation, error)
	// KeepAlive resets an on-demand tool's idle-stop countdown so continued proxy
	// activity keeps its container alive. It is a no-op for tools without an idle
	// timer.
	KeepAlive(context.Context, Kind) error
}

type Command struct {
	Name string
	Args []string
}

type Runner interface {
	Run(context.Context, Command) ([]byte, error)
}

type execRunner struct{}

type HostConfig struct {
	QuadletRoot          string
	SystemdRoot          string
	ConfigRoot           string
	NginxAvailableRoot   string
	NginxEnabledRoot     string
	PHPMyAdminConfigRoot string
}

type HostOperator struct {
	runner               Runner
	now                  func() time.Time
	quadletRoot          string
	systemdRoot          string
	configRoot           string
	nginxAvailableRoot   string
	nginxEnabledRoot     string
	phpMyAdminConfigRoot string
}

func NewHostOperator(runner Runner, config HostConfig) (*HostOperator, error) {
	if runner == nil {
		runner = execRunner{}
	}
	if config.QuadletRoot == "" {
		config.QuadletRoot = "/etc/containers/systemd"
	}
	if config.SystemdRoot == "" {
		config.SystemdRoot = "/etc/systemd/system"
	}
	if config.ConfigRoot == "" {
		config.ConfigRoot = "/etc/nexa-panel/admin-tools"
	}
	if config.NginxAvailableRoot == "" {
		config.NginxAvailableRoot = "/etc/nginx/sites-available"
	}
	if config.NginxEnabledRoot == "" {
		config.NginxEnabledRoot = "/etc/nginx/sites-enabled"
	}
	if config.PHPMyAdminConfigRoot == "" {
		config.PHPMyAdminConfigRoot = "/etc/phpmyadmin/conf.d"
	}
	if !filepath.IsAbs(config.QuadletRoot) || !filepath.IsAbs(config.SystemdRoot) || !filepath.IsAbs(config.ConfigRoot) || !filepath.IsAbs(config.NginxAvailableRoot) || !filepath.IsAbs(config.NginxEnabledRoot) || !filepath.IsAbs(config.PHPMyAdminConfigRoot) {
		return nil, errors.New("admin tool managed roots must be absolute")
	}
	return &HostOperator{
		runner: runner, now: time.Now,
		quadletRoot: filepath.Clean(config.QuadletRoot), systemdRoot: filepath.Clean(config.SystemdRoot), configRoot: filepath.Clean(config.ConfigRoot),
		nginxAvailableRoot: filepath.Clean(config.NginxAvailableRoot), nginxEnabledRoot: filepath.Clean(config.NginxEnabledRoot), phpMyAdminConfigRoot: filepath.Clean(config.PHPMyAdminConfigRoot),
	}, nil
}

func Defaults() []Tool {
	return []Tool{
		{Kind: PHPMyAdmin, Port: 18080, SystemdUnit: "nexa-phpmyadmin.service", OnDemand: false},
		{Kind: PGAdmin, Image: "docker.io/dpage/pgadmin4:9.16", ContainerName: "nexa-pgadmin", Port: 18081, MemoryMB: 512, PIDsLimit: 192, SystemdUnit: "nexa-pgadmin.service", OnDemand: true},
	}
}
