package databases

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/uptrace/bun"

	mysqloperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/mysql"
)

// mysqlAdapter maps the neutral core onto the MySQL-family operator. The node
// runs at most one MySQL or MariaDB engine, which is discovered rather than
// provisioned, and its users carry a client host scope.
type mysqlAdapter struct {
	db       *bun.DB
	operator mysqloperator.Operator
	now      func() time.Time
}

func newMySQLAdapter(db *bun.DB, operator mysqloperator.Operator, now func() time.Time) *mysqlAdapter {
	return &mysqlAdapter{db: db, operator: operator, now: now}
}

func (a *mysqlAdapter) Spec() Spec {
	return Spec{
		Engine:                "mysql",
		DisplayName:           "MySQL",
		JobKind:               "mysql_family",
		CredentialLabelPrefix: "mysql-account:",
		AdminToolHost:         "localhost",
		BackupRoot:            "/var/lib/nexa-panel/backups/mysql",
		UserScopedByHost:      true,
		Provisionable:         false,
		Tables:                Tables{Servers: "mysql_family_engines", Users: "mysql_accounts", Databases: "mysql_databases", Grants: "mysql_grants", RestorePoints: "mysql_restore_points"},
		Columns:               Columns{ServerFK: "engine_id", OwnerFK: "owner_account_id", UserFK: "account_id"},
	}
}

