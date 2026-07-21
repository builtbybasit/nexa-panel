package sftp

import (
	"context"
	"errors"
	"fmt"
)

// NodeSystem is the privileged surface Apply drives. HostSystem is the real
// implementation; tests substitute a fake.
type NodeSystem interface {
	// EnsureChrootRoot re-asserts the OpenSSH chroot invariant on a site root
	// (owned by root, not group/world writable). It is idempotent with what the
	// sites operator already does at activation.
	EnsureChrootRoot(ctx context.Context, rootPath string) error
	WriteDropIn(ctx context.Context, path, content string) error
	RemoveDropIn(ctx context.Context, path string) error
	// ValidateSSHD runs `sshd -t` so a bad drop-in is caught before a reload
	// can lock every SSH login out of the node.
	ValidateSSHD(ctx context.Context) error
	ReloadSSHD(ctx context.Context) error
	SetPassword(ctx context.Context, user, password string) error
	LockPassword(ctx context.Context, user string) error
}

type HostOperator struct {
	system NodeSystem
}

func NewHostOperator(system NodeSystem) (*HostOperator, error) {
	if system == nil {
		system = NewHostSystem()
	}
	return &HostOperator{system: system}, nil
}

func (o *HostOperator) Apply(ctx context.Context, request Request) (Observation, error) {
	if err := request.validate(); err != nil {
		return Observation{}, err
	}
	if request.Enabled {
		return o.enable(ctx, request)
	}
	return o.disable(ctx, request)
}

func (o *HostOperator) enable(ctx context.Context, request Request) (Observation, error) {
	path := request.dropInPath()
	if err := o.system.EnsureChrootRoot(ctx, request.RootPath); err != nil {
		return Observation{}, fmt.Errorf("prepare SFTP chroot root: %w", err)
	}
	if err := o.system.WriteDropIn(ctx, path, renderDropIn(request)); err != nil {
		return Observation{}, fmt.Errorf("write SFTP configuration: %w", err)
	}
	// A drop-in that fails `sshd -t` would break every future SSH login on the
	// next reload. Validate first, and pull the file back out if it is bad.
	if err := o.system.ValidateSSHD(ctx); err != nil {
		_ = o.system.RemoveDropIn(context.WithoutCancel(ctx), path)
		return Observation{}, fmt.Errorf("validate SSH configuration: %w", err)
	}
	if err := o.system.ReloadSSHD(ctx); err != nil {
		_ = o.system.RemoveDropIn(context.WithoutCancel(ctx), path)
		return Observation{}, fmt.Errorf("reload SSH: %w", err)
	}
	passwordSet := false
	if request.Password != "" {
		if err := o.system.SetPassword(ctx, request.UnixUser, request.Password); err != nil {
			return Observation{}, fmt.Errorf("set SFTP account password: %w", err)
		}
		passwordSet = true
	}
	return Observation{Slug: request.Slug, Enabled: true, Username: request.UnixUser, PasswordSet: passwordSet, DropInPath: path}, nil
}

func (o *HostOperator) disable(ctx context.Context, request Request) (Observation, error) {
	path := request.dropInPath()
	if err := o.system.RemoveDropIn(ctx, path); err != nil {
		return Observation{}, fmt.Errorf("remove SFTP configuration: %w", err)
	}
	if err := o.system.ValidateSSHD(ctx); err != nil {
		return Observation{}, fmt.Errorf("validate SSH configuration: %w", err)
	}
	if err := o.system.ReloadSSHD(ctx); err != nil {
		return Observation{}, fmt.Errorf("reload SSH: %w", err)
	}
	// Lock the password too: even with the Match block gone the account should
	// not be reachable by a stale credential should global password auth ever
	// be turned on.
	if err := o.system.LockPassword(ctx, request.UnixUser); err != nil {
		return Observation{}, fmt.Errorf("lock SFTP account: %w", err)
	}
	return Observation{Slug: request.Slug, Enabled: false, Username: request.UnixUser, DropInPath: path}, nil
}

// ErrChrootRootUnsafe is returned when a site root cannot serve as a chroot and
// cannot be repaired. It is exported so callers can surface actionable guidance.
var ErrChrootRootUnsafe = errors.New("site root is not a usable SFTP chroot")
