// Package databases manages every database engine on the node behind one API.
// A single core owns the shared lifecycle — desired state in SQLite, an
// agent-signed plan per change, review, apply, observe — while an engine
// adapter per family (MySQL/MariaDB, PostgreSQL) contributes only what
// genuinely differs: discovery, provisioning, and the translation of neutral
// changes into the engine operator's dialect. Adding an engine means adding an
// adapter, not another module.
package databases

import (
	"context"
	"errors"
	"regexp"
	"time"

	"github.com/uptrace/bun"

	"github.com/nexa-panel/nexa-panel/internal/platform/jobs"
	"github.com/nexa-panel/nexa-panel/internal/platform/module"
	mysqloperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/mysql"
	postgresoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/postgres"
	"github.com/nexa-panel/nexa-panel/internal/platform/secrets"
)

const (
	resourceServer       = "server"
	resourceUser         = "user"
	resourceDatabase     = "database"
	resourceGrant        = "grant"
	resourceRestorePoint = "restore_point"
)

var resourceNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{1,62}$`)

type Module struct {
	database *bun.DB
	jobs     *jobs.Module
	cipher   secrets.Cipher
	now      func() time.Time
	// engines is ordered: listings merge in this order before sorting, and
	// adapter resolution probes engines in this order.
	engines []*engine
}

// engine pairs an adapter with the parameterized store that reads and writes
// its tables. Everything the core does for a resource goes through one of
// these two handles.
type engine struct {
	adapter engineAdapter
	spec    Spec
	store   *store
}

type CreateServerRequest struct {
	Engine  string `json:"engine"`
	Version string `json:"version"`
	Cluster string `json:"cluster,omitempty"`
	Port    int    `json:"port,omitempty"`
}

type CreateUserRequest struct {
	ServerID string `json:"serverId"`
	Name     string `json:"name"`
	Host     string `json:"host,omitempty"`
	// Password is chosen (or generated) in the client and applied to the
	// engine as-is; the panel stores only its ciphertext and digest.
	Password string `json:"password"`
}

type SetPasswordRequest struct {
	Password string `json:"password"`
}

type CreateDatabaseRequest struct {
	ServerID    string `json:"serverId"`
	Name        string `json:"name"`
	OwnerUserID string `json:"ownerUserId"`
	// Optional: the site this database is being created for. Recording it here
	// is what lets a site teardown find its databases by relation instead of by
	// guessing at their names.
	SiteID string `json:"siteId,omitempty"`
}

type CreateGrantRequest struct {
	DatabaseID string `json:"databaseId"`
	UserID     string `json:"userId"`
	Access     string `json:"access"`
}

type AdminToolCredential struct {
	Host     string
	Port     int
	Database string
	Username string
	Secret   []byte
}

// BackupTarget is what a logical dump or restore needs to reach a managed
// database, expressed engine-neutrally for the backups module.
type BackupTarget struct {
	Engine  string
	Name    string
	Version string
	Port    int
	Socket  string
}

func New(_ context.Context, database *bun.DB, queue *jobs.Module, cipher secrets.Cipher, mysqlOperator mysqloperator.Operator, postgresOperator postgresoperator.Operator) (*Module, error) {
	if database == nil || queue == nil || cipher == nil || mysqlOperator == nil || postgresOperator == nil {
		return nil, errors.New("databases state, jobs, secret cipher, and both engine operators are required")
	}
	m := &Module{database: database, jobs: queue, cipher: cipher, now: time.Now}
	adapters := []engineAdapter{
		newMySQLAdapter(database, mysqlOperator, m.clock),
		newPostgresAdapter(database, postgresOperator, m.clock),
	}
	for _, adapter := range adapters {
		spec := adapter.Spec()
		eng := &engine{adapter: adapter, spec: spec, store: &store{db: database, spec: spec, now: m.clock}}
		m.engines = append(m.engines, eng)
		if err := queue.RegisterHandler(spec.JobKind+".execute", m.executeJobFor(eng)); err != nil {
			return nil, err
		}
	}
	return m, nil
}

func (m *Module) clock() time.Time { return m.now() }

func (m *Module) Descriptor() module.Descriptor {
	return module.Descriptor{ID: "databases", Name: "Databases", Version: "0.2.0", Description: "MySQL, MariaDB, and PostgreSQL servers, databases, users, scoped grants, logical backups, and restore behind one engine-neutral API.", Dependencies: []string{"identity", "jobs"}, RequiredCapabilities: []string{"mysql-client", "postgresql-common"}, EstimatedIdleBytes: 2 * 1024 * 1024}
}

func (m *Module) Register(registry module.Registry) error {
	return m.registerHTTP(registry)
}

// engineByKey resolves an engine family key ("mysql", "postgresql").
func (m *Module) engineByKey(key string) (*engine, error) {
	for _, eng := range m.engines {
		if eng.spec.Engine == key {
			return eng, nil
		}
	}
	return nil, errors.New("database engine is unsupported")
}

// engineForServer finds the engine whose server table holds the given ID.
// Server IDs are engine-prefixed and random, so probing each engine's table is
// unambiguous.
func (m *Module) engineForServer(ctx context.Context, serverID string) (*engine, error) {
	for _, eng := range m.engines {
		if _, err := eng.adapter.GetServer(ctx, serverID); err == nil {
			return eng, nil
		}
	}
	return nil, errors.New("database server not found")
}

// engineForResource finds the engine whose table holds the resource. Resource
// IDs are random 96-bit tokens, so existence in exactly one engine's table is
// how a bare ID stays engine-neutral at the API surface.
func (m *Module) engineForResource(ctx context.Context, resourceType, id string) (*engine, error) {
	if resourceType == resourceServer {
		return m.engineForServer(ctx, id)
	}
	for _, eng := range m.engines {
		exists, err := eng.store.ResourceExists(ctx, resourceType, id)
		if err != nil {
			return nil, err
		}
		if exists {
			return eng, nil
		}
	}
	return nil, errors.New("database resource not found")
}
