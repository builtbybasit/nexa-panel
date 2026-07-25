package deploy

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/modules/sites"
	"github.com/nexa-panel/nexa-panel/internal/platform/audit"
	deployoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/deploy"

	"github.com/uptrace/bun"
)

// GeneratedKey is the result of in-panel key generation. PrivateKey is in this
// value once and is never persisted — the same contract as a generated SFTP
// password — so a caller that loses it generates another key instead.
type GeneratedKey struct {
	Key        SSHKey    `json:"key"`
	PrivateKey string    `json:"privateKey"`
	Access     SSHAccess `json:"access"`
}

// enable switches a site's own account into an interactive, key-only login.
// The SFTP check comes first because the two features write competing sshd
// Match blocks for one account; the operator re-checks it on the node, which
// closes the race against an SFTP enable that lands in between.
func (m *Module) enable(ctx context.Context, site sites.Site, actor *string, remoteAddress string) (SSHAccess, error) {
	jailed, err := m.sftp.AccessEnabled(ctx, site.ID)
	if err != nil {
		return SSHAccess{}, err
	}
	if jailed {
		return SSHAccess{}, refuse(http.StatusConflict, "sftp_access_enabled", "Disable SFTP for this site first. One account cannot have both an SFTP jail and an SSH login.")
	}
	if err := m.record(ctx, "deploy.ssh_enabled", site, actor, remoteAddress, nil); err != nil {
		return SSHAccess{}, err
	}
	return m.setEnabled(ctx, site, true)
}

func (m *Module) disable(ctx context.Context, site sites.Site, actor *string, remoteAddress string) (SSHAccess, error) {
	if err := m.record(ctx, "deploy.ssh_disabled", site, actor, remoteAddress, nil); err != nil {
		return SSHAccess{}, err
	}
	return m.setEnabled(ctx, site, false)
}

// addKey installs a pasted public key. The key is parsed and fingerprinted here
// so a malformed paste is refused before it is stored, let alone written to the
// node's authorized-keys file.
func (m *Module) addKey(ctx context.Context, site sites.Site, label, publicKey string, actor *string, remoteAddress string) (SSHAccess, error) {
	row, err := newKeyRow(site.ID, label, publicKey, m.now().UTC())
	if err != nil {
		return SSHAccess{}, refuse(http.StatusBadRequest, "invalid_public_key", err.Error())
	}
	if err := m.record(ctx, "deploy.ssh_key_added", site, actor, remoteAddress, keyMetadata(row)); err != nil {
		return SSHAccess{}, err
	}
	return m.applyKeys(ctx, site, func(ctx context.Context, tx bun.Tx) error { return insertKey(ctx, tx, row) })
}

func (m *Module) removeKey(ctx context.Context, site sites.Site, keyID string, actor *string, remoteAddress string) (SSHAccess, error) {
	row := new(sshKeyModel)
	err := m.database.NewSelect().Model(row).Where("id = ? AND site_id = ?", keyID, site.ID).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return SSHAccess{}, refuse(http.StatusNotFound, "ssh_key_not_found", "That key is not installed for this site.")
	}
	if err != nil {
		return SSHAccess{}, err
	}
	if err := m.record(ctx, "deploy.ssh_key_removed", site, actor, remoteAddress, keyMetadata(row)); err != nil {
		return SSHAccess{}, err
	}
	return m.applyKeys(ctx, site, func(ctx context.Context, tx bun.Tx) error {
		_, err := tx.NewDelete().Model((*sshKeyModel)(nil)).Where("id = ? AND site_id = ?", keyID, site.ID).Exec(ctx)
		return err
	})
}

// generateKey mints a pair on the node and installs its public half. The audit
// entry is written before the node is asked, so the intent is recorded even if
// generation fails; the fingerprint is unknown at that point and is therefore
// not part of it.
func (m *Module) generateKey(ctx context.Context, site sites.Site, label string, actor *string, remoteAddress string) (GeneratedKey, error) {
	label, err := normalizeLabel(label)
	if err != nil {
		return GeneratedKey{}, refuse(http.StatusBadRequest, "invalid_public_key", err.Error())
	}
	if err := m.record(ctx, "deploy.ssh_key_generated", site, actor, remoteAddress, map[string]any{"label": label}); err != nil {
		return GeneratedKey{}, err
	}
	generated, err := m.operator.GenerateUserKey(ctx, deployoperator.SSHAccessRequest{Slug: site.Slug, UnixUser: site.UnixUser, RootPath: site.RootPath})
	if err != nil {
		return GeneratedKey{}, operatorRefusal(err)
	}
	row, err := newKeyRow(site.ID, label, generated.PublicKey, m.now().UTC())
	if err != nil {
		return GeneratedKey{}, err
	}
	access, err := m.applyKeys(ctx, site, func(ctx context.Context, tx bun.Tx) error { return insertKey(ctx, tx, row) })
	if err != nil {
		return GeneratedKey{}, err
	}
	return GeneratedKey{Key: presentKeys([]sshKeyModel{*row})[0], PrivateKey: generated.PrivateKey, Access: access}, nil
}

// nodeIntent is the state the node must be driven to once a mutation commits.
// drive is a separate flag from enabled because a key-list change on a site
// whose access is off has nothing to install: disabling removed the drop-in and
// the key file from the node, so there is no live file left to rewrite.
type nodeIntent struct {
	enabled bool
	drive   bool
}

