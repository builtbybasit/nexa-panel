package databases

import (
	"context"
	"errors"
)

// failResource records a failure on the resource, with two softenings: a user
// that already holds a working credential falls back to active (abandoning the
// staged secret) rather than failed, and a restore point that has verified
// before keeps its verified badge with the failure attached.
func (m *Module) failResource(ctx context.Context, eng *engine, resourceType, id string, failure error) {
	message := failure.Error()
	if len(message) > 300 {
		message = message[:300]
	}
	if resourceType == resourceUser {
		row, err := eng.store.GetUser(ctx, id)
		if err == nil && row.CredentialCiphertext != nil {
			_ = eng.store.AbandonUserPendingCredential(ctx, id, message)
			return
		}
	}
	if resourceType == resourceRestorePoint {
		row, err := eng.store.GetRestorePoint(ctx, id)
		if err == nil && row.VerifiedAt != nil {
			_, _ = eng.store.SetResourceStatus(ctx, resourceType, id, StatusVerified, &message)
			return
		}
	}
	_, _ = eng.store.SetResourceStatus(ctx, resourceType, id, StatusFailed, &message)
}

// activeServer loads a server and requires it to be serving.
func (m *Module) activeServer(ctx context.Context, eng *engine, id string) (Server, error) {
	server, err := eng.adapter.GetServer(ctx, id)
	if err != nil || (server.Status != string(StatusActive) && server.Status != "online") {
		return Server{}, errors.New(eng.spec.DisplayName + " server must be online")
	}
	return server, nil
}
