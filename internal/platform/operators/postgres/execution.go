package postgres

import (
	"context"

	"crypto/sha256"

	"errors"

	"encoding/hex"
	"strconv"

	"strings"
)

func (o *HostOperator) Apply(ctx context.Context, execution Execution) (Observation, error) {
	plan := execution.Plan
	if plan.ID == "" || plan.Kind != PlanKind || o.now().UTC().After(plan.ExpiresAt) {
		return Observation{}, errors.New("PostgreSQL plan is invalid or expired")
	}
	if plan.Change.SecretSHA256 != "" {
		digest := sha256.Sum256([]byte(execution.Secret))
		if !strings.EqualFold(hex.EncodeToString(digest[:]), plan.Change.SecretSHA256) {
			return Observation{}, errors.New("credential does not match the approved PostgreSQL plan")
		}
	} else if execution.Secret != "" {
		return Observation{}, errors.New("unexpected credential for PostgreSQL operation")
	}
	instances, err := o.Discover(ctx)
	if err != nil {
		return Observation{}, err
	}
	change, observed, err := o.normalizeChange(ctx, plan.Change, instances)
	if err != nil {
		return Observation{}, err
	}
	current, _ := fingerprint(observed)
	if current != plan.ObservedFingerprint {
		return Observation{}, errors.New("PostgreSQL observed state changed after planning")
	}
	return o.execute(ctx, change, execution.Secret)
}

func (o *HostOperator) execute(ctx context.Context, change Change, secret string) (Observation, error) {
	switch change.Action {
	case ActionProvision:
		return o.provision(ctx, change)
	case ActionCreateRole:
		return o.createRole(ctx, change, secret)
	case ActionRotateRole:
		return o.rotateRole(ctx, change, secret)
	case ActionCreateDatabase:
		return o.createDatabase(ctx, change)
	case ActionApplyGrant:
		return o.applyGrant(ctx, change)
	case ActionCreateBackup:
		return o.createBackup(ctx, change)
	case ActionRestoreBackup:
		return o.restoreBackup(ctx, change)
	default:
		return Observation{}, errors.New("PostgreSQL action is unsupported")
	}
}

func (o *HostOperator) applyGrant(ctx context.Context, change Change) (Observation, error) {
	database, role, owner := quoteIdentifier(change.Database), quoteIdentifier(change.Role), quoteIdentifier(change.OwnerRole)
	statements := []string{
		"REVOKE ALL ON DATABASE " + database + " FROM " + role + ";",
		"GRANT CONNECT ON DATABASE " + database + " TO " + role + ";",
		"\\connect " + database,
		"REVOKE ALL ON SCHEMA public FROM " + role + ";",
		"REVOKE ALL PRIVILEGES ON ALL TABLES IN SCHEMA public FROM " + role + ";",
		"REVOKE ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA public FROM " + role + ";",
		"ALTER DEFAULT PRIVILEGES FOR ROLE " + owner + " IN SCHEMA public REVOKE ALL ON TABLES FROM " + role + ";",
		"ALTER DEFAULT PRIVILEGES FOR ROLE " + owner + " IN SCHEMA public REVOKE ALL ON SEQUENCES FROM " + role + ";",
	}
	if change.Access != AccessConnect {
		statements = append(statements, "GRANT USAGE ON SCHEMA public TO "+role+";")
		if change.Access == AccessReadOnly {
			statements = append(statements, "GRANT SELECT ON ALL TABLES IN SCHEMA public TO "+role+";", "GRANT SELECT ON ALL SEQUENCES IN SCHEMA public TO "+role+";", "ALTER DEFAULT PRIVILEGES FOR ROLE "+owner+" IN SCHEMA public GRANT SELECT ON TABLES TO "+role+";", "ALTER DEFAULT PRIVILEGES FOR ROLE "+owner+" IN SCHEMA public GRANT SELECT ON SEQUENCES TO "+role+";")
		} else {
			statements = append(statements, "GRANT SELECT, INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON ALL TABLES IN SCHEMA public TO "+role+";", "GRANT USAGE, SELECT, UPDATE ON ALL SEQUENCES IN SCHEMA public TO "+role+";", "ALTER DEFAULT PRIVILEGES FOR ROLE "+owner+" IN SCHEMA public GRANT SELECT, INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON TABLES TO "+role+";", "ALTER DEFAULT PRIVILEGES FOR ROLE "+owner+" IN SCHEMA public GRANT USAGE, SELECT, UPDATE ON SEQUENCES TO "+role+";")
		}
	}
	command := asPostgres(change.Version, "psql", "--no-psqlrc", "--host", o.socketRoot, "--port", strconv.Itoa(change.Port), "--username", "postgres", "--dbname", "postgres", "--set", "ON_ERROR_STOP=1")
	command.Stdin = strings.Join(statements, "\n") + "\n"
	if output, err := o.runner.Run(ctx, command); err != nil {
		return Observation{}, commandError("apply PostgreSQL grant", output, err)
	}
	return Observation{Action: change.Action, Database: change.Database, Role: change.Role, Access: string(change.Access), Verified: true}, nil
}