// setEnabled persists the new state and drives the node to match it — in both
// directions. A disable is a node operation, not a flag flip: it is the only
// thing that removes the sshd drop-in and the authorized-keys file and puts the
// account back on a nologin shell.
func (m *Module) setEnabled(ctx context.Context, site sites.Site, enabled bool) (SSHAccess, error) {
	return m.transact(ctx, site, func(ctx context.Context, tx bun.Tx) (nodeIntent, error) {
		return nodeIntent{enabled: enabled, drive: true}, m.upsert(ctx, tx, site, enabled)
	})
}

// applyKeys persists a key-list change and re-installs the list only when
// access is already on. A disabled site has no drop-in and no key file on the
// node — the disable removed both — so there is nothing to rewrite there.
func (m *Module) applyKeys(ctx context.Context, site sites.Site, mutate func(context.Context, bun.Tx) error) (SSHAccess, error) {
	return m.transact(ctx, site, func(ctx context.Context, tx bun.Tx) (nodeIntent, error) {
		if err := mutate(ctx, tx); err != nil {
			return nodeIntent{}, err
		}
		row := new(sshAccessModel)
		err := tx.NewSelect().Model(row).Where("site_id = ?", site.ID).Scan(ctx)
		if errors.Is(err, sql.ErrNoRows) {
			return nodeIntent{}, nil
		}
		if err != nil {
			return nodeIntent{}, err
		}
		return nodeIntent{enabled: row.Enabled, drive: row.Enabled}, nil
	})
}

// transact runs one state change: the caller's mutation decides the desired node
// state, the change is committed, and the node is then driven with the resulting
// key list.
//
// The node call deliberately runs outside the database transaction. The control
// database is a single SQLite connection, so an agent round trip — which writes
// /etc/ssh, runs `sshd -t` and reloads sshd, under a multi-minute client
// timeout — held inside a transaction would stall every other request in the
// panel. The property that made the in-transaction call attractive is kept by
// restoring the pre-change rows when the node refuses, so the control panel
// still never claims a state the node did not accept. The mutex serializes SSH
// changes with each other, which the write lock used to do implicitly.
func (m *Module) transact(ctx context.Context, site sites.Site, mutate func(context.Context, bun.Tx) (nodeIntent, error)) (SSHAccess, error) {
	m.sshChanges.Lock()
	defer m.sshChanges.Unlock()

	before, err := m.snapshot(ctx, site.ID)
	if err != nil {
		return SSHAccess{}, err
	}
	var (
		intent nodeIntent
		rows   []sshKeyModel
	)
	if err := m.database.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		desired, err := mutate(ctx, tx)
		if err != nil {
			return err
		}
		keys, err := m.keyRows(ctx, tx, site.ID)
		if err != nil {
			return err
		}
		intent, rows = desired, keys
		return nil
	}); err != nil {
		return SSHAccess{}, err
	}
	if intent.drive {
		if driveErr := m.drive(ctx, site, intent.enabled, rows); driveErr != nil {
			// Undo on a cancellation-free context: the caller's deadline is often
			// what got us here, and leaving the panel claiming a state the node
			// refused is worse than the extra work.
			if restoreErr := m.restore(context.WithoutCancel(ctx), site.ID, before); restoreErr != nil {
				return SSHAccess{}, errors.Join(operatorRefusal(driveErr), restoreErr)
			}
			return SSHAccess{}, operatorRefusal(driveErr)
		}
	}
	return m.currentAccess(ctx, m.database, site)
}

func newKeyRow(siteID, label, publicKey string, now time.Time) (*sshKeyModel, error) {
	label, err := normalizeLabel(label)
	if err != nil {
		return nil, err
	}
	algorithm, blob, comment, err := parseAuthorizedKey(publicKey)
	if err != nil {
		return nil, err
	}
	digest, err := fingerprint(blob)
	if err != nil {
		return nil, err
	}
	return &sshKeyModel{
		ID: randomKeyID(), SiteID: siteID, Label: label, Algorithm: algorithm,
		PublicKey: blob, Fingerprint: digest, Comment: comment, CreatedAt: now,
	}, nil
}

// insertKey refuses a duplicate explicitly rather than letting the unique index
// surface as an opaque failure: re-adding a key is a mistake worth naming.
func insertKey(ctx context.Context, tx bun.Tx, row *sshKeyModel) error {
	count, err := tx.NewSelect().Model((*sshKeyModel)(nil)).
		Where("site_id = ? AND fingerprint = ?", row.SiteID, row.Fingerprint).Count(ctx)
	if err != nil {
		return err
	}
	if count > 0 {
		return refuse(http.StatusConflict, "ssh_key_duplicate", "That key is already installed for this site.")
	}
	_, err = tx.NewInsert().Model(row).Exec(ctx)
	return err
}

// keyMetadata is what an audit entry may carry about a key: enough to identify
// it, never the body itself and never a private half.
func keyMetadata(row *sshKeyModel) map[string]any {
	return map[string]any{"label": row.Label, "algorithm": row.Algorithm, "fingerprint": row.Fingerprint}
}

func (m *Module) record(ctx context.Context, action string, site sites.Site, actor *string, remoteAddress string, metadata map[string]any) error {
	return m.jobs.Audit().RecordSensitive(ctx, audit.Entry{
		ActorUserID: actor, Action: action, Subject: "site:" + site.ID,
		RemoteAddress: remoteAddress, Metadata: metadata,
	})
}

func operatorRefusal(err error) error {
	if errors.Is(err, deployoperator.ErrSFTPJailPresent) {
		return refuse(http.StatusConflict, "sftp_access_enabled", "The node still has an SFTP jail configured for this site. Disable SFTP, then try again.")
	}
	return refuse(http.StatusConflict, "deploy_operation_failed", err.Error())
}
