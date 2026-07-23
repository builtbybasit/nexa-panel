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
	"syscall"
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
	if deployerLayout(site) {
		siteGID, err := strconv.Atoi(account.Gid)
		if err != nil || siteGID < 0 {
			return errors.New("site account returned an invalid GID")
		}
		if err := prepareDeployerLayout(root, site, uid, siteGID, gid); err != nil {
			return fmt.Errorf("prepare site release tree: %w", err)
		}
	}
	return prepareDocumentRoot(root, site, uid, gid)
}

// RemoveSite undoes PrepareSite. It is the last step of a teardown, and it is
// deliberately paranoid: the agent runs as root, so every identity it is asked
// to destroy is verified against the managed account contract first — the same
// contract scripts/uninstall.sh enforces before it deletes anything. The name
// is already pinned to "nexa_<slug>" by the caller's re-validation, and an
// account whose UID is 0 or whose home directory is not this site's root is
// left untouched, so the teardown fails loudly rather than deleting an
// operator's account that happens to collide.
//
// Both halves are idempotent, because a teardown is retried after an
// interruption: an account or a root that is already gone is success.
func (s *HostSystem) RemoveSite(ctx context.Context, site Site) error {
	if err := s.removeSiteAccount(ctx, site); err != nil {
		return err
	}
	return removeSiteRoot(site.RootPath)
}

func (s *HostSystem) removeSiteAccount(ctx context.Context, site Site) error {
	lookupUser := s.lookupUser
	if lookupUser == nil {
		lookupUser = user.Lookup
	}
	account, err := lookupUser(site.UnixUser)
	var unknown user.UnknownUserError
	if errors.As(err, &unknown) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("look up site account: %w", err)
	}
	uid, err := strconv.Atoi(account.Uid)
	if err != nil || uid <= 0 {
		return fmt.Errorf("site account %s has an invalid or privileged UID; refusing to delete it", site.UnixUser)
	}
	if filepath.Clean(account.HomeDir) != filepath.Clean(site.RootPath) {
		return fmt.Errorf("site account %s is not the managed owner of %s; refusing to delete it", site.UnixUser, site.RootPath)
	}
	// --remove is deliberately not passed: it would delete whatever the home path
	// currently resolves to, following a symlink an attacker could have put
	// there. The root is removed separately, through its parent's descriptor.
	if output, err := s.command(ctx, "userdel", site.UnixUser); err != nil {
		message := strings.TrimSpace(string(output))
		if len(message) > 500 {
			message = message[:500]
		}
		return fmt.Errorf("delete site account %s: %s: %s", site.UnixUser, err, message)
	}
	return nil
}

// removeSiteRoot deletes the managed site tree through its parent directory's
// own descriptor, so the entry is identified once and cannot be swapped for a
// symlink pointing somewhere else between the check and the removal. A path
// that is not a real directory is refused rather than removed.
func removeSiteRoot(rootPath string) error {
	parent, err := os.OpenRoot(filepath.Dir(rootPath))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("open site parent: %w", err)
	}
	defer parent.Close()
	name := filepath.Base(rootPath)
	info, err := parent.Lstat(name)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("%s is not a managed site root", rootPath)
	}
	if err := parent.RemoveAll(name); err != nil {
		return fmt.Errorf("remove site root %s: %w", rootPath, err)
	}
	return nil
}

