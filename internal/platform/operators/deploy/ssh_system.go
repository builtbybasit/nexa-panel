package deploy

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
)

// SSHHostSystem is the real, privileged SSHNodeSystem. It runs on the node agent
// as root, so it can write into /etc/ssh, own the generated key tree, and change
// a login shell with usermod.
type SSHHostSystem struct {
	command     func(ctx context.Context, name string, args ...string) ([]byte, error)
	lookupUser  func(string) (*user.User, error)
	lookupGroup func(string) (*user.Group, error)
}

func NewSSHHostSystem() *SSHHostSystem {
	return &SSHHostSystem{command: runCommand, lookupUser: user.Lookup, lookupGroup: user.LookupGroup}
}

func (s *SSHHostSystem) SFTPDropInExists(_ context.Context, slug string) (bool, error) {
	_, err := os.Lstat(filepath.Join(SSHDDropInDir, "nexa-site-"+slug+".conf"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// EnsureHomeSSHDir creates {root}/.ssh through an os.Root, so a symlink planted
// in the site root cannot redirect the chmod or the chown at a path outside it.
func (s *SSHHostSystem) EnsureHomeSSHDir(_ context.Context, rootPath, unixUser string) error {
	account, err := s.lookupUser(unixUser)
	if err != nil {
		return fmt.Errorf("look up site account: %w", err)
	}
	uid, err := strconv.Atoi(account.Uid)
	if err != nil || uid < 0 {
		return errors.New("site account returned an invalid UID")
	}
	gid, err := strconv.Atoi(account.Gid)
	if err != nil || gid < 0 {
		return errors.New("site account returned an invalid GID")
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return fmt.Errorf("open site root: %w", err)
	}
	defer root.Close()
	if err := root.Mkdir(".ssh", 0o700); err != nil && !os.IsExist(err) {
		return err
	}
	linkInfo, err := root.Lstat(".ssh")
	if err != nil {
		return err
	}
	if linkInfo.Mode()&os.ModeSymlink != 0 || !linkInfo.IsDir() {
		return errors.New(".ssh is not a managed directory")
	}
	directory, err := root.Open(".ssh")
	if err != nil {
		return err
	}
	defer directory.Close()
	openedInfo, err := directory.Stat()
	if err != nil {
		return err
	}
	if !openedInfo.IsDir() || !os.SameFile(linkInfo, openedInfo) {
		return errors.New(".ssh changed while it was being prepared")
	}
	if err := directory.Chmod(0o700); err != nil {
		return err
	}
	return directory.Chown(uid, gid)
}

// WriteAuthorizedKeys publishes the key list root-owned and world-readable.
// sshd reads it after dropping to the login user, so it must be readable; it
// must equally not be writable by that user, which is why it lives here and not
// in the site's home.
func (s *SSHHostSystem) WriteAuthorizedKeys(_ context.Context, path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	// MkdirAll applies the process umask, and the agent runs under a restrictive
	// one: 0755 &^ 0177 leaves a 0600 directory that nothing can traverse. The
	// leaf was already chmodded back, but sshd opens the file after dropping to
	// the login user and walks the whole chain, so every directory this call
	// creates has to be corrected — not just the last one. Without this the
	// login fails with "Could not open user authorized keys: Permission denied"
	// while every root-run test passes, because root bypasses the check.
	if err := os.Chmod(AuthorizedKeysDir, 0o711); err != nil {
		return err
	}
	if err := os.Chmod(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := atomicWrite(path, []byte(content), 0o644); err != nil {
		return err
	}
	return os.Chown(path, 0, 0)
}

func (s *SSHHostSystem) RemoveAuthorizedKeys(_ context.Context, path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (s *SSHHostSystem) WriteDropIn(_ context.Context, path, content string) error {
	return atomicWrite(path, []byte(content), 0o600)
}

func (s *SSHHostSystem) RemoveDropIn(_ context.Context, path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (s *SSHHostSystem) SetShell(ctx context.Context, unixUser, shell string) error {
	if output, err := s.command(ctx, "usermod", "-s", shell, unixUser); err != nil {
		return commandError(output, err)
	}
	return nil
}

func (s *SSHHostSystem) ValidateSSHD(ctx context.Context) error {
	if output, err := s.command(ctx, "sshd", "-t"); err != nil {
		return commandError(output, err)
	}
	return nil
}

func (s *SSHHostSystem) ReloadSSHD(ctx context.Context) error {
	if output, err := s.command(ctx, "systemctl", "reload", "ssh.service"); err != nil {
		return commandError(output, err)
	}
	return nil
}

func runCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}
