package admintools

import (
	"encoding/json"
	"time"

	admintooloperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/admintools"
	"github.com/uptrace/bun"
)

type Status string

const (
	StatusPlanning  Status = "planning"
	StatusPlanReady Status = "plan_ready"
	StatusApplying  Status = "applying"
	StatusActive    Status = "active"
	StatusStopped   Status = "stopped"
	StatusFailed    Status = "failed"
)

type Tool struct {
	admintooloperator.Tool
	LastJobID *int64    `json:"lastJobId,omitempty"`
	Failure   string    `json:"failure,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}
type StoredPlan struct {
	ID        string                   `json:"id"`
	ToolKind  admintooloperator.Kind   `json:"toolKind"`
	Operation admintooloperator.Action `json:"operation"`
	AgentPlan admintooloperator.Plan   `json:"agentPlan"`
	CreatedAt time.Time                `json:"createdAt"`
	ExpiresAt time.Time                `json:"expiresAt"`
}

type toolModel struct {
	bun.BaseModel `bun:"table:admin_tools,alias:tool"`
	Kind          string `bun:",pk"`
	Image         string
	ContainerName string
	Port          int
	MemoryMB      int
	PIDsLimit     int `bun:"pids_limit"`
	Status        string
	SystemdUnit   string
	OnDemand      bool
	LastJobID     *int64
	Failure       *string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}
type planModel struct {
	bun.BaseModel `bun:"table:admin_tool_plans,alias:plan"`
	ID            string `bun:",pk"`
	ToolKind      string
	Operation     string
	PlanJSON      string
	CreatedAt     time.Time
	ExpiresAt     time.Time
}
type launchModel struct {
	bun.BaseModel      `bun:"table:admin_tool_launches,alias:launch"`
	ID                 string `bun:",pk"`
	ActorUserID        string
	PanelUser          string
	ToolKind           string
	SourceEngine       string
	DatabaseID         string
	AccountID          string
	LaunchTokenHash    string
	SessionTokenHash   string
	SessionCiphertext  string
	UpstreamCookieName *string
	UsedAt             *time.Time
	ExpiresAt          time.Time
	SessionExpiresAt   time.Time
	CreatedAt          time.Time
}

func (m toolModel) toTool() Tool {
	return Tool{Tool: admintooloperator.Tool{Kind: admintooloperator.Kind(m.Kind), Image: m.Image, ContainerName: m.ContainerName, Port: m.Port, MemoryMB: m.MemoryMB, PIDsLimit: m.PIDsLimit, Status: m.Status, SystemdUnit: m.SystemdUnit, OnDemand: m.OnDemand}, LastJobID: m.LastJobID, Failure: pointerString(m.Failure), CreatedAt: m.CreatedAt, UpdatedAt: m.UpdatedAt}
}

func (m planModel) toStoredPlan() (StoredPlan, error) {
	var plan admintooloperator.Plan
	if err := json.Unmarshal([]byte(m.PlanJSON), &plan); err != nil {
		return StoredPlan{}, err
	}
	return StoredPlan{ID: m.ID, ToolKind: admintooloperator.Kind(m.ToolKind), Operation: admintooloperator.Action(m.Operation), AgentPlan: plan, CreatedAt: m.CreatedAt, ExpiresAt: m.ExpiresAt}, nil
}

func pointerString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