// prepareDeployerLayout creates releases/ and shared/ and seeds the initial
// release plus the current symlink, so `nginx -t` and the health probe pass
// before the first deploy has ever run. Everything it creates is idempotent and
// unmanaged: the deployer owns the release tree afterwards and the panel never
// re-asserts ownership below releases/.
//
// The tree is nested at {root}/app rather than sitting directly in the site
// root because the deploy user re-points {root}/app/current with `ln -sfn`,
// which needs write on the directory holding the link — and {root} itself must
// stay root-owned and non-group-writable or sshd refuses to chroot the per-site
// SFTP jail into it. {root}/app is therefore owned nexa_<slug>:nexa_<slug>; the
// directories below it keep the www-data group the rest of the site uses, so
// the web server can traverse and read what a release publishes.
func prepareDeployerLayout(root *os.Root, site Site, uid, siteGID, webGID int) error {
	if err := prepareOwnedDirectory(root, "app", 0o755, uid, siteGID); err != nil {
		return fmt.Errorf("prepare release root: %w", err)
	}
	app, err := root.OpenRoot("app")
	if err != nil {
		return fmt.Errorf("open release root: %w", err)
	}
	defer app.Close()
	for _, directory := range []struct {
		name string
		mode os.FileMode
	}{
		{name: "releases", mode: 0o755},
		{name: "shared", mode: 0o750},
	} {
		if err := prepareOwnedDirectory(app, directory.name, directory.mode, uid, webGID); err != nil {
			return fmt.Errorf("prepare release directory %s: %w", directory.name, err)
		}
	}
	shared, err := app.OpenRoot("shared")
	if err != nil {
		return fmt.Errorf("open shared directory: %w", err)
	}
	defer shared.Close()
	if err := prepareOwnedDirectory(shared, "storage", 0o750, uid, webGID); err != nil {
		return fmt.Errorf("prepare release directory shared/storage: %w", err)
	}
	releases, err := app.OpenRoot("releases")
	if err != nil {
		return fmt.Errorf("open releases directory: %w", err)
	}
	defer releases.Close()
	if err := prepareOwnedDirectory(releases, "initial", 0o755, uid, webGID); err != nil {
		return fmt.Errorf("prepare release directory releases/initial: %w", err)
	}
	initial, err := releases.OpenRoot("initial")
	if err != nil {
		return fmt.Errorf("open initial release: %w", err)
	}
	defer initial.Close()
	if err := prepareOwnedDirectory(initial, "public", 0o750, uid, webGID); err != nil {
		return fmt.Errorf("prepare release directory releases/initial/public: %w", err)
	}
	public, err := initial.OpenRoot("public")
	if err != nil {
		return fmt.Errorf("open initial release document root: %w", err)
	}
	defer public.Close()
	// The subdirectory override is resolved below the release's public/, not
	// below {root}/public, because that is where documentRoot() points in
	// deployer mode — without it the very first activation would fail
	// VerifyDocumentRoot on a site that serves out of a subdirectory.
	served, err := prepareSubdirectory(public, site.Settings.Subdirectory, uid, webGID)
	if err != nil {
		return err
	}
	defer served.Close()
	if err := seedReleasePlaceholder(served, site, uid, webGID); err != nil {
		return fmt.Errorf("seed initial release placeholder: %w", err)
	}
	return prepareCurrentSymlink(app, filepath.Join("releases", "initial"), uid, siteGID)
}

// seedReleasePlaceholder writes the pre-deploy placeholder into the initial
// release, and only when nothing is there: the file is deliberately not a
// managed artifact (see Render), so the first real deploy simply replaces the
// release that contains it and no digest gate ever sees it. O_EXCL is what
// makes that "only when absent" atomic, and it also means an entry the site
// account put there — including a symlink — is left untouched rather than
// chowned.
func seedReleasePlaceholder(public *os.Root, site Site, uid, gid int) error {
	content, err := execute(indexTemplate, site)
	if err != nil {
		return err
	}
	file, err := public.OpenFile("index.php", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o640)
	if err != nil {
		if os.IsExist(err) {
			return nil
		}
		return err
	}
	defer file.Close()
	if _, err := file.WriteString(content); err != nil {
		return err
	}
	if err := file.Chmod(0o640); err != nil {
		return err
	}
	return file.Chown(uid, gid)
}

// prepareCurrentSymlink installs {root}/app/current -> releases/initial when the
// link is absent. An existing symlink is left exactly as the deployer set it; a
// non-symlink at that path is a hard error, mirroring prepareOwnedDirectory's
// refusal to adopt an unmanaged entry. Root creates the first link because the
// site root is root-owned, and the deployer replaces it atomically at deploy
// time with `ln -sfn` inside {root}/app, which the site account does own.
//
// The seeded link is lchowned to the site account, and that is load-bearing
// rather than cosmetic: the vhost renders `disable_symlinks if_not_owner
// from={root}/app` (see symlinkFrom), so nginx compares the owner of `current`
// with the owner of the release it points at. A root-owned link over a
// site-owned release would 403 every request, and it is the same check that
// stops the site account re-pointing `current` at another account's tree.
func prepareCurrentSymlink(app *os.Root, target string, uid, gid int) error {
	info, err := app.Lstat("current")
	switch {
	case err == nil && info.Mode()&os.ModeSymlink != 0:
		return nil
	case err == nil:
		return errors.New("current is not a managed release link")
	case !os.IsNotExist(err):
		return err
	}
	// A concurrent PrepareSite (or a deploy) may have won the race; the link it
	// created is as valid as the one this call meant to create — and it already
	// belongs to whoever created it, so it is not re-owned here.
	if err := app.Symlink(target, "current"); err != nil {
		if os.IsExist(err) {
			return nil
		}
		return err
	}
	// Lchown, never Chown: chowning through the link would re-own the release
	// directory it points at instead of the link itself.
	if err := app.Lchown("current", uid, gid); err != nil {
		return fmt.Errorf("set release link ownership: %w", err)
	}
	return nil
}

