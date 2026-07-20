package postgres

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/nexa-panel/nexa-panel/internal/platform/jobs"
	postgresoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/postgres"
)

func (m *Module) SyncInstances(ctx context.Context) ([]Instance, error) {
	observed, err := m.operator.Discover(ctx)
	if err != nil {
		return nil, err
	}
	now := m.now().UTC()
	for _, item := range observed {
		model := instanceModel{ID: item.ID, Version: item.Version, ClusterName: item.Cluster, Port: item.Port, Status: item.Status, Owner: item.Owner, DataPath: item.DataPath, SocketPath: item.SocketPath, LogPath: item.LogPath, ConfigPath: item.ConfigPath, SystemdUnit: item.SystemdUnit, ManagedByNexa: item.ManagedByNexa, CreatedAt: now, UpdatedAt: now}
		_, err := m.database.NewInsert().Model(&model).On("CONFLICT (id) DO UPDATE").Set("version = EXCLUDED.version").Set("cluster_name = EXCLUDED.cluster_name").Set("port = EXCLUDED.port").Set("status = EXCLUDED.status").Set("owner = EXCLUDED.owner").Set("data_path = EXCLUDED.data_path").Set("socket_path = EXCLUDED.socket_path").Set("log_path = EXCLUDED.log_path").Set("config_path = EXCLUDED.config_path").Set("systemd_unit = EXCLUDED.systemd_unit").Set("managed_by_nexa = EXCLUDED.managed_by_nexa").Set("failure = NULL").Set("updated_at = EXCLUDED.updated_at").Exec(ctx)
		if err != nil {
			return nil, err
		}
	}
	return m.ListInstances(ctx)
}

func (m *Module) ListInstances(ctx context.Context) ([]Instance, error) {
	models := []instanceModel{}
	if err := m.database.NewSelect().Model(&models).OrderExpr("version DESC, cluster_name ASC").Scan(ctx); err != nil {
		return nil, err
	}
	items := make([]Instance, 0, len(models))
	for _, model := range models {
		items = append(items, model.toInstance())
	}
	return items, nil
}

func (m *Module) CreateInstance(ctx context.Context, request CreateInstanceRequest, actor *string) (Instance, jobs.Job, error) {
	request.Version = strings.TrimSpace(request.Version)
	request.Cluster = strings.ToLower(strings.TrimSpace(request.Cluster))
	if request.Version != "16" && request.Version != "17" && request.Version != "18" {
		return Instance{}, jobs.Job{}, errors.New("PostgreSQL version must be 16, 17, or 18")
	}
	if !regexp.MustCompile(`^[a-z][a-z0-9_]{1,30}$`).MatchString(request.Cluster) {
		return Instance{}, jobs.Job{}, errors.New("cluster must contain 2-31 lowercase letters, numbers, or underscores")
	}
	instances, err := m.SyncInstances(ctx)
	if err != nil {
		return Instance{}, jobs.Job{}, err
	}
	if request.Port == 0 {
		request.Port = suggestedPort(instances)
	}
	if request.Port < 1024 || request.Port > 65535 {
		return Instance{}, jobs.Job{}, errors.New("port must be between 1024 and 65535")
	}
	for _, item := range instances {
		if item.Port == request.Port {
			return Instance{}, jobs.Job{}, errors.New("port is already used by another PostgreSQL instance")
		}
	}
	id := "postgresql_" + request.Version + "_" + request.Cluster
	now := m.now().UTC()
	model := &instanceModel{ID: id, Version: request.Version, ClusterName: request.Cluster, Port: request.Port, Status: string(StatusPlanning), Owner: "postgres", DataPath: filepath.Join("/var/lib/postgresql", request.Version, request.Cluster), SocketPath: "/run/postgresql", LogPath: filepath.Join("/var/log/postgresql", fmt.Sprintf("postgresql-%s-%s.log", request.Version, request.Cluster)), ConfigPath: filepath.Join("/etc/postgresql", request.Version, request.Cluster), SystemdUnit: fmt.Sprintf("postgresql@%s-%s.service", request.Version, request.Cluster), ManagedByNexa: true, CreatedAt: now, UpdatedAt: now}
	if _, err := m.database.NewInsert().Model(model).Exec(ctx); err != nil {
		return Instance{}, jobs.Job{}, friendlyUnique(err, "PostgreSQL instance identity, port, or path is already managed")
	}
	job, err := m.submitPlan(ctx, resourceInstance, id, postgresoperator.ActionProvision, actor)
	if err != nil {
		m.failResource(ctx, resourceInstance, id, err)
		return model.toInstance(), jobs.Job{}, err
	}
	model.LastJobID = &job.ID
	_, err = m.database.NewUpdate().Model((*instanceModel)(nil)).Set("last_job_id = ?", job.ID).Set("updated_at = ?", m.now().UTC()).Where("id = ?", id).Exec(ctx)
	return model.toInstance(), job, err
}

func suggestedPort(items []Instance) int {
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

func (m *Module) activeInstance(ctx context.Context, id string) (instanceModel, error) {
	model, err := m.getInstanceModel(ctx, id)
	if err != nil || (model.Status != string(StatusActive) && model.Status != "online") {
		return instanceModel{}, errors.New("PostgreSQL instance must be online")
	}
	return model, nil
}

func (m *Module) getInstanceModel(ctx context.Context, id string) (instanceModel, error) {
	var model instanceModel
	err := m.database.NewSelect().Model(&model).Where("id = ?", id).Scan(ctx)
	return model, err
}