type mysqlEngineModel struct {
	bun.BaseModel `bun:"table:mysql_family_engines,alias:engine"`
	ID            string `bun:",pk"`
	Kind          string
	Version       string
	VersionText   string
	Port          int
	Status        string
	SocketPath    string
	SystemdUnit   string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

func (model mysqlEngineModel) toServer() Server {
	return Server{ID: model.ID, Engine: "mysql", Kind: model.Kind, Version: model.Version, VersionText: model.VersionText, Port: model.Port, Status: model.Status, SocketPath: model.SocketPath, SystemdUnit: model.SystemdUnit, CreatedAt: model.CreatedAt, UpdatedAt: model.UpdatedAt}
}

func (a *mysqlAdapter) SyncServers(ctx context.Context) ([]Server, error) {
	observed, err := a.operator.Discover(ctx)
	if err != nil {
		return nil, err
	}
	now := a.now().UTC()
	if observed != nil {
		model := mysqlEngineModel{ID: observed.ID, Kind: string(observed.Kind), Version: observed.Version, VersionText: observed.VersionText, Port: observed.Port, Status: observed.Status, SocketPath: observed.SocketPath, SystemdUnit: observed.SystemdUnit, CreatedAt: now, UpdatedAt: now}
		_, err := a.db.NewInsert().Model(&model).On("CONFLICT (id) DO UPDATE").Set("kind = EXCLUDED.kind").Set("version = EXCLUDED.version").Set("version_text = EXCLUDED.version_text").Set("port = EXCLUDED.port").Set("status = EXCLUDED.status").Set("socket_path = EXCLUDED.socket_path").Set("systemd_unit = EXCLUDED.systemd_unit").Set("updated_at = EXCLUDED.updated_at").Exec(ctx)
		if err != nil {
			return nil, err
		}
	}
	return a.ListServers(ctx)
}

func (a *mysqlAdapter) ListServers(ctx context.Context) ([]Server, error) {
	models := []mysqlEngineModel{}
	if err := a.db.NewSelect().Model(&models).OrderExpr("kind ASC").Scan(ctx); err != nil {
		return nil, err
	}
	items := make([]Server, 0, len(models))
	for _, model := range models {
		items = append(items, model.toServer())
	}
	return items, nil
}

func (a *mysqlAdapter) GetServer(ctx context.Context, id string) (Server, error) {
	var model mysqlEngineModel
	if err := a.db.NewSelect().Model(&model).Where("id = ?", id).Scan(ctx); err != nil {
		return Server{}, err
	}
	return model.toServer(), nil
}

func (a *mysqlAdapter) CreateServer(context.Context, CreateServerRequest) (Server, error) {
	return Server{}, errors.New("the MySQL-family engine is discovered from the node and cannot be provisioned here")
}

func (a *mysqlAdapter) CommitServer(context.Context, string, Observation) error {
	return errors.New("MySQL-family engines are discovered, not provisioned")
}

// Sizes ignores the server ID: one probe covers the node's single engine.
func (a *mysqlAdapter) Sizes(ctx context.Context, _ string) (map[string]int64, error) {
	return a.operator.Sizes(ctx)
}

// translate maps a neutral change to the operator's dialect, keeping each
// action's field set exactly as the operator's validation expects.
func (a *mysqlAdapter) translate(change Change) (mysqloperator.Change, error) {
	switch change.Action {
	case ActionCreateUser, ActionRotateUser, ActionDropUser:
		return mysqloperator.Change{Action: mysqlAction(change.Action), EngineID: change.ServerID, Account: change.User, AccountHost: change.UserHost, SecretSHA256: change.SecretSHA256}, nil
	case ActionCreateDatabase:
		return mysqloperator.Change{Action: mysqloperator.ActionCreateDatabase, EngineID: change.ServerID, Database: change.Database, Account: change.Owner, AccountHost: change.OwnerHost}, nil
	case ActionDropDatabase:
		return mysqloperator.Change{Action: mysqloperator.ActionDropDatabase, EngineID: change.ServerID, Database: change.Database}, nil
	case ActionApplyGrant, ActionRevokeGrant:
		return mysqloperator.Change{Action: mysqlAction(change.Action), EngineID: change.ServerID, Database: change.Database, Account: change.User, AccountHost: change.UserHost, Access: mysqloperator.AccessLevel(change.Access)}, nil
	case ActionCreateBackup:
		return mysqloperator.Change{Action: mysqloperator.ActionCreateBackup, EngineID: change.ServerID, Database: change.Database, BackupID: change.BackupID, BackupPath: change.BackupPath}, nil
	case ActionRestoreBackup:
		return mysqloperator.Change{Action: mysqloperator.ActionRestoreBackup, EngineID: change.ServerID, Database: change.Database, BackupID: change.BackupID, BackupPath: change.BackupPath, BackupSHA256: change.BackupSHA256, RestoreToken: change.RestoreToken}, nil
	default:
		return mysqloperator.Change{}, errors.New("MySQL-family change action is unsupported")
	}
}

func mysqlAction(action string) mysqloperator.Action {
	switch action {
	case ActionCreateUser:
		return mysqloperator.ActionCreateAccount
	case ActionRotateUser:
		return mysqloperator.ActionRotateAccount
	case ActionDropUser:
		return mysqloperator.ActionDropAccount
	case ActionApplyGrant:
		return mysqloperator.ActionApplyGrant
	default:
		return mysqloperator.ActionRevokeGrant
	}
}

func (a *mysqlAdapter) PlanChange(ctx context.Context, change Change) (AgentPlan, error) {
	translated, err := a.translate(change)
	if err != nil {
		return AgentPlan{}, err
	}
	plan, err := a.operator.Plan(ctx, translated)
	if err != nil {
		return AgentPlan{}, err
	}
	raw, err := json.Marshal(plan)
	if err != nil {
		return AgentPlan{}, err
	}
	return AgentPlan{Raw: raw, Steps: plan.Steps}, nil
}

func (a *mysqlAdapter) ApplyPlan(ctx context.Context, raw json.RawMessage, secret string) (Observation, error) {
	var plan mysqloperator.Plan
	if err := json.Unmarshal(raw, &plan); err != nil {
		return Observation{}, err
	}
	observation, err := a.operator.Apply(ctx, mysqloperator.Execution{Plan: plan, Secret: secret})
	if err != nil {
		return Observation{}, err
	}
	encoded, err := json.Marshal(observation)
	if err != nil {
		return Observation{}, err
	}
	neutral := Observation{Verified: observation.Verified, Restored: observation.Restored, Raw: encoded}
	if observation.Backup != nil {
		neutral.Backup = &BackupObservation{SHA256: observation.Backup.SHA256, SizeBytes: observation.Backup.SizeBytes, CreatedAt: observation.Backup.CreatedAt, Verified: observation.Backup.Verified}
	}
	return neutral, nil
}

var _ engineAdapter = (*mysqlAdapter)(nil)
