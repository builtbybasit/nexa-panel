package sites

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func NewHostSystem() *HostSystem {
	dialer := &net.Dialer{Timeout: 3 * time.Second}
	return &HostSystem{
		command: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, name, args...).CombinedOutput()
		},
		lookupUser:  user.Lookup,
		lookupGroup: user.LookupGroup,
		client: &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, "tcp", "127.0.0.1:80")
		}}, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }, Timeout: 5 * time.Second},
		verifyTimeout:  15 * time.Second,
		verifyInterval: 250 * time.Millisecond,
	}
}

type HostSystem struct {
	command     func(context.Context, string, ...string) ([]byte, error)
	lookupUser  func(string) (*user.User, error)
	lookupGroup func(string) (*user.Group, error)
	client      *http.Client
	// verifyTimeout bounds how long VerifyHost waits for a reload to take effect.
	verifyTimeout  time.Duration
	verifyInterval time.Duration
}

func (s *HostSystem) PrepareSite(ctx context.Context, site Site) error {
	lookupUser := s.lookupUser
	if lookupUser == nil {
		lookupUser = user.Lookup
	}
	lookupGroup := s.lookupGroup
	if lookupGroup == nil {
		lookupGroup = user.LookupGroup
	}
	account, err := lookupUser(site.UnixUser)
	if err != nil {
		var unknown user.UnknownUserError
		if !errors.As(err, &unknown) {
			return fmt.Errorf("look up site account: %w", err)
		}
		if _, commandErr := s.command(ctx, "useradd", "--system", "--user-group", "--home-dir", site.RootPath, "--shell", "/usr/sbin/nologin", site.UnixUser); commandErr != nil {
			return commandErr
		}
		account, err = lookupUser(site.UnixUser)
	}
	if err != nil {
		return err
	}
	uid, err := strconv.Atoi(account.Uid)
	if err != nil || uid < 0 {
		return errors.New("site account returned an invalid UID")
	}
	group, err := lookupGroup("www-data")
	if err != nil {
		return err
	}
	gid, err := strconv.Atoi(group.Gid)
	if err != nil || gid < 0 {
		return errors.New("web server group returned an invalid GID")
	}
	parent, err := os.OpenRoot(filepath.Dir(site.RootPath))
	if err != nil {
		return fmt.Errorf("open site parent: %w", err)
	}
	defer parent.Close()
	// The site root is owned by root, not the site account, and is not
	// group- or world-writable. This is what lets it double as an OpenSSH
	// ChrootDirectory for optional per-site SFTP: sshd refuses to chroot into
	// any path a login user could rename. The writable tree lives one level
	// down in the subdirectories below, which the site account does own.
	if err := prepareOwnedDirectory(parent, filepath.Base(site.RootPath), 0o755, 0, 0); err != nil {
		return fmt.Errorf("prepare site root: %w", err)
	}
	root, err := parent.OpenRoot(filepath.Base(site.RootPath))
	if err != nil {
		return fmt.Errorf("open site root: %w", err)
	}
	defer root.Close()
	for _, directory := range []struct {
		name string
		mode os.FileMode
	}{
		{name: "public", mode: 0o750},
		{name: "logs", mode: 0o770},
		{name: "tmp", mode: 0o700},
		{name: "private", mode: 0o700},
		{name: "backups", mode: 0o700},
	} {
		if err := prepareOwnedDirectory(root, directory.name, directory.mode, uid, gid); err != nil {
			return fmt.Errorf("prepare site directory %s: %w", directory.name, err)
		}
	}
	return nil
}

// prepareOwnedDirectory operates through an os.Root and an opened directory
// descriptor. The identity comparison rejects symlink substitution before any
// chmod or chown can affect an attacker-chosen target.
func prepareOwnedDirectory(root *os.Root, name string, mode os.FileMode, uid, gid int) error {
	if err := root.Mkdir(name, mode); err != nil && !os.IsExist(err) {
		return err
	}
	linkInfo, err := root.Lstat(name)
	if err != nil {
		return err
	}
	if linkInfo.Mode()&os.ModeSymlink != 0 || !linkInfo.IsDir() {
		return fmt.Errorf("%s is not a managed directory", name)
	}
	directory, err := root.Open(name)
	if err != nil {
		return err
	}
	defer directory.Close()
	openedInfo, err := directory.Stat()
	if err != nil {
		return err
	}
	if !openedInfo.IsDir() || !os.SameFile(linkInfo, openedInfo) {
		return fmt.Errorf("%s changed while it was being prepared", name)
	}
	if err := directory.Chmod(mode); err != nil {
		return err
	}
	return directory.Chown(uid, gid)
}

