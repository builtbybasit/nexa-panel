package databases

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/nexa-panel/nexa-panel/internal/platform/jobs"
)

// submitExecute queues the single job that realizes one change: resolve the
// desired state, obtain an agent-signed plan, and execute it immediately. The
// signed plan still exists — the agent will not act without one — but it is no
// longer parked for human review; a click is a decision.
func (m *Module) submitExecute(ctx context.Context, eng *engine, resourceType, resourceID, action string, actor *string) (jobs.Job, error) {
	job, err := m.jobs.SubmitTitled(ctx, eng.spec.JobKind+".execute", jobTitle(eng, resourceType, action), map[string]string{"resourceType": resourceType, "resourceId": resourceID, "action": action}, actor)
	if err != nil {
		return jobs.Job{}, err
	}
	_, err = eng.store.AttachJob(ctx, resourceType, resourceID, job.ID, busyStatusFor(action))
	return job, err
}

func jobTitle(eng *engine, resourceType, action string) string {
	verb := "Update"
	switch {
	case strings.HasSuffix(action, ".create"), action == ActionProvisionServer, action == ActionApplyGrant:
		verb = "Create"
	case strings.HasSuffix(action, ".drop"), action == ActionRevokeGrant:
		verb = "Remove"
	case action == ActionRotateUser:
		verb = "Update"
	case action == ActionRestoreBackup:
		verb = "Restore"
	}
	return verb + " " + eng.spec.DisplayName + " " + resourceType
}

// busyStatusFor names the state a resource sits in while its job runs.
func busyStatusFor(action string) Status {
	switch action {
	case ActionCreateBackup:
		return StatusBackingUp
	case ActionRestoreBackup:
		return StatusRestoring
	case ActionDropDatabase, ActionDropUser, ActionRevokeGrant:
		return StatusDeleting
	default:
		return StatusApplying
	}
}

// executeJobFor binds the shared execute job to one engine: resolve the
// change, have the agent sign a plan for it, run it, and persist what the
// agent verifiably observed.
func (m *Module) executeJobFor(eng *engine) func(context.Context, json.RawMessage, func(int, string) error) (any, error) {
	return func(ctx context.Context, raw json.RawMessage, report func(int, string) error) (any, error) {
		var request struct {
			ResourceType string `json:"resourceType"`
			ResourceID   string `json:"resourceId"`
			Action       string `json:"action"`
		}
		if json.Unmarshal(raw, &request) != nil || request.ResourceID == "" {
			return nil, errors.New("invalid database change request")
		}
		if err := report(15, "Loading managed database desired state."); err != nil {
			return nil, err
		}
		change, err := m.changeFor(ctx, eng, request.ResourceType, request.ResourceID, request.Action)
		if err != nil {
			m.failResource(context.WithoutCancel(ctx), eng, request.ResourceType, request.ResourceID, err)
			return nil, err
		}
		if err := report(35, "Requesting an agent-signed "+eng.spec.DisplayName+" plan."); err != nil {
			return nil, err
		}
		plan, err := eng.adapter.PlanChange(ctx, change)
		if err != nil {
			m.failResource(context.WithoutCancel(ctx), eng, request.ResourceType, request.ResourceID, err)
			return nil, err
		}
		if len(plan.Steps) > 0 {
			if err := report(50, strings.Join(plan.Steps, " → ")); err != nil {
				return nil, err
			}
		}
		var secret string
		if change.Action == ActionCreateUser || change.Action == ActionRotateUser {
			user, getErr := eng.store.GetUser(ctx, request.ResourceID)
			if getErr != nil || user.PendingCredentialCiphertext == nil {
				return nil, errors.New("pending database credential is unavailable")
			}
			plaintext, decryptErr := m.cipher.Decrypt(eng.spec.CredentialLabelPrefix+user.ID, *user.PendingCredentialCiphertext)
			if decryptErr != nil {
				return nil, decryptErr
			}
			secret = string(plaintext)
			clear(plaintext)
			defer func() { secret = "" }()
		}
		observation, err := eng.adapter.ApplyPlan(ctx, plan.Raw, secret)
		secret = ""
		if err != nil {
			m.failResource(context.WithoutCancel(ctx), eng, request.ResourceType, request.ResourceID, err)
			return nil, err
		}
		if err := report(85, "Persisting verified "+eng.spec.DisplayName+" observed state."); err != nil {
			return nil, err
		}
		if err := m.commitObservation(ctx, eng, request.ResourceType, request.ResourceID, change, observation); err != nil {
			return nil, err
		}
		return observation.Raw, nil
	}
}