// prepareDocumentRoot creates the optional subdirectory the site serves from, so
// activating a subdirectory override does not leave Nginx pointing at a missing
// path. Each segment is created through the parent's own descriptor, keeping the
// same symlink-substitution safety as the fixed directories above.
func prepareDocumentRoot(root *os.Root, site Site, uid, gid int) error {
	if site.Settings.Subdirectory == "" {
		return nil
	}
	public, err := root.OpenRoot("public")
	if err != nil {
		return fmt.Errorf("open site public directory: %w", err)
	}
	defer public.Close()
	served, err := prepareSubdirectory(public, site.Settings.Subdirectory, uid, gid)
	if err != nil {
		return err
	}
	return served.Close()
}

// prepareSubdirectory walks the configured subdirectory below an already-opened
// public directory and returns a descriptor for the served directory itself,
// which the caller closes. Each segment is created through its parent's own
// descriptor, keeping the same symlink-substitution safety as the fixed
// directories above.
func prepareSubdirectory(public *os.Root, subdirectory string, uid, gid int) (*os.Root, error) {
	// Re-opened rather than returned directly so the caller's descriptor and the
	// returned one have independent lifetimes; closing the walk's intermediate
	// descriptors must never close the public directory it started from.
	current, err := public.OpenRoot(".")
	if err != nil {
		return nil, fmt.Errorf("open site public directory: %w", err)
	}
	if subdirectory == "" {
		return current, nil
	}
	// Closed via a closure so the *final* descriptor is released; `defer
	// current.Close()` would bind the receiver now and double-close public/.
	failed := true
	defer func() {
		if failed {
			current.Close()
		}
	}()
	for _, segment := range strings.Split(subdirectory, "/") {
		if err := prepareOwnedDirectory(current, segment, 0o750, uid, gid); err != nil {
			return nil, fmt.Errorf("prepare site document root %s: %w", subdirectory, err)
		}
		next, err := current.OpenRoot(segment)
		if err != nil {
			return nil, fmt.Errorf("open site document root %s: %w", subdirectory, err)
		}
		current.Close()
		current = next
	}
	failed = false
	return current, nil
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
		switch artifact.Kind {
		case "site-root":
			relative, err := filepath.Rel(site.RootPath, artifact.Path)
			if err != nil || relative == "." || !filepath.IsLocal(relative) {
				return fmt.Errorf("site artifact %s is outside its managed root", artifact.Path)
			}
			if err := secureOwnedArtifact(root, relative, artifact, uid, gid); err != nil {
				return fmt.Errorf("secure site artifact %s: %w", artifact.Path, err)
			}
		case "nginx-htpasswd":
			// The password file is the one managed artifact Nginx itself must read
			// at request time, and its workers run as www-data. Written by
			// writeArtifacts it lands root:root 0640, which the worker cannot open,
			// so auth_basic fails every authenticated request with a 500. Giving it
			// the www-data group (root-owned, 0640) makes it readable by the worker
			// and by nobody else — the site account deliberately does not get it,
			// since a site must not be able to read or rewrite its own hash file.
			if err := secureGroupReadableArtifact(artifact, -1, gid); err != nil {
				return fmt.Errorf("secure site artifact %s: %w", artifact.Path, err)
			}
		}
	}
	return nil
}

// secureGroupReadableArtifact applies ownership to a managed artifact that lives
// outside the site root. Unlike secureOwnedArtifact it cannot go through an
// *os.Root, so it opens with O_NOFOLLOW and verifies the descriptor is a regular
// file whose content still matches the plan before touching mode or ownership.
// The containing directories are root-owned, so this is defence in depth rather
// than a race the site account can currently win.
//
// A uid of -1 leaves the owner untouched, which is what the htpasswd case wants:
// the agent wrote the file as root, only the group needs adjusting, and not
// re-asserting the owner keeps this usable off the privileged path.
func secureGroupReadableArtifact(artifact Artifact, uid, gid int) error {
	file, err := os.OpenFile(artifact.Path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("managed artifact is not a regular file")
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

// VerifyDocumentRoot asserts the served root exists and is a directory before
// Nginx is asked to accept the configuration. Nginx validates a dangling root
// happily and then answers 404 for every request, and probeHost accepts a 404
// because a real application legitimately answers one at "/" — so without this
// an activation whose document root was never created reports success. It
// matters most in deployer mode, where the root resolves through the current
// symlink, but the check is mode-independent and closes the same hole for
// standard sites.
func (s *HostSystem) VerifyDocumentRoot(_ context.Context, site Site) error {
	root := documentRoot(site)
	info, err := os.Stat(root)
	if err != nil {
		return fmt.Errorf("site document root %s: %w", root, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("site document root %s is not a directory", root)
	}
	return nil
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
