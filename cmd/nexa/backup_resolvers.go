package main

import (
	"context"

	"github.com/nexa-panel/nexa-panel/internal/modules/mysql"
	"github.com/nexa-panel/nexa-panel/internal/modules/postgres"
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

type postgresBackupResolver struct{ postgres *postgres.Module }

func (r postgresBackupResolver) BackupDatabase(ctx context.Context, id string) (backupoperator.DatabaseTarget, error) {
	name, version, port, socket, err := r.postgres.BackupTarget(ctx, id)
	if err != nil {
		return backupoperator.DatabaseTarget{}, err
	}
	return backupoperator.DatabaseTarget{Engine: "postgres", Name: name, Version: version, Port: port, Socket: socket}, nil
}

type mysqlBackupResolver struct{ mysql *mysql.Module }

func (r mysqlBackupResolver) BackupDatabase(ctx context.Context, id string) (backupoperator.DatabaseTarget, error) {
	name, kind, socket, err := r.mysql.BackupTarget(ctx, id)
	if err != nil {
		return backupoperator.DatabaseTarget{}, err
	}
	return backupoperator.DatabaseTarget{Engine: kind, Name: name, Socket: socket}, nil
}