// changeFor resolves a resource plus an action into the neutral change the
// engine adapter will translate for its operator.
func (m *Module) changeFor(ctx context.Context, eng *engine, resourceType, resourceID, action string) (Change, error) {
	switch resourceType {
	case resourceServer:
		if !eng.spec.Provisionable {
			return Change{}, errors.New(eng.spec.DisplayName + " servers are discovered, not changed through the panel")
		}
		server, err := eng.adapter.GetServer(ctx, resourceID)
		if err != nil {
			return Change{}, err
		}
		return Change{Action: action, ServerID: server.ID, Provision: &ProvisionSpec{Version: server.Version, Cluster: server.Cluster, Port: server.Port}}, nil
	case resourceUser:
		user, err := eng.store.GetUser(ctx, resourceID)
		if err != nil {
			return Change{}, err
		}
		change := Change{Action: action, ServerID: user.ServerID, User: user.Name, UserHost: user.Host}
		if action == ActionCreateUser || action == ActionRotateUser {
			if user.PendingSecretDigest == nil {
				return Change{}, errors.New("pending user credential is unavailable")
			}
			change.SecretSHA256 = *user.PendingSecretDigest
		}
		if action == ActionDropUser {
			userDatabases, err := m.userDatabasesFor(ctx, eng, user.ID)
			if err != nil {
				return Change{}, err
			}
			change.UserDatabases = userDatabases
		}
		return change, nil
	case resourceDatabase:
		database, err := eng.store.GetDatabase(ctx, resourceID)
		if err != nil {
			return Change{}, err
		}
		change := Change{Action: action, ServerID: database.ServerID, Database: database.Name}
		// Dropping needs only the database name; the owner may already be gone
		// and the engine's drop statement does not use it.
		if action != ActionDropDatabase {
			owner, err := eng.store.GetUser(ctx, database.OwnerUserID)
			if err != nil {
				return Change{}, err
			}
			change.Owner, change.OwnerHost = owner.Name, owner.Host
		}
		return change, nil
	case resourceGrant:
		grant, err := eng.store.GetGrant(ctx, resourceID)
		if err != nil {
			return Change{}, err
		}
		database, err := eng.store.GetDatabase(ctx, grant.DatabaseID)
		if err != nil {
			return Change{}, err
		}
		owner, err := eng.store.GetUser(ctx, database.OwnerUserID)
		if err != nil {
			return Change{}, err
		}
		user, err := eng.store.GetUser(ctx, grant.UserID)
		if err != nil {
			return Change{}, err
		}
		return Change{Action: action, ServerID: database.ServerID, Database: database.Name, Owner: owner.Name, OwnerHost: owner.Host, User: user.Name, UserHost: user.Host, Access: grant.Access}, nil
	case resourceRestorePoint:
		point, err := eng.store.GetRestorePoint(ctx, resourceID)
		if err != nil {
			return Change{}, err
		}
		database, err := eng.store.GetDatabase(ctx, point.DatabaseID)
		if err != nil {
			return Change{}, err
		}
		owner, err := eng.store.GetUser(ctx, database.OwnerUserID)
		if err != nil {
			return Change{}, err
		}
		change := Change{Action: action, ServerID: database.ServerID, Database: database.Name, Owner: owner.Name, OwnerHost: owner.Host, BackupID: point.ID, BackupPath: point.Path}
		if action == ActionRestoreBackup {
			if point.SHA256 == nil {
				return Change{}, errors.New("restore point checksum is unavailable")
			}
			change.BackupSHA256, change.RestoreToken = *point.SHA256, randomToken(12)
		}
		return change, nil
	default:
		return Change{}, errors.New("database resource type is unsupported")
	}
}

func (m *Module) commitObservation(ctx context.Context, eng *engine, resourceType, resourceID string, change Change, observation Observation) error {
	if isDropOperation(change.Action) {
		return m.commitDrop(ctx, eng, resourceType, resourceID, change)
	}
	switch resourceType {
	case resourceServer:
		return eng.adapter.CommitServer(ctx, resourceID, observation)
	case resourceUser:
		return eng.store.SwapUserCredential(ctx, resourceID)
	case resourceDatabase, resourceGrant:
		_, err := eng.store.SetResourceStatus(ctx, resourceType, resourceID, StatusActive, nil)
		return err
	case resourceRestorePoint:
		if change.Action == ActionCreateBackup {
			if observation.Backup == nil || !observation.Backup.Verified {
				return errors.New("database backup verification is missing")
			}
			return eng.store.MarkRestorePointVerified(ctx, resourceID, observation.Backup.SHA256, observation.Backup.SizeBytes, observation.Backup.CreatedAt)
		}
		point, err := eng.store.GetRestorePoint(ctx, resourceID)
		if err != nil {
			return err
		}
		if err := eng.store.SetDatabaseActive(ctx, point.DatabaseID); err != nil {
			return err
		}
		_, err = eng.store.SetResourceStatus(ctx, resourceRestorePoint, resourceID, StatusVerified, nil)
		return err
	default:
		return errors.New("database observation resource is unsupported")
	}
}
