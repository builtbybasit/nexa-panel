package applications

import (
	"encoding/json"
	"time"

	packagesoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/packages"
	"github.com/uptrace/bun"
)

const schema = `
CREATE TABLE applications (
  id TEXT PRIMARY KEY,
  app TEXT NOT NULL,
  version TEXT NOT NULL,
  status TEXT NOT NULL,
  installed_version TEXT,
  last_job_id INTEGER REFERENCES jobs(id),
  failure TEXT,
  created_at TIMESTAMP NOT NULL,
  updated_at TIMESTAMP NOT NULL
);
CREATE TABLE application_plans (
  id TEXT PRIMARY KEY,
  application_id TEXT NOT NULL REFERENCES applications(id) ON DELETE CASCADE,
  operation TEXT NOT NULL,
  plan_json TEXT NOT NULL,
  created_at TIMESTAMP NOT NULL,
  expires_at TIMESTAMP NOT NULL
);
CREATE INDEX application_plans_app_idx ON application_plans(application_id, created_at DESC);
`

// Status is the control-plane workflow state of an installable application.
type Status string

const (
	StatusAvailable  Status = "available"
	StatusPlanning   Status = "planning"
	StatusPlanReady  Status = "plan_ready"
	StatusInstalling Status = "installing"
	StatusRemoving   Status = "removing"
	StatusInstalled  Status = "installed"
	StatusFailed     Status = "failed"
)

// Application is one catalog card returned to the UI.
type Application struct {
	ID               string `json:"id"`
	App              string `json:"app"`
	Version          string `json:"version,omitempty"`
	Label            string `json:"label"`
	Summary          string `json:"summary"`
	Category         string `json:"category"`
	Status           string `json:"status"`
	InstalledVersion string `json:"installedVersion,omitempty"`
	Managed          bool   `json:"managed"`
	LastJobID        *int64 `json:"lastJobId,omitempty"`
	Failure          string `json:"failure,omitempty"`
}

// StoredPlan is a persisted, agent-signed package plan awaiting approval.
type StoredPlan struct {
	ID            string                  `json:"id"`
	ApplicationID string                  `json:"applicationId"`
	Operation     packagesoperator.Action `json:"operation"`
	AgentPlan     packagesoperator.Plan   `json:"agentPlan"`
	CreatedAt     time.Time               `json:"createdAt"`
	ExpiresAt     time.Time               `json:"expiresAt"`
}

type applicationModel struct {
	bun.BaseModel    `bun:"table:applications,alias:application"`
	ID               string `bun:",pk"`
	App              string
	Version          string
	Status           string
	InstalledVersion *string
	LastJobID        *int64
	Failure          *string
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

type planModel struct {
	bun.BaseModel `bun:"table:application_plans,alias:plan"`
	ID            string `bun:",pk"`
	ApplicationID string
	Operation     string
	PlanJSON      string
	CreatedAt     time.Time
	ExpiresAt     time.Time
}

func (m planModel) toStoredPlan() (StoredPlan, error) {
	var plan packagesoperator.Plan
	if err := json.Unmarshal([]byte(m.PlanJSON), &plan); err != nil {
		return StoredPlan{}, err
	}
	return StoredPlan{
		ID: m.ID, ApplicationID: m.ApplicationID,
		Operation: packagesoperator.Action(m.Operation), AgentPlan: plan,
		CreatedAt: m.CreatedAt, ExpiresAt: m.ExpiresAt,
	}, nil
}

func pointerString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
