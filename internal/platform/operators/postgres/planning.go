package postgres

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/platform/hostcmd"
)

func (o *HostOperator) Plan(ctx context.Context, change Change) (Plan, error) {
	instances, err := o.Discover(ctx)
	if err != nil {
		return Plan{}, err
	}
	change, observed, err := o.normalizeChange(ctx, change, instances)
	if err != nil {
		return Plan{}, err
	}
	fingerprint, err := fingerprint(observed)
	if err != nil {
		return Plan{}, err
	}
	steps, warnings, interruption := planDescription(change)
	now := o.now().UTC()
	return Plan{ID: randomID(), Kind: PlanKind, Change: change, Steps: steps, Warnings: warnings, ObservedFingerprint: fingerprint, Interruption: interruption, PlannedAt: now, ExpiresAt: now.Add(20 * time.Minute)}, nil
}

func (o *HostOperator) normalizeChange(ctx context.Context, change Change, instances []Instance) (Change, any, error) {
	change.Version = strings.TrimSpace(change.Version)
	change.Cluster = strings.ToLower(strings.TrimSpace(change.Cluster))
	change.Database = strings.ToLower(strings.TrimSpace(change.Database))
	change.OwnerRole = strings.ToLower(strings.TrimSpace(change.OwnerRole))
	change.Role = strings.ToLower(strings.TrimSpace(change.Role))
	for index := range change.RoleDatabases {
		change.RoleDatabases[index].Name = strings.ToLower(strings.TrimSpace(change.RoleDatabases[index].Name))
		change.RoleDatabases[index].NewOwner = strings.ToLower(strings.TrimSpace(change.RoleDatabases[index].NewOwner))
	}
	change.BackupID = strings.TrimSpace(change.BackupID)
	change.BackupPath = filepath.Clean(strings.TrimSpace(change.BackupPath))
	change.BackupSHA256 = strings.ToLower(strings.TrimSpace(change.BackupSHA256))
	change.SecretSHA256 = strings.ToLower(strings.TrimSpace(change.SecretSHA256))

	if change.Action == ActionProvision {
		if !supportedVersion(change.Version) || !clusterPattern.MatchString(change.Cluster) {
			return Change{}, nil, errors.New("PostgreSQL version 16, 17, or 18 and a valid cluster name are required")
		}
		if change.Port == 0 {
			change.Port = firstAvailablePort(instances)
		}
		if change.Port < 1024 || change.Port > 65535 {
			return Change{}, nil, errors.New("PostgreSQL port must be between 1024 and 65535")
		}
		change.InstanceID = instanceID(change.Version, change.Cluster)
		for _, item := range instances {
			if item.ID == change.InstanceID || item.Port == change.Port || item.DataPath == filepath.Join(o.dataRoot, change.Version, change.Cluster) {
				return Change{}, nil, errors.New("PostgreSQL instance port, identity, or data path is already in use")
			}
		}
		if output, err := o.runner.Run(ctx, Command{Name: binary(change.Version, "postgres"), Args: []string{"--version"}}); err != nil {
			return Change{}, nil, hostcmd.Error("verify PostgreSQL server package", output, err)
		}
		return change, instances, nil
	}

	var instance *Instance
	for index := range instances {
		if instances[index].ID == change.InstanceID {
			copy := instances[index]
			instance = &copy
			break
		}
	}
	if instance == nil {
		return Change{}, nil, errors.New("PostgreSQL instance is not installed")
	}
	if instance.Status != "online" {
		return Change{}, nil, errors.New("PostgreSQL instance is not online")
	}
	change.Version, change.Cluster, change.Port = instance.Version, instance.Cluster, instance.Port
	if err := validateResourceChange(change, o.backupRoot); err != nil {
		return Change{}, nil, err
	}
	return change, instance, nil
}

