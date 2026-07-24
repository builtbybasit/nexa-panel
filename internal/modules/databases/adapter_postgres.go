package databases

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/uptrace/bun"

	postgresoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/postgres"
)

// postgresAdapter maps the neutral core onto the PostgreSQL operator. Unlike
// the MySQL family, a node can run several PostgreSQL instances and the panel
// can provision new ones.
type postgresAdapter struct {
	db       *bun.DB
	operator postgresoperator.Operator
	now      func() time.Time
}

func newPostgresAdapter(db *bun.DB, operator postgresoperator.Operator, now func() time.Time) *postgresAdapter {
	return &postgresAdapter{db: db, operator: operator, now: now}
}

func (a *postgresAdapter) Spec() Spec {
	return Spec{
		Engine:                "postgresql",
		DisplayName:           "PostgreSQL",
		JobKind:               "postgresql",
		CredentialLabelPrefix: "postgresql-role:",
		AdminToolHost:         "host.containers.internal",
		BackupRoot:            "/var/lib/postgresql/nexa-backups",
		UserScopedByHost:      false,
		Provisionable:         true,
		Tables:                Tables{Servers: "postgresql_instances", Users: "database_roles", Databases: "managed_databases", Grants: "database_grants", RestorePoints: "database_restore_points"},
		Columns:               Columns{ServerFK: "instance_id", OwnerFK: "owner_role_id", UserFK: "role_id"},
	}
}

