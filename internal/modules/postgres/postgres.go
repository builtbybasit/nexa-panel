package postgres

import (
	"github.com/nexa-panel/nexa-panel/internal/platform/jobs"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/platform/persistence"
	"github.com/uptrace/bun"

	"context"

	"github.com/nexa-panel/nexa-panel/internal/platform/secrets"

	"regexp"

	"github.com/nexa-panel/nexa-panel/internal/platform/module"

	"errors"
	postgresoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/postgres"
)

const (
	resourceInstance     = "instance"
	resourceRole         = "role"
	resourceDatabase     = "database"
	resourceGrant        = "grant"
	resourceRestorePoint = "restore_point"
)

var resourceNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{1,62}$`)

type Module struct {
	database   *bun.DB
	jobs       *jobs.Module
	cipher     secrets.Cipher
	operator   postgresoperator.Operator
	now        func() time.Time
	backupRoot string
}

type CreateInstanceRequest struct {
	Version string `json:"version"`
	Cluster string `json:"cluster"`
	Port    int    `json:"port,omitempty"`
}

type CreateRoleRequest struct {
	InstanceID string `json:"instanceId"`
	Name       string `json:"name"`
}

type CreateDatabaseRequest struct {
	InstanceID  string `json:"instanceId"`
	Name        string `json:"name"`
	OwnerRoleID string `json:"ownerRoleId"`
}

type CreateGrantRequest struct {
	DatabaseID string                       `json:"databaseId"`
	RoleID     string                       `json:"roleId"`
	Access     postgresoperator.AccessLevel `json:"access"`
}

type AdminToolCredential struct {
	Host     string
	Port     int
	Database string
	Username string
	Secret   []byte
}

func New(ctx context.Context, database *bun.DB, queue *jobs.Module, cipher secrets.Cipher, operator postgresoperator.Operator) (*Module, error) {
	if database == nil || queue == nil || cipher == nil || operator == nil {
		return nil, errors.New("databases state, jobs, secret cipher, and PostgreSQL operator are required")
	}
	if err := persistence.Migrate(ctx, database, "databases", []string{schema, databaseSizeSchema}); err != nil {
		return nil, err
	}
	m := &Module{database: database, jobs: queue, cipher: cipher, operator: operator, now: time.Now, backupRoot: "/var/lib/postgresql/nexa-backups"}
	if err := queue.RegisterHandler("postgresql.plan", m.planJob); err != nil {
		return nil, err
	}
	if err := queue.RegisterHandler("postgresql.apply", m.applyJob); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *Module) Descriptor() module.Descriptor {
	return module.Descriptor{ID: "databases", Name: "Databases", Version: "0.1.0", Description: "PostgreSQL 16-18 instances, databases, roles, scoped grants, logical backups, and restore.", Dependencies: []string{"identity", "jobs"}, RequiredCapabilities: []string{"postgresql-common"}, EstimatedIdleBytes: 1024 * 1024}
}

func (m *Module) Register(registry module.Registry) error {
	return m.registerHTTP(registry)
}
