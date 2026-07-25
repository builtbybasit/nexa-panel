package deploy

import (
	"context"
	"errors"
	"fmt"
)

// SSHNodeSystem is the privileged surface ApplySSHAccess drives. SSHHostSystem
// is the real implementation; tests substitute a fake.
type SSHNodeSystem interface {
	// SFTPDropInExists reports whether the sftp operator already owns a jail for
	// this site. Both drop-ins present would leave the jail unenforced, so this
	// is the node-side half of the mutual exclusion the control panel declares.
	SFTPDropInExists(ctx context.Context, slug string) (bool, error)
	// EnsureHomeSSHDir creates {root}/.ssh owned by the site account. Root can
	// create a child of the root-owned site root; the account still cannot
	// create or rename entries in the root itself, so the chroot invariant the
	// sftp operator depends on is untouched.
	EnsureHomeSSHDir(ctx context.Context, rootPath, unixUser string) error
	WriteAuthorizedKeys(ctx context.Context, path, content string) error
	RemoveAuthorizedKeys(ctx context.Context, path string) error
	WriteDropIn(ctx context.Context, path, content string) error
	RemoveDropIn(ctx context.Context, path string) error
	// SetShell flips the account between an interactive shell and nologin. The
	// account is created with nologin at activation and nothing else ever
	// changes it, so this is the only writer.
	SetShell(ctx context.Context, user, shell string) error
	// ValidateSSHD runs `sshd -t` so a bad drop-in is caught before a reload
	// can lock every SSH login out of the node.
	ValidateSSHD(ctx context.Context) error
	ReloadSSHD(ctx context.Context) error
	// GenerateKeyPair mints an ed25519 pair in a temporary directory and returns
	// both halves without keeping a copy.
	GenerateKeyPair(ctx context.Context, comment string) (GeneratedKey, error)
}

type SSHHostOperator struct {
	system SSHNodeSystem
}

func NewSSHHostOperator(system SSHNodeSystem) (*SSHHostOperator, error) {
	if system == nil {
		system = NewSSHHostSystem()
	}
	return &SSHHostOperator{system: system}, nil
}

func (o *SSHHostOperator) ApplySSHAccess(ctx context.Context, request SSHAccessRequest) (SSHAccessObservation, error) {
	if err := request.validate(); err != nil {
		return SSHAccessObservation{}, err
	}
	if request.Enabled {
		return o.enable(ctx, request)
	}
	return o.disable(ctx, request)
}

func (o *SSHHostOperator) enable(ctx context.Context, request SSHAccessRequest) (SSHAccessObservation, error) {
	dropIn := request.dropInPath()
	keysPath := request.authorizedKeysPath()
	jailed, err := o.system.SFTPDropInExists(ctx, request.Slug)
	if err != nil {
		return SSHAccessObservation{}, fmt.Errorf("inspect SFTP configuration: %w", err)
	}
	if jailed {
		return SSHAccessObservation{}, ErrSFTPJailPresent
	}
	if err := o.system.EnsureHomeSSHDir(ctx, request.RootPath, request.UnixUser); err != nil {
		return SSHAccessObservation{}, fmt.Errorf("prepare site SSH directory: %w", err)
	}
	if err := o.system.WriteAuthorizedKeys(ctx, keysPath, renderAuthorizedKeys(request.Keys)); err != nil {
		return SSHAccessObservation{}, fmt.Errorf("write authorized keys: %w", err)
	}
	if err := o.system.WriteDropIn(ctx, dropIn, renderSSHDropIn(request)); err != nil {
		return SSHAccessObservation{}, fmt.Errorf("write SSH access configuration: %w", err)
	}
	// A drop-in that fails `sshd -t` would break every future SSH login on the
	// next reload. Validate first, and pull both files back out if it is bad.
	if err := o.system.ValidateSSHD(ctx); err != nil {
		o.rollback(ctx, dropIn, keysPath)
		return SSHAccessObservation{}, fmt.Errorf("validate SSH configuration: %w", err)
	}
	if err := o.system.ReloadSSHD(ctx); err != nil {
		o.rollback(ctx, dropIn, keysPath)
		return SSHAccessObservation{}, fmt.Errorf("reload SSH: %w", err)
	}
	// The shell flip comes last: until sshd is serving the Match block an
	// interactive shell on the account buys an attacker nothing, and if any step
	// above failed the account is still nologin.
	if err := o.system.SetShell(ctx, request.UnixUser, loginShell); err != nil {
		return SSHAccessObservation{}, fmt.Errorf("set site account shell: %w", err)
	}
	return SSHAccessObservation{
		Slug:               request.Slug,
		Enabled:            true,
		Username:           request.UnixUser,
		Shell:              loginShell,
		DropInPath:         dropIn,
		AuthorizedKeysPath: keysPath,
		KeyCount:           len(request.Keys),
	}, nil
}

func (o *SSHHostOperator) disable(ctx context.Context, request SSHAccessRequest) (SSHAccessObservation, error) {
	dropIn := request.dropInPath()
	keysPath := request.authorizedKeysPath()
	// Shell first: it closes new logins even if the reload below fails.
	if err := o.system.SetShell(ctx, request.UnixUser, nologinShell); err != nil {
		return SSHAccessObservation{}, fmt.Errorf("set site account shell: %w", err)
	}
	if err := o.system.RemoveDropIn(ctx, dropIn); err != nil {
		return SSHAccessObservation{}, fmt.Errorf("remove SSH access configuration: %w", err)
	}
	if err := o.system.RemoveAuthorizedKeys(ctx, keysPath); err != nil {
		return SSHAccessObservation{}, fmt.Errorf("remove authorized keys: %w", err)
	}
	if err := o.system.ValidateSSHD(ctx); err != nil {
		return SSHAccessObservation{}, fmt.Errorf("validate SSH configuration: %w", err)
	}
	if err := o.system.ReloadSSHD(ctx); err != nil {
		return SSHAccessObservation{}, fmt.Errorf("reload SSH: %w", err)
	}
	// {root}/.ssh and its contents stay: the deploy key lives there, and turning
	// SSH access off must not destroy the credential a repository trusts.
	return SSHAccessObservation{
		Slug:               request.Slug,
		Enabled:            false,
		Username:           request.UnixUser,
		Shell:              nologinShell,
		DropInPath:         dropIn,
		AuthorizedKeysPath: keysPath,
	}, nil
}

// rollback undoes a half-applied enable. It runs on a cancellation-free context
// because the caller's deadline is usually what got us here, and it re-runs
// `sshd -t` so the node is left in a state a later reload will accept.
func (o *SSHHostOperator) rollback(ctx context.Context, dropIn, keysPath string) {
	ctx = context.WithoutCancel(ctx)
	_ = o.system.RemoveDropIn(ctx, dropIn)
	_ = o.system.RemoveAuthorizedKeys(ctx, keysPath)
	_ = o.system.ValidateSSHD(ctx)
}

// ErrSFTPJailPresent is returned when a site still has the sftp operator's
// drop-in installed. It is exported so callers can surface actionable guidance:
// the two drop-ins cannot coexist, and SFTP must be disabled first.
var ErrSFTPJailPresent = errors.New("per-site SFTP is enabled for this site")
