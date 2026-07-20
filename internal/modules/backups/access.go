package backups

import (
	"context"
	"database/sql"

	"github.com/uptrace/bun"

	"github.com/nexa-panel/nexa-panel/internal/platform/identity"
)

type backupAccess struct {
	unscoped bool
	granted  map[string]struct{}
}

func (m *Module) accessForUser(ctx context.Context, user identity.User) (backupAccess, error) {
	if user.Role != "developer" {
		return backupAccess{unscoped: true}, nil
	}
	all, siteIDs, err := m.accessPolicy.AccessibleSiteIDs(ctx, user)
	if err != nil {
		return backupAccess{}, err
	}
	if all {
		return backupAccess{unscoped: true}, nil
	}
	granted := make(map[string]struct{}, len(siteIDs))
	for _, siteID := range siteIDs {
		granted[siteID] = struct{}{}
	}
	return backupAccess{granted: granted}, nil
}

// planVisible keeps compound plans atomic: exposing a plan when only one of
// its sites is granted would reveal the other sites and their backup metadata.
// Database targets have no site ownership mapping, so plans containing any
// database also remain hidden from site-scoped developers.
func (access backupAccess) planVisible(model planModel) bool {
	if access.unscoped {
		return true
	}
	siteIDs := decodeStringSlice(model.SiteIDsJSON)
	if len(siteIDs) == 0 || len(decodeStringSlice(model.DatabaseIDsJSON)) != 0 {
		return false
	}
	for _, siteID := range siteIDs {
		if _, ok := access.granted[siteID]; !ok {
			return false
		}
	}
	return true
}

func (m *Module) listPlansForUser(ctx context.Context, user identity.User) ([]planModel, error) {
	access, err := m.accessForUser(ctx, user)
	if err != nil {
		return nil, err
	}
	models := make([]planModel, 0)
	if err := m.database.NewSelect().Model(&models).Order("name ASC").Scan(ctx); err != nil {
		return nil, err
	}
	visible := make([]planModel, 0, len(models))
	for _, model := range models {
		if access.planVisible(model) {
			visible = append(visible, model)
		}
	}
	return visible, nil
}

func (m *Module) getPlanForUser(ctx context.Context, user identity.User, planID string) (planModel, error) {
	var model planModel
	if err := m.database.NewSelect().Model(&model).Where("id = ?", planID).Scan(ctx); err != nil {
		return planModel{}, err
	}
	access, err := m.accessForUser(ctx, user)
	if err != nil {
		return planModel{}, err
	}
	if !access.planVisible(model) {
		return planModel{}, sql.ErrNoRows
	}
	return model, nil
}

func (m *Module) listAccountsForUser(ctx context.Context, user identity.User) ([]accountModel, error) {
	if user.Role != "developer" {
		models := make([]accountModel, 0)
		err := m.database.NewSelect().Model(&models).Order("name ASC").Scan(ctx)
		return models, err
	}
	plans, err := m.listPlansForUser(ctx, user)
	if err != nil {
		return nil, err
	}
	accountIDs := make(map[string]struct{}, len(plans))
	for _, plan := range plans {
		accountIDs[plan.AccountID] = struct{}{}
	}
	models := make([]accountModel, 0, len(accountIDs))
	if len(accountIDs) == 0 {
		return models, nil
	}
	ids := make([]string, 0, len(accountIDs))
	for id := range accountIDs {
		ids = append(ids, id)
	}
	if err := m.database.NewSelect().Model(&models).Where("id IN (?)", bun.List(ids)).Order("name ASC").Scan(ctx); err != nil {
		return nil, err
	}
	return models, nil
}

func (m *Module) getAccountForUser(ctx context.Context, user identity.User, accountID string) (*accountModel, error) {
	if user.Role != "developer" {
		return m.getAccountModel(ctx, accountID)
	}
	models, err := m.listAccountsForUser(ctx, user)
	if err != nil {
		return nil, err
	}
	for index := range models {
		if models[index].ID == accountID {
			return &models[index], nil
		}
	}
	return nil, sql.ErrNoRows
}
