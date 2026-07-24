package main

import (
	"context"

	databasesmodule "github.com/nexa-panel/nexa-panel/internal/modules/databases"
	"github.com/nexa-panel/nexa-panel/internal/modules/sites"
	backupoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/backups"
)

// These composition-root adapters keep the backups module dependent on narrow
// capabilities rather than concrete site and database modules.
type siteBackupResolver struct{ sites *sites.Module }

func (r siteBackupResolver) BackupSite(ctx context.Context, id string) (backupoperator.SiteTarget, error) {
	site, err := r.sites.Get(ctx, id)
	if err != nil {
		return backupoperator.SiteTarget{}, err
	}
	return backupoperator.SiteTarget{Slug: site.Slug, RootPath: site.RootPath, UnixUser: site.UnixUser}, nil
}

// databaseBackupResolver serves both engine slots of the backups module: the
// unified databases module resolves any managed database by ID and reports
// which engine dialect the dump must speak.
type databaseBackupResolver struct{ databases *databasesmodule.Module }

func (r databaseBackupResolver) BackupDatabase(ctx context.Context, id string) (backupoperator.DatabaseTarget, error) {
	target, err := r.databases.BackupTarget(ctx, id)
	if err != nil {
		return backupoperator.DatabaseTarget{}, err
	}
	return backupoperator.DatabaseTarget{Engine: target.Engine, Name: target.Name, Version: target.Version, Port: target.Port, Socket: target.Socket}, nil
}
