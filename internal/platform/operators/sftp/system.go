package sftp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// HostSystem is the real, privileged NodeSystem. It runs on the node agent as
// root, so it can chown the chroot root and rewrite /etc/shadow via chpasswd.
type HostSystem struct {
	command func(ctx context.Context, stdin, name string, args ...string) ([]byte, error)
}

func NewHostSystem() *HostSystem {
	return &HostSystem{command: runCommand}
}

func runCommand(ctx context.Context, stdin, name string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, name, args...)
	if stdin != "" {
		command.Stdin = strings.NewReader(stdin)
	}
	return command.CombinedOutput()
}

func (s *HostSystem) EnsureChrootRoot(_ context.Context, rootPath string) error {
	rootPath = filepath.Clean(rootPath)
	if filepath.Dir(rootPath) != siteRootBase {
		return fmt.Errorf("%w: refusing to adjust a path outside %s", ErrChrootRootUnsafe, siteRootBase)
	}
	info, err := os.Lstat(rootPath)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("%w: not a managed directory", ErrChrootRootUnsafe)
	}
	// sshd rejects a chroot the login user could rename, so the root must be
	// owned by root and carry no group/other write bit.
	if err := os.Chown(rootPath, 0, 0); err != nil {
		return err
	}
	return os.Chmod(rootPath, 0o755)
}

func (s *HostSystem) WriteDropIn(_ context.Context, path, content string) error {
	return atomicWrite(path, []byte(content), 0o600)
}

func (s *HostSystem) RemoveDropIn(_ context.Context, path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (s *HostSystem) ValidateSSHD(ctx context.Context) error {
	if output, err := s.command(ctx, "", "sshd", "-t"); err != nil {
		return commandError(output, err)
	}
	return nil
}

func (s *HostSystem) ReloadSSHD(ctx context.Context) error {
	if output, err := s.command(ctx, "", "systemctl", "reload", "ssh.service"); err != nil {
		return commandError(output, err)
	}
	return nil
}

// SetPassword feeds "user:password" to chpasswd on stdin so the secret never
// appears in the process argument list. Any error output is scrubbed of the
// password before it is surfaced.
func (s *HostSystem) SetPassword(ctx context.Context, user, password string) error {
	output, err := s.command(ctx, user+":"+password+"\n", "chpasswd")
	if err != nil {
		scrubbed := strings.ReplaceAll(string(output), password, "[redacted]")
		return commandError([]byte(scrubbed), err)
	}
	return nil
}

func (s *HostSystem) LockPassword(ctx context.Context, user string) error {
	if output, err := s.command(ctx, "", "passwd", "-l", user); err != nil {
		return commandError(output, err)
	}
	return nil
}