type postgresInstanceModel struct {
	bun.BaseModel `bun:"table:postgresql_instances,alias:instance"`
	ID            string `bun:",pk"`
	Version       string
	ClusterName   string
	Port          int
	Status        string
	Owner         string
	DataPath      string
	SocketPath    string
	LogPath       string
	ConfigPath    string
	SystemdUnit   string
	ManagedByNexa bool
	LastJobID     *int64
	Failure       *string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

func (model postgresInstanceModel) toServer() Server {
	return Server{ID: model.ID, Engine: "postgresql", Kind: "postgresql", Version: model.Version, Cluster: model.ClusterName, Port: model.Port, Status: model.Status, SocketPath: model.SocketPath, SystemdUnit: model.SystemdUnit, ManagedByNexa: model.ManagedByNexa, LastJobID: model.LastJobID, Failure: pointerString(model.Failure), CreatedAt: model.CreatedAt, UpdatedAt: model.UpdatedAt}
}

func (a *postgresAdapter) SyncServers(ctx context.Context) ([]Server, error) {
	observed, err := a.operator.Discover(ctx)
	if err != nil {
		return nil, err
	}
	now := a.now().UTC()
	for _, item := range observed {
		model := postgresInstanceModel{ID: item.ID, Version: item.Version, ClusterName: item.Cluster, Port: item.Port, Status: item.Status, Owner: item.Owner, DataPath: item.DataPath, SocketPath: item.SocketPath, LogPath: item.LogPath, ConfigPath: item.ConfigPath, SystemdUnit: item.SystemdUnit, ManagedByNexa: item.ManagedByNexa, CreatedAt: now, UpdatedAt: now}
		_, err := a.db.NewInsert().Model(&model).On("CONFLICT (id) DO UPDATE").Set("version = EXCLUDED.version").Set("cluster_name = EXCLUDED.cluster_name").Set("port = EXCLUDED.port").Set("status = EXCLUDED.status").Set("owner = EXCLUDED.owner").Set("data_path = EXCLUDED.data_path").Set("socket_path = EXCLUDED.socket_path").Set("log_path = EXCLUDED.log_path").Set("config_path = EXCLUDED.config_path").Set("systemd_unit = EXCLUDED.systemd_unit").Set("managed_by_nexa = EXCLUDED.managed_by_nexa").Set("failure = NULL").Set("updated_at = EXCLUDED.updated_at").Exec(ctx)
		if err != nil {
			return nil, err
		}
	}
	return a.ListServers(ctx)
}

func (a *postgresAdapter) ListServers(ctx context.Context) ([]Server, error) {
	models := []postgresInstanceModel{}
	if err := a.db.NewSelect().Model(&models).OrderExpr("version DESC, cluster_name ASC").Scan(ctx); err != nil {
		return nil, err
	}
	items := make([]Server, 0, len(models))
	for _, model := range models {
		items = append(items, model.toServer())
	}
	return items, nil
}

func (a *postgresAdapter) GetServer(ctx context.Context, id string) (Server, error) {
	var model postgresInstanceModel
	if err := a.db.NewSelect().Model(&model).Where("id = ?", id).Scan(ctx); err != nil {
		return Server{}, err
	}
	return model.toServer(), nil
}

var postgresClusterPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{1,30}$`)

func (a *postgresAdapter) CreateServer(ctx context.Context, request CreateServerRequest) (Server, error) {
	version := strings.TrimSpace(request.Version)
	cluster := strings.ToLower(strings.TrimSpace(request.Cluster))
	if version != "16" && version != "17" && version != "18" {
		return Server{}, errors.New("PostgreSQL version must be 16, 17, or 18")
	}
	if !postgresClusterPattern.MatchString(cluster) {
		return Server{}, errors.New("cluster must contain 2-31 lowercase letters, numbers, or underscores")
	}
	instances, err := a.SyncServers(ctx)
	if err != nil {
		return Server{}, err
	}
	port := request.Port
	if port == 0 {
		port = suggestedPort(instances)
	}
	if port < 1024 || port > 65535 {
		return Server{}, errors.New("port must be between 1024 and 65535")
	}
	for _, item := range instances {
		if item.Port == port {
			return Server{}, errors.New("port is already used by another PostgreSQL instance")
		}
	}
	id := "postgresql_" + version + "_" + cluster
	now := a.now().UTC()
	model := &postgresInstanceModel{ID: id, Version: version, ClusterName: cluster, Port: port, Status: string(StatusPlanning), Owner: "postgres", DataPath: filepath.Join("/var/lib/postgresql", version, cluster), SocketPath: "/run/postgresql", LogPath: filepath.Join("/var/log/postgresql", fmt.Sprintf("postgresql-%s-%s.log", version, cluster)), ConfigPath: filepath.Join("/etc/postgresql", version, cluster), SystemdUnit: fmt.Sprintf("postgresql@%s-%s.service", version, cluster), ManagedByNexa: true, CreatedAt: now, UpdatedAt: now}
	if _, err := a.db.NewInsert().Model(model).Exec(ctx); err != nil {
		return Server{}, friendlyUnique(err, "PostgreSQL instance identity, port, or path is already managed")
	}
	return model.toServer(), nil
}

func suggestedPort(items []Server) int {
	used := map[int]struct{}{}
	for _, item := range items {
		used[item.Port] = struct{}{}
	}
	for port := 5432; port <= 5532; port++ {
		if _, ok := used[port]; !ok {
			return port
		}
	}
	return 0
}

// CommitServer persists the observed state of a freshly provisioned instance.
func (a *postgresAdapter) CommitServer(ctx context.Context, serverID string, observation Observation) error {
	var observed postgresoperator.Observation
	if err := json.Unmarshal(observation.Raw, &observed); err != nil {
		return err
	}
	if observed.Instance == nil {
		return errors.New("PostgreSQL instance observation is missing")
	}
	item := observed.Instance
	_, err := a.db.NewUpdate().Model((*postgresInstanceModel)(nil)).Set("version = ?", item.Version).Set("cluster_name = ?", item.Cluster).Set("port = ?", item.Port).Set("status = ?", StatusActive).Set("owner = ?", item.Owner).Set("data_path = ?", item.DataPath).Set("socket_path = ?", item.SocketPath).Set("log_path = ?", item.LogPath).Set("config_path = ?", item.ConfigPath).Set("systemd_unit = ?", item.SystemdUnit).Set("managed_by_nexa = ?", item.ManagedByNexa).Set("failure = NULL").Set("updated_at = ?", a.now().UTC()).Where("id = ?", serverID).Exec(ctx)
	return err
}

func (a *postgresAdapter) Sizes(ctx context.Context, serverID string) (map[string]int64, error) {
	return a.operator.Sizes(ctx, serverID)
}

// translate maps a neutral change to the operator's dialect, keeping each
// action's field set exactly as the operator's validation expects.
func (a *postgresAdapter) translate(change Change) (postgresoperator.Change, error) {
	switch change.Action {
	case ActionProvisionServer:
		if change.Provision == nil {
			return postgresoperator.Change{}, errors.New("PostgreSQL provisioning details are missing")
		}
		return postgresoperator.Change{Action: postgresoperator.ActionProvision, InstanceID: change.ServerID, Version: change.Provision.Version, Cluster: change.Provision.Cluster, Port: change.Provision.Port}, nil
	case ActionCreateUser, ActionRotateUser:
		return postgresoperator.Change{Action: postgresAction(change.Action), InstanceID: change.ServerID, Role: change.User, SecretSHA256: change.SecretSHA256}, nil
	case ActionDropUser:
		roleDatabases := make([]postgresoperator.RoleDatabase, 0, len(change.UserDatabases))
		for _, item := range change.UserDatabases {
			roleDatabases = append(roleDatabases, postgresoperator.RoleDatabase{Name: item.Name, NewOwner: item.NewOwner})
		}
		return postgresoperator.Change{Action: postgresoperator.ActionDropRole, InstanceID: change.ServerID, Role: change.User, RoleDatabases: roleDatabases}, nil
	case ActionCreateDatabase:
		return postgresoperator.Change{Action: postgresoperator.ActionCreateDatabase, InstanceID: change.ServerID, Database: change.Database, OwnerRole: change.Owner}, nil
	case ActionDropDatabase:
		return postgresoperator.Change{Action: postgresoperator.ActionDropDatabase, InstanceID: change.ServerID, Database: change.Database}, nil
	case ActionApplyGrant, ActionRevokeGrant:
		return postgresoperator.Change{Action: postgresAction(change.Action), InstanceID: change.ServerID, Database: change.Database, OwnerRole: change.Owner, Role: change.User, Access: postgresoperator.AccessLevel(change.Access)}, nil
	case ActionCreateBackup:
		return postgresoperator.Change{Action: postgresoperator.ActionCreateBackup, InstanceID: change.ServerID, Database: change.Database, OwnerRole: change.Owner, BackupID: change.BackupID, BackupPath: change.BackupPath}, nil
	case ActionRestoreBackup:
		return postgresoperator.Change{Action: postgresoperator.ActionRestoreBackup, InstanceID: change.ServerID, Database: change.Database, OwnerRole: change.Owner, BackupID: change.BackupID, BackupPath: change.BackupPath, BackupSHA256: change.BackupSHA256, RestoreToken: change.RestoreToken}, nil
	default:
		return postgresoperator.Change{}, errors.New("PostgreSQL change action is unsupported")
	}
}

func postgresAction(action string) postgresoperator.Action {
	switch action {
	case ActionCreateUser:
		return postgresoperator.ActionCreateRole
	case ActionRotateUser:
		return postgresoperator.ActionRotateRole
	case ActionApplyGrant:
		return postgresoperator.ActionApplyGrant
	default:
		return postgresoperator.ActionRevokeGrant
	}
}

func (a *postgresAdapter) PlanChange(ctx context.Context, change Change) (AgentPlan, error) {
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

func (a *postgresAdapter) ApplyPlan(ctx context.Context, raw json.RawMessage, secret string) (Observation, error) {
	var plan postgresoperator.Plan
	if err := json.Unmarshal(raw, &plan); err != nil {
		return Observation{}, err
	}
	observation, err := a.operator.Apply(ctx, postgresoperator.Execution{Plan: plan, Secret: secret})
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

var _ engineAdapter = (*postgresAdapter)(nil)
