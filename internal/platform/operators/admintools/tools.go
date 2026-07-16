package admintools

import (
	"context"
	"time"

	"errors"
	"path/filepath"
)

const PlanKind = "nexa.admin-tool.v1"

type Kind string

const (
	PHPMyAdmin Kind = "phpmyadmin"
	PGAdmin    Kind = "pgadmin"
)

type Action string

const (
	ActionDeploy Action = "tool.deploy"
	ActionStart  Action = "tool.start"
	ActionStop   Action = "tool.stop"
	ActionLaunch Action = "tool.launch"
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
	QuadletRoot string
	ConfigRoot  string
}

type HostOperator struct {
	runner      Runner
	now         func() time.Time
	quadletRoot string
	configRoot  string
}

func NewHostOperator(runner Runner, config HostConfig) (*HostOperator, error) {
	if runner == nil {
		runner = execRunner{}
	}
	if config.QuadletRoot == "" {
		config.QuadletRoot = "/etc/containers/systemd"
	}
	if config.ConfigRoot == "" {
		config.ConfigRoot = "/etc/nexa-panel/admin-tools"
	}
	if !filepath.IsAbs(config.QuadletRoot) || !filepath.IsAbs(config.ConfigRoot) {
		return nil, errors.New("admin tool managed roots must be absolute")
	}
	return &HostOperator{runner: runner, now: time.Now, quadletRoot: filepath.Clean(config.QuadletRoot), configRoot: filepath.Clean(config.ConfigRoot)}, nil
}

func Defaults() []Tool {
	return []Tool{
		{Kind: PHPMyAdmin, Image: "docker.io/library/phpmyadmin:5.2.3", ContainerName: "nexa-phpmyadmin", Port: 18080, MemoryMB: 128, PIDsLimit: 128, SystemdUnit: "nexa-phpmyadmin.service", OnDemand: true},
		{Kind: PGAdmin, Image: "docker.io/dpage/pgadmin4:9.16", ContainerName: "nexa-pgadmin", Port: 18081, MemoryMB: 256, PIDsLimit: 192, SystemdUnit: "nexa-pgadmin.service", OnDemand: true},
	}
}
