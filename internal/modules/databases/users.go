package databases

import (
	"context"
	"errors"
	"sort"
	"strings"

	"github.com/nexa-panel/nexa-panel/internal/platform/audit"
	"github.com/nexa-panel/nexa-panel/internal/platform/jobs"
)

// ListUsers returns managed users, optionally scoped to one server, merged
// across engines when unscoped.
func (m *Module) ListUsers(ctx context.Context, serverID string) ([]User, error) {
	if serverID != "" {
		eng, err := m.engineForServer(ctx, serverID)
		if err != nil {
			return nil, err
		}
		return m.listEngineUsers(ctx, eng, serverID)
	}
	items := []User{}
	for _, eng := range m.engines {
		users, err := m.listEngineUsers(ctx, eng, "")
		if err != nil {
			return nil, err
		}
		items = append(items, users...)
	}
	sortByName(items, func(item User) string { return item.Name })
	return items, nil
}

func (m *Module) listEngineUsers(ctx context.Context, eng *engine, serverID string) ([]User, error) {
	rows, err := eng.store.ListUsers(ctx, serverID)
	if err != nil {
		return nil, err
	}
	items := make([]User, 0, len(rows))
	for _, row := range rows {
		items = append(items, row.toUser(eng.spec.Engine))
	}
	return items, nil
}

func (m *Module) CreateUser(ctx context.Context, request CreateUserRequest, actor *string) (User, jobs.Job, error) {
	request.ServerID = strings.TrimSpace(request.ServerID)
	request.Name = strings.ToLower(strings.TrimSpace(request.Name))
	request.Host = strings.TrimSpace(request.Host)
	eng, err := m.engineForServer(ctx, request.ServerID)
	if err != nil {
		return User{}, jobs.Job{}, err
	}
	if !resourceNamePattern.MatchString(request.Name) {
		return User{}, jobs.Job{}, errors.New("user name must contain 2-63 lowercase letters, numbers, or underscores")
	}
	if eng.spec.UserScopedByHost {
		if request.Host == "" {
			request.Host = "localhost"
		}
	} else if request.Host != "" {
		return User{}, jobs.Job{}, errors.New("host scopes are only supported for MySQL-family users")
	}
	if _, err := m.activeServer(ctx, eng, request.ServerID); err != nil {
		return User{}, jobs.Job{}, err
	}
	id := randomResourceID("user")
	ciphertext, digest, err := m.sealPassword(eng, id, request.Password)
	if err != nil {
		return User{}, jobs.Job{}, err
	}
	now := m.now().UTC()
	row := userRow{ID: id, ServerID: request.ServerID, Name: request.Name, Host: request.Host, Status: string(StatusApplying), PendingCredentialCiphertext: &ciphertext, PendingSecretDigest: &digest, CreatedAt: now, UpdatedAt: now}
	if err := eng.store.InsertUser(ctx, row); err != nil {
		return User{}, jobs.Job{}, friendlyUnique(err, "user name is already managed on this server")
	}
	job, err := m.submitExecute(ctx, eng, resourceUser, id, ActionCreateUser, actor)
	if err != nil {
		m.failResource(ctx, eng, resourceUser, id, err)
		return row.toUser(eng.spec.Engine), jobs.Job{}, err
	}
	row.LastJobID = &job.ID
	m.jobs.Audit().RecordBestEffort(ctx, audit.Entry{
		ActorUserID: actor, Action: "databases.user_created", Subject: "database-user:" + id,
		Metadata: map[string]any{"engine": eng.spec.Engine, "name": row.Name, "host": row.Host, "serverId": row.ServerID},
	})
	return row.toUser(eng.spec.Engine), job, nil
}

// SetPassword stages a client-chosen password for an active user and applies
// it to the engine in one job.
func (m *Module) SetPassword(ctx context.Context, id, password string, actor *string) (User, jobs.Job, error) {
	eng, err := m.engineForResource(ctx, resourceUser, id)
	if err != nil {
		return User{}, jobs.Job{}, err
	}
	row, err := eng.store.GetUser(ctx, id)
	if err != nil {
		return User{}, jobs.Job{}, err
	}
	if Status(row.Status) != StatusActive && Status(row.Status) != StatusFailed {
		return User{}, jobs.Job{}, errors.New("the user is busy; wait for the current operation to finish")
	}
	ciphertext, digest, err := m.sealPassword(eng, id, password)
	if err != nil {
		return User{}, jobs.Job{}, err
	}
	if err := eng.store.SetUserPendingCredential(ctx, id, ciphertext, digest); err != nil {
		return User{}, jobs.Job{}, err
	}
	job, err := m.submitExecute(ctx, eng, resourceUser, id, ActionRotateUser, actor)
	if err != nil {
		m.failResource(ctx, eng, resourceUser, id, err)
		return row.toUser(eng.spec.Engine), jobs.Job{}, err
	}
	m.jobs.Audit().RecordBestEffort(ctx, audit.Entry{
		ActorUserID: actor, Action: "databases.user_password_set", Subject: "database-user:" + id,
		Metadata: map[string]any{"engine": eng.spec.Engine, "name": row.Name, "serverId": row.ServerID},
	})
	row.LastJobID, row.Status, row.UpdatedAt = &job.ID, string(StatusApplying), m.now().UTC()
	return row.toUser(eng.spec.Engine), job, nil
}

// sortByName orders merged cross-engine listings deterministically.
func sortByName[T any](items []T, name func(T) string) {
	sort.SliceStable(items, func(i, j int) bool { return name(items[i]) < name(items[j]) })
}