func validateResourceChange(change Change, backupRoot string) error {
	switch change.Action {
	case ActionCreateRole, ActionRotateRole:
		if !namePattern.MatchString(change.Role) || !hashPattern.MatchString(change.SecretSHA256) {
			return errors.New("role and credential digest are required")
		}
	case ActionDropRole:
		if !namePattern.MatchString(change.Role) {
			return errors.New("a valid role is required")
		}
		for _, item := range change.RoleDatabases {
			if !namePattern.MatchString(item.Name) || (item.NewOwner != "" && !namePattern.MatchString(item.NewOwner)) {
				return errors.New("role database references must be valid identifiers")
			}
		}
	case ActionCreateDatabase:
		if !namePattern.MatchString(change.Database) || !namePattern.MatchString(change.OwnerRole) {
			return errors.New("database and owner role are required")
		}
	case ActionDropDatabase:
		if !namePattern.MatchString(change.Database) {
			return errors.New("a valid database is required")
		}
	case ActionRevokeGrant:
		if !namePattern.MatchString(change.Database) || !namePattern.MatchString(change.OwnerRole) || !namePattern.MatchString(change.Role) {
			return errors.New("database, owner role, and grantee role are required")
		}
	case ActionApplyGrant:
		if !namePattern.MatchString(change.Database) || !namePattern.MatchString(change.OwnerRole) || !namePattern.MatchString(change.Role) {
			return errors.New("database, owner role, and grantee role are required")
		}
		if change.Access != AccessConnect && change.Access != AccessReadOnly && change.Access != AccessReadWrite {
			return errors.New("database access must be connect, read_only, or read_write")
		}
	case ActionCreateBackup:
		if !namePattern.MatchString(change.Database) || !namePattern.MatchString(change.OwnerRole) || change.BackupID == "" {
			return errors.New("database, owner role, and backup identity are required")
		}
		expected := filepath.Join(backupRoot, change.InstanceID, change.Database, change.BackupID+".dump")
		if change.BackupPath == "." {
			change.BackupPath = expected
		}
		if change.BackupPath != expected {
			return errors.New("backup path is outside the managed PostgreSQL backup root")
		}
	case ActionRestoreBackup:
		if !namePattern.MatchString(change.Database) || !namePattern.MatchString(change.OwnerRole) || change.BackupID == "" || !hashPattern.MatchString(change.BackupSHA256) || !tokenPattern.MatchString(change.RestoreToken) {
			return errors.New("database, owner, verified backup, and restore confirmation are required")
		}
		expected := filepath.Join(backupRoot, change.InstanceID, change.Database, change.BackupID+".dump")
		if change.BackupPath != expected {
			return errors.New("restore path is outside the managed PostgreSQL backup root")
		}
	default:
		return errors.New("PostgreSQL action is unsupported")
	}
	return nil
}

func planDescription(change Change) ([]string, []string, bool) {
	switch change.Action {
	case ActionProvision:
		return []string{fmt.Sprintf("Create PostgreSQL %s cluster %s on port %d.", change.Version, change.Cluster, change.Port), "Start the dedicated cluster and verify local readiness."}, []string{"Provisioning allocates a new data directory and systemd identity."}, false
	case ActionCreateRole:
		return []string{fmt.Sprintf("Create non-superuser login role %s.", change.Role), "Verify the role exists without returning its password."}, nil, false
	case ActionRotateRole:
		return []string{fmt.Sprintf("Rotate the credential for role %s without placing it in command arguments.", change.Role), "Verify the role can still log in."}, []string{"Existing clients must be updated with the new credential."}, false
	case ActionDropRole:
		steps := []string{}
		warnings := []string{fmt.Sprintf("Applications signing in as %s will lose access.", change.Role)}
		for _, item := range change.RoleDatabases {
			if item.NewOwner != "" {
				steps = append(steps, fmt.Sprintf("Transfer ownership of database %s to %s.", item.Name, item.NewOwner))
				warnings = append(warnings, fmt.Sprintf("Database %s is owned by %s; ownership moves to %s.", item.Name, change.Role, item.NewOwner))
			}
		}
		steps = append(steps, fmt.Sprintf("Reassign owned objects, revoke privileges, and drop role %s.", change.Role), "Verify the role no longer exists.")
		return steps, warnings, false
	case ActionCreateDatabase:
		return []string{fmt.Sprintf("Create database %s from template0 owned by %s.", change.Database, change.OwnerRole), "Verify ownership and connectivity."}, nil, false
	case ActionDropDatabase:
		return []string{fmt.Sprintf("Drop database %s and everything in it.", change.Database), "Verify the database no longer exists."}, []string{fmt.Sprintf("This permanently deletes database %s. Back it up first.", change.Database)}, true
	case ActionApplyGrant:
		return []string{fmt.Sprintf("Apply %s access on %s to %s.", change.Access, change.Database, change.Role), "Set matching privileges for existing and future public-schema objects."}, nil, false
	case ActionRevokeGrant:
		return []string{fmt.Sprintf("Revoke %s's access on %s.", change.Role, change.Database)}, []string{fmt.Sprintf("%s will lose the access this grant provided.", change.Role)}, false
	case ActionCreateBackup:
		return []string{fmt.Sprintf("Create a custom-format logical backup of %s.", change.Database), "Hash and verify the archive before recording a restore point."}, nil, false
	case ActionRestoreBackup:
		return []string{fmt.Sprintf("Restore verified backup %s into a temporary database.", change.BackupID), "Verify the restored database, atomically swap database names, and remove the replaced copy."}, []string{"Active connections will be terminated during the final database-name swap."}, true
	default:
		return nil, nil, false
	}
}

func normalizeStatus(value string) string {
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(value)), "online") {
		return "online"
	}
	return "down"
}
