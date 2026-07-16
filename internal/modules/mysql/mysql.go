package mysql

import (
	"errors"

	"github.com/nexa-panel/nexa-panel/internal/platform/jobs"
	mysqloperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/mysql"
	"github.com/nexa-panel/nexa-panel/internal/platform/persistence"

	"github.com/nexa-panel/nexa-panel/internal/platform/secrets"
	"regexp"

	"time"

	"context"
	"github.com/uptrace/bun"

	"github.com/nexa-panel/nexa-panel/internal/platform/module"
)

const (
	resourceEngine       = "engine"
	resourceAccount      = "account"
	resourceDatabase     = "database"
	resourceGrant        = "grant"
	resourceRestorePoint = "restore_point"
)

var resourceNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{1,62}$`)

type Module struct {
	database   *bun.DB
	jobs       *jobs.Module
	cipher     secrets.Cipher
	operator   mysqloperator.Operator
	now        func() time.Time
	backupRoot string
}

type CreateAccountRequest struct {
	EngineID string `json:"engineId"`
	Name     string `json:"name"`
	Host     string `json:"host,omitempty"`
}

type CreateDatabaseRequest struct {
	EngineID       string `json:"engineId"`
	Name           string `json:"name"`
	OwnerAccountID string `json:"ownerAccountId"`
}

type CreateGrantRequest struct {
	DatabaseID string                    `json:"databaseId"`
	AccountID  string                    `json:"accountId"`
	Access     mysqloperator.AccessLevel `json:"access"`
}

type AdminToolCredential struct {
	Host     string
	Port     int
	Database string
	Username string
	Secret   []byte
}

func New(ctx context.Context, database *bun.DB, queue *jobs.Module, cipher secrets.Cipher, operator mysqloperator.Operator) (*Module, error) {
	if database == nil || queue == nil || cipher == nil || operator == nil {
		return nil, errors.New("databases state, jobs, secret cipher, and MySQL-family operator are required")
	}
	if err := persistence.Migrate(ctx, database, "mysql_databases", []string{schema}); err != nil {
		return nil, err
	}
	m := &Module{database: database, jobs: queue, cipher: cipher, operator: operator, now: time.Now, backupRoot: "/var/lib/nexa-panel/backups/mysql"}
	if err := queue.RegisterHandler("mysql_family.plan", m.planJob); err != nil {
		return nil, err
	}
	if err := queue.RegisterHandler("mysql_family.apply", m.applyJob); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *Module) Descriptor() module.Descriptor {
	return module.Descriptor{ID: "mysql-databases", Name: "MySQL & MariaDB", Version: "0.1.0", Description: "One native MySQL-family engine, databases, accounts, scoped grants, logical backups, and restore.", Dependencies: []string{"identity", "jobs"}, RequiredCapabilities: []string{"mysql-client"}, EstimatedIdleBytes: 1024 * 1024}
}

func (m *Module) Register(registry module.Registry) error {
	return m.registerHTTP(registry)
}