func (s *HostSystem) SecureArtifacts(_ context.Context, site Site, artifacts []Artifact) error {
	lookupUser := s.lookupUser
	if lookupUser == nil {
		lookupUser = user.Lookup
	}
	lookupGroup := s.lookupGroup
	if lookupGroup == nil {
		lookupGroup = user.LookupGroup
	}
	account, err := lookupUser(site.UnixUser)
	if err != nil {
		return err
	}
	uid, err := strconv.Atoi(account.Uid)
	if err != nil || uid < 0 {
		return errors.New("site account returned an invalid UID")
	}
	group, err := lookupGroup("www-data")
	if err != nil {
		return err
	}
	gid, err := strconv.Atoi(group.Gid)
	if err != nil || gid < 0 {
		return errors.New("web server group returned an invalid GID")
	}
	root, err := os.OpenRoot(site.RootPath)
	if err != nil {
		return fmt.Errorf("open site root for artifact ownership: %w", err)
	}
	defer root.Close()
	for _, artifact := range artifacts {
		if artifact.Kind != "site-root" {
			continue
		}
		relative, err := filepath.Rel(site.RootPath, artifact.Path)
		if err != nil || relative == "." || !filepath.IsLocal(relative) {
			return fmt.Errorf("site artifact %s is outside its managed root", artifact.Path)
		}
		if err := secureOwnedArtifact(root, relative, artifact, uid, gid); err != nil {
			return fmt.Errorf("secure site artifact %s: %w", artifact.Path, err)
		}
	}
	return nil
}

// secureOwnedArtifact verifies and changes ownership through the already-opened
// file descriptor. A site account controls its public directory, so path-based
// chown would otherwise permit a symlink-swap race against the privileged agent.
func secureOwnedArtifact(root *os.Root, name string, artifact Artifact, uid, gid int) error {
	linkInfo, err := root.Lstat(name)
	if err != nil {
		return err
	}
	if linkInfo.Mode()&os.ModeSymlink != 0 || !linkInfo.Mode().IsRegular() {
		return errors.New("managed artifact is not a regular file")
	}
	file, err := root.Open(name)
	if err != nil {
		return err
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil {
		return err
	}
	if !openedInfo.Mode().IsRegular() || !os.SameFile(linkInfo, openedInfo) {
		return errors.New("managed artifact changed while it was being secured")
	}
	content, err := io.ReadAll(io.LimitReader(file, 512*1024+1))
	if err != nil {
		return err
	}
	if len(content) > 512*1024 || digestBytes(content) != digestString(artifact.Content) {
		return errors.New("managed artifact content changed before ownership was applied")
	}
	if err := file.Chmod(os.FileMode(artifact.Mode)); err != nil {
		return err
	}
	return file.Chown(uid, gid)
}

func (s *HostSystem) ValidatePHP(ctx context.Context, version string) error {
	return s.run(ctx, "php-fpm"+version, "-t")
}

func (s *HostSystem) ValidateNginx(ctx context.Context) error { return s.run(ctx, "nginx", "-t") }

func (s *HostSystem) ReloadPHP(ctx context.Context, version string) error {
	return s.run(ctx, "systemctl", "reload", "php"+version+"-fpm.service")
}

func (s *HostSystem) ReloadNginx(ctx context.Context) error {
	return s.run(ctx, "systemctl", "reload", "nginx.service")
}

func (s *HostSystem) run(ctx context.Context, name string, args ...string) error {
	output, err := s.command(ctx, name, args...)
	if err != nil {
		message := strings.TrimSpace(string(output))
		if len(message) > 500 {
			message = message[:500]
		}
		return fmt.Errorf("%s: %s", err, message)
	}
	return nil
}

// SiteHeader is emitted by every managed Nginx server block. It identifies which
// block served a response, so verification can tell the site apart from the
// default_server catch-all without depending on what the document root contains.
const SiteHeader = "X-Nexa-Site"

// VerifyHost polls until the site's own Nginx server block answers, because
// "systemctl reload" returns before Nginx serves the new configuration: old
// workers keep the old config for a few hundred milliseconds, during which the
// default_server answers instead.
func (s *HostSystem) VerifyHost(ctx context.Context, site Site) error {
	deadline := time.Now().Add(s.verifyTimeout)
	for {
		err := s.probeHost(ctx, site)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil || !time.Now().Before(deadline) {
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(s.verifyInterval):
		}
	}
}

func (s *HostSystem) probeHost(ctx context.Context, site Site) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+site.PrimaryDomain+"/", nil)
	if err != nil {
		return err
	}
	request.Host = site.PrimaryDomain
	response, err := s.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if served := response.Header.Get(SiteHeader); served != site.Slug {
		return fmt.Errorf("%s served status %d from a different Nginx server block (%s: %q, want %q)", site.PrimaryDomain, response.StatusCode, SiteHeader, served, site.Slug)
	}
	if response.StatusCode >= 500 {
		return fmt.Errorf("%s served status %d", site.PrimaryDomain, response.StatusCode)
	}
	return nil
}
