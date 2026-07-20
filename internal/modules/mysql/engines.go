package mysql

import (
	"context"
	"errors"
)

func (m *Module) SyncEngines(ctx context.Context) ([]Engine, error) {
	observed, err := m.operator.Discover(ctx)
	if err != nil {
		return nil, err
	}
	now := m.now().UTC()
	if observed != nil {
		model := engineModel{ID: observed.ID, Kind: string(observed.Kind), Version: observed.Version, VersionText: observed.VersionText, Port: observed.Port, Status: observed.Status, SocketPath: observed.SocketPath, SystemdUnit: observed.SystemdUnit, CreatedAt: now, UpdatedAt: now}
		_, err := m.database.NewInsert().Model(&model).On("CONFLICT (id) DO UPDATE").Set("kind = EXCLUDED.kind").Set("version = EXCLUDED.version").Set("version_text = EXCLUDED.version_text").Set("port = EXCLUDED.port").Set("status = EXCLUDED.status").Set("socket_path = EXCLUDED.socket_path").Set("systemd_unit = EXCLUDED.systemd_unit").Set("updated_at = EXCLUDED.updated_at").Exec(ctx)
		if err != nil {
			return nil, err
		}
	}
	return m.ListEngines(ctx)
}

func (m *Module) ListEngines(ctx context.Context) ([]Engine, error) {
	models := []engineModel{}
	if err := m.database.NewSelect().Model(&models).OrderExpr("kind ASC").Scan(ctx); err != nil {
		return nil, err
	}
	items := make([]Engine, 0, len(models))
	for _, model := range models {
		items = append(items, model.toEngine())
	}
	return items, nil
}

func (m *Module) activeEngine(ctx context.Context, id string) (engineModel, error) {
	model, err := m.getEngineModel(ctx, id)
	if err != nil || (model.Status != string(StatusActive) && model.Status != "online") {
		return engineModel{}, errors.New("MySQL-family engine must be online")
	}
	return model, nil
}

func (m *Module) getEngineModel(ctx context.Context, id string) (engineModel, error) {
	var model engineModel
	err := m.database.NewSelect().Model(&model).Where("id = ?", id).Scan(ctx)
	return model, err
}
