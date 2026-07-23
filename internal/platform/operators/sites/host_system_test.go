package sites

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

var verifySite = Site{Slug: "eample-site", PrimaryDomain: "example.com"}

// welcomePage mimics the stock Debian default_server, which answers 200 for any
// host it does not recognise and carries no X-Nexa-Site header.
func welcomePage(writer http.ResponseWriter, _ *http.Request) {
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write([]byte("<html><title>Welcome to nginx!</title></html>"))
}

func newVerifySystem(t *testing.T, timeout time.Duration, handler http.HandlerFunc) *HostSystem {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	address := strings.TrimPrefix(server.URL, "http://")
	dialer := &net.Dialer{}
	return &HostSystem{
		client: &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, "tcp", address)
		}}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		verifyTimeout:  timeout,
		verifyInterval: 5 * time.Millisecond,
	}
}

// The site root doubles as an OpenSSH chroot, so PrepareSite must never accept
// a symlink in its place: following one would let a swap repoint the jail (and
// the privileged chown behind it) at an attacker-chosen directory. The root is
// the first managed directory prepared, so a symlink there is rejected before
// any chown runs — which also keeps this test meaningful without root, where a
// chown to uid 0 on a real directory would otherwise fail first.
func TestPrepareSiteRejectsManagedDirectorySymlink(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "site")
	victim := filepath.Join(t.TempDir(), "victim")
	if err := os.Mkdir(victim, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, root); err != nil {
		t.Fatal(err)
	}

	system := NewHostSystem()
	system.lookupUser = func(string) (*user.User, error) {
		return &user.User{Uid: strconv.Itoa(os.Getuid())}, nil
	}
	system.lookupGroup = func(string) (*user.Group, error) {
		return &user.Group{Gid: strconv.Itoa(os.Getgid())}, nil
	}

	err := system.PrepareSite(context.Background(), Site{UnixUser: "nexa_demo", RootPath: root})
	if err == nil {
		t.Fatal("PrepareSite() = nil, want a managed-directory symlink to be rejected")
	}
	info, statErr := os.Stat(victim)
	if statErr != nil {
		t.Fatal(statErr)
	}
	if got := info.Mode().Perm(); got != 0o755 {
		t.Fatalf("victim mode = %o, want 755; PrepareSite followed the symlink", got)
	}
}

func TestPrepareSiteDoesNotCreateUserAfterLookupInfrastructureFailure(t *testing.T) {
	system := NewHostSystem()
	system.lookupUser = func(string) (*user.User, error) {
		return nil, errors.New("directory service unavailable")
	}
	called := false
	system.command = func(context.Context, string, ...string) ([]byte, error) {
		called = true
		return nil, nil
	}
	err := system.PrepareSite(context.Background(), Site{UnixUser: "nexa_demo", RootPath: filepath.Join(t.TempDir(), "site")})
	if err == nil || !strings.Contains(err.Error(), "look up site account") {
		t.Fatalf("PrepareSite() error = %v, want the lookup infrastructure failure", err)
	}
	if called {
		t.Fatal("PrepareSite invoked useradd after an inconclusive account lookup")
	}
}

// deployerSite is prepared through prepareDeployerLayout rather than through
// PrepareSite: the site root is chowned to uid 0 before the release tree is
// touched, which no unprivileged test can do. The uid and gid handed to the
// layout are the test process's own, so every chmod and chown below still runs
// for real.
func deployerSite(t *testing.T) (*os.Root, Site) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "site")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	return root, Site{Slug: "demo-site", UnixUser: "nexa_demo_site", RootPath: path, DeploymentMode: "deployer"}
}

func prepareTestLayout(t *testing.T, handle *os.Root, site Site) error {
	t.Helper()
	return prepareDeployerLayout(handle, site, os.Getuid(), os.Getgid(), os.Getgid())
}

func TestPrepareDeployerLayoutCreatesTheReleaseTree(t *testing.T) {
	handle, site := deployerSite(t)
	root := site.RootPath
	if err := prepareTestLayout(t, handle, site); err != nil {
		t.Fatalf("prepareDeployerLayout() = %v", err)
	}
	for path, mode := range map[string]os.FileMode{
		"app":                         0o755,
		"app/releases":                0o755,
		"app/releases/initial":        0o755,
		"app/releases/initial/public": 0o750,
		"app/shared":                  0o750,
		"app/shared/storage":          0o750,
	} {
		info, err := os.Stat(filepath.Join(root, path))
		if err != nil {
			t.Fatal(err)
		}
		if !info.IsDir() || info.Mode().Perm() != mode {
			t.Fatalf("%s mode = %v, want directory %o", path, info.Mode(), mode)
		}
	}
	// The site root itself must stay root-owned 0755: sshd refuses to chroot the
	// per-site SFTP jail into anything the login user could rename.
	info, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("site root mode = %o, want 755", info.Mode().Perm())
	}
	target, err := os.Readlink(filepath.Join(root, "app", "current"))
	if err != nil {
		t.Fatal(err)
	}
	if target != filepath.Join("releases", "initial") {
		t.Fatalf("current -> %q, want releases/initial", target)
	}
	placeholder, err := os.ReadFile(filepath.Join(root, "app", "current", "public", "index.php"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(placeholder), "demo-site") {
		t.Fatalf("placeholder = %q, want the site slug", placeholder)
	}
}

// A second activation prepares the layout again over a tree the deployer now
// owns. It must not put the placeholder back, must not re-point current at the
// initial release, and must not fail.
func TestPrepareDeployerLayoutLeavesADeployedReleaseTreeAlone(t *testing.T) {
	handle, site := deployerSite(t)
	root := site.RootPath
	if err := prepareTestLayout(t, handle, site); err != nil {
		t.Fatalf("prepareDeployerLayout() = %v", err)
	}
	deployed := filepath.Join(root, "app", "releases", "20260722")
	if err := os.MkdirAll(filepath.Join(deployed, "public"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "app", "current")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("releases", "20260722"), filepath.Join(root, "app", "current")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "app", "releases", "initial", "public", "index.php"), []byte("application"), 0o640); err != nil {
		t.Fatal(err)
	}

	if err := prepareTestLayout(t, handle, site); err != nil {
		t.Fatalf("second prepareDeployerLayout() = %v", err)
	}
	target, err := os.Readlink(filepath.Join(root, "app", "current"))
	if err != nil {
		t.Fatal(err)
	}
	if target != filepath.Join("releases", "20260722") {
		t.Fatalf("current -> %q, want the deployed release to be left alone", target)
	}
	content, err := os.ReadFile(filepath.Join(root, "app", "releases", "initial", "public", "index.php"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "application" {
		t.Fatalf("placeholder = %q, want the existing file to be left alone", content)
	}
}

// A deployer-mode site that serves out of a subdirectory resolves it below the
// release's public/, so the first activation has a document root to verify
// before any deploy has run.
func TestPrepareDeployerLayoutSeedsTheSubdirectoryDocumentRoot(t *testing.T) {
	handle, site := deployerSite(t)
	site.Settings.Subdirectory = "web/dist"
	if err := prepareTestLayout(t, handle, site); err != nil {
		t.Fatalf("prepareDeployerLayout() = %v", err)
	}
	if _, err := os.Stat(filepath.Join(documentRoot(site), "index.php")); err != nil {
		t.Fatalf("stat served index.php = %v, want the placeholder in the served directory", err)
	}
}

// A directory (or any other entry) where the release link belongs is not
// something the panel may adopt: replacing it would destroy whatever put it
// there, so the activation fails instead.
func TestPrepareDeployerLayoutRejectsACurrentThatIsNotALink(t *testing.T) {
	handle, site := deployerSite(t)
	if err := os.MkdirAll(filepath.Join(site.RootPath, "app", "current"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := prepareTestLayout(t, handle, site)
	if err == nil || !strings.Contains(err.Error(), "current is not a managed release link") {
		t.Fatalf("prepareDeployerLayout() = %v, want the unmanaged current entry to be refused", err)
	}
}

// The release tree is unmanaged by construction: no rendered artifact may resolve
// inside it, or a deploy would look like drift to checkBefore and Rollback.
func TestDeployerPlanCarriesNoArtifactInsideTheReleaseTree(t *testing.T) {
	site := Site{
		ID: "site-1", Slug: "demo-site", PrimaryDomain: "demo.example.com", PHPVersion: "8.4",
		UnixUser: "nexa_demo_site", RootPath: "/srv/nexa/sites/demo-site", SocketPath: "/run/php/nexa-demo-site.sock",
		DeploymentMode: "deployer",
	}
	plan, err := (Renderer{}).Render(site)
	if err != nil {
		t.Fatal(err)
	}
	releases := filepath.Join(releaseRoot(site), "releases")
	for _, path := range append(append([]string{}, plan.Retired...), artifactPaths(plan.Artifacts)...) {
		relative, err := filepath.Rel(releases, path)
		if err == nil && filepath.IsLocal(relative) {
			t.Fatalf("plan path %s resolves inside the release tree", path)
		}
	}
}

func artifactPaths(artifacts []Artifact) []string {
	paths := make([]string, 0, len(artifacts))
	for _, artifact := range artifacts {
		paths = append(paths, artifact.Path)
	}
	return paths
}

// Nginx accepts a dangling root and answers 404, and probeHost accepts a 404, so
// the missing-document-root case has to be caught by its own assertion.
func TestVerifyDocumentRootRejectsAMissingRoot(t *testing.T) {
	site := Site{Slug: "demo-site", RootPath: filepath.Join(t.TempDir(), "site")}
	err := NewHostSystem().VerifyDocumentRoot(context.Background(), site)
	if err == nil || !strings.Contains(err.Error(), filepath.Join(site.RootPath, "public")) {
		t.Fatalf("VerifyDocumentRoot() = %v, want an error naming the missing document root", err)
	}
}

func TestVerifyDocumentRootFollowsTheDeployerCurrentLink(t *testing.T) {
	handle, site := deployerSite(t)
	system := NewHostSystem()
	if err := system.VerifyDocumentRoot(context.Background(), site); err == nil {
		t.Fatal("VerifyDocumentRoot() = nil, want an error before the release tree exists")
	}
	if err := prepareTestLayout(t, handle, site); err != nil {
		t.Fatal(err)
	}
	if err := system.VerifyDocumentRoot(context.Background(), site); err != nil {
		t.Fatalf("VerifyDocumentRoot() = %v, want nil once the initial release is seeded", err)
	}
}

func TestVerifyDocumentRootRejectsAFileWhereTheRootBelongs(t *testing.T) {
	root := filepath.Join(t.TempDir(), "site")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "public"), []byte("not a directory"), 0o640); err != nil {
		t.Fatal(err)
	}
	err := NewHostSystem().VerifyDocumentRoot(context.Background(), Site{Slug: "demo-site", RootPath: root})
	if err == nil || !strings.Contains(err.Error(), "is not a directory") {
		t.Fatalf("VerifyDocumentRoot() = %v, want the non-directory root to be refused", err)
	}
}

func TestSecureArtifactsRejectsSymlinkWithoutChangingTarget(t *testing.T) {
	root := filepath.Join(t.TempDir(), "site")
	public := filepath.Join(root, "public")
	if err := os.MkdirAll(public, 0o750); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(victim, []byte("do not touch"), 0o600); err != nil {
		t.Fatal(err)
	}
	artifactPath := filepath.Join(public, "index.php")
	if err := os.Symlink(victim, artifactPath); err != nil {
		t.Fatal(err)
	}

	system := NewHostSystem()
	system.lookupUser = func(string) (*user.User, error) {
		return &user.User{Uid: strconv.Itoa(os.Getuid())}, nil
	}
	system.lookupGroup = func(string) (*user.Group, error) {
		return &user.Group{Gid: strconv.Itoa(os.Getgid())}, nil
	}
	err := system.SecureArtifacts(context.Background(), Site{UnixUser: "nexa_demo", RootPath: root}, []Artifact{{Kind: "site-root", Path: artifactPath, Mode: 0o640, Content: "managed"}})
	if err == nil {
		t.Fatal("SecureArtifacts() = nil, want a managed-artifact symlink to be rejected")
	}
	content, readErr := os.ReadFile(victim)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(content) != "do not touch" {
		t.Fatalf("victim contents = %q; SecureArtifacts followed the symlink", content)
	}
	info, statErr := os.Stat(victim)
	if statErr != nil {
		t.Fatal(statErr)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("victim mode = %o, want 600", got)
	}
}

func TestVerifyHostAcceptsResponseFromTheSiteServerBlock(t *testing.T) {
	system := newVerifySystem(t, time.Second, func(writer http.ResponseWriter, request *http.Request) {
		if request.Host != verifySite.PrimaryDomain {
			t.Errorf("Host = %q, want %q", request.Host, verifySite.PrimaryDomain)
		}
		writer.Header().Set(SiteHeader, verifySite.Slug)
		_, _ = writer.Write([]byte("Nexa Panel site eample-site is ready on PHP 8.3.6\n"))
	})
	if err := system.VerifyHost(context.Background(), verifySite); err != nil {
		t.Fatalf("VerifyHost() = %v, want nil", err)
	}
}

// A site whose document root already holds a real application has no Nexa
// placeholder in its body. The server block header still proves Nginx routed
// the request correctly, so verification must accept it.
func TestVerifyHostAcceptsApplicationContentWithoutThePlaceholder(t *testing.T) {
	system := newVerifySystem(t, time.Second, func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set(SiteHeader, verifySite.Slug)
		_, _ = writer.Write([]byte("<html><body>Some customer application</body></html>"))
	})
	if err := system.VerifyHost(context.Background(), verifySite); err != nil {
		t.Fatalf("VerifyHost() = %v, want nil", err)
	}
}

// Regression: "systemctl reload nginx" returns before Nginx serves the new
// configuration, so the first probes land on old workers and fall through to
// the default_server. VerifyHost must wait rather than fail the activation.
func TestVerifyHostWaitsForNginxReloadToTakeEffect(t *testing.T) {
	var probes atomic.Int32
	system := newVerifySystem(t, 2*time.Second, func(writer http.ResponseWriter, request *http.Request) {
		if probes.Add(1) <= 3 {
			welcomePage(writer, request)
			return
		}
		writer.Header().Set(SiteHeader, verifySite.Slug)
		_, _ = writer.Write([]byte("Nexa Panel site eample-site is ready on PHP 8.3.6\n"))
	})
	if err := system.VerifyHost(context.Background(), verifySite); err != nil {
		t.Fatalf("VerifyHost() = %v, want nil once the reload took effect", err)
	}
	if got := probes.Load(); got < 4 {
		t.Fatalf("probes = %d, want the probe to have retried past the stale config", got)
	}
}

func TestVerifyHostRejectsTheDefaultServerWelcomePage(t *testing.T) {
	system := newVerifySystem(t, 50*time.Millisecond, welcomePage)
	err := system.VerifyHost(context.Background(), verifySite)
	if err == nil {
		t.Fatal("VerifyHost() = nil, want an error when the default_server answers")
	}
	if !strings.Contains(err.Error(), "different Nginx server block") {
		t.Fatalf("VerifyHost() = %v, want an error naming the wrong server block", err)
	}
}

func TestVerifyHostRejectsServerErrorFromTheSite(t *testing.T) {
	system := newVerifySystem(t, 50*time.Millisecond, func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set(SiteHeader, verifySite.Slug)
		writer.WriteHeader(http.StatusBadGateway)
	})
	err := system.VerifyHost(context.Background(), verifySite)
	if err == nil {
		t.Fatal("VerifyHost() = nil, want an error when PHP-FPM is unreachable")
	}
	if !strings.Contains(err.Error(), "502") {
		t.Fatalf("VerifyHost() = %v, want an error naming status 502", err)
	}
}

func TestVerifyHostStopsWhenTheContextIsCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	system := newVerifySystem(t, time.Minute, func(writer http.ResponseWriter, request *http.Request) {
		cancel()
		welcomePage(writer, request)
	})
	if err := system.VerifyHost(ctx, verifySite); err == nil {
		t.Fatal("VerifyHost() = nil, want an error once the context is cancelled")
	}
}

// Nginx workers run as www-data and open auth_basic_user_file at request time,
// but writeArtifacts leaves the file root:root 0640 — unreadable by the worker,
// which turns every authenticated request into a 500. SecureArtifacts must hand
// the password file the web-server group.
func TestSecureArtifactsGivesTheHtpasswdTheWebServerGroup(t *testing.T) {
	root := t.TempDir()
	includes := filepath.Join(root, "includes")
	if err := os.MkdirAll(includes, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "site", "public"), 0o755); err != nil {
		t.Fatal(err)
	}
	htpasswd := Artifact{Kind: "nginx-htpasswd", Path: filepath.Join(includes, "nexa-demo.htpasswd"), Mode: 0o640, Content: "demo:$2y$10$abcdefghijklmnopqrstuv\n"}
	// Written the way writeArtifacts writes it: no ownership, restrictive mode.
	if err := os.WriteFile(htpasswd.Path, []byte(htpasswd.Content), 0o600); err != nil {
		t.Fatal(err)
	}

	system := NewHostSystem()
	system.lookupUser = func(string) (*user.User, error) {
		return &user.User{Uid: strconv.Itoa(os.Getuid())}, nil
	}
	system.lookupGroup = func(string) (*user.Group, error) {
		return &user.Group{Gid: strconv.Itoa(os.Getgid())}, nil
	}
	site := Site{UnixUser: "nexa_demo", RootPath: filepath.Join(root, "site")}
	if err := system.SecureArtifacts(context.Background(), site, []Artifact{htpasswd}); err != nil {
		t.Fatalf("SecureArtifacts() = %v", err)
	}

	info, err := os.Stat(htpasswd.Path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("mode = %o, want 640 (group-readable for the nginx worker)", info.Mode().Perm())
	}
	gid, err := fileGID(htpasswd.Path)
	if err != nil {
		t.Fatal(err)
	}
	if gid != os.Getgid() {
		t.Fatalf("gid = %d, want the web-server group %d", gid, os.Getgid())
	}
}

// A tampered artifact must not have ownership widened onto it: the content is
// re-checked against the plan first, so a swapped file is refused before chmod.
func TestSecureArtifactsRefusesATamperedHtpasswd(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "site", "public"), 0o755); err != nil {
		t.Fatal(err)
	}
	htpasswd := Artifact{Kind: "nginx-htpasswd", Path: filepath.Join(root, "nexa-demo.htpasswd"), Mode: 0o640, Content: "demo:$2y$10$expected\n"}
	if err := os.WriteFile(htpasswd.Path, []byte("attacker:$2y$10$swapped\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	system := NewHostSystem()
	system.lookupUser = func(string) (*user.User, error) {
		return &user.User{Uid: strconv.Itoa(os.Getuid())}, nil
	}
	system.lookupGroup = func(string) (*user.Group, error) {
		return &user.Group{Gid: strconv.Itoa(os.Getgid())}, nil
	}
	site := Site{UnixUser: "nexa_demo", RootPath: filepath.Join(root, "site")}
	if err := system.SecureArtifacts(context.Background(), site, []Artifact{htpasswd}); err == nil {
		t.Fatal("SecureArtifacts() = nil, want a tampered password file to be refused")
	}
}

func fileGID(path string) (int, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, errors.New("unsupported stat")
	}
	return int(stat.Gid), nil
}

// The seeded current link belongs to the site account, not to root. The vhost
// renders `disable_symlinks if_not_owner from={root}/app`, so nginx compares the
// owner of `current` with the owner of the release it points at: a root-owned
// link over a site-owned release would 403 every request on a freshly activated
// deployer-mode site. Lchown, not Chown — chowning through the link would
// re-own the release directory instead.
func TestPrepareDeployerLayoutOwnsTheCurrentLink(t *testing.T) {
	handle, site := deployerSite(t)
	if err := prepareTestLayout(t, handle, site); err != nil {
		t.Fatalf("prepareDeployerLayout() = %v", err)
	}
	link := filepath.Join(site.RootPath, "app", "current")
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("current is not a symlink")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Skip("this platform does not report link ownership")
	}
	if int(stat.Uid) != os.Getuid() || int(stat.Gid) != os.Getgid() {
		t.Fatalf("current link owned by %d:%d, want %d:%d", stat.Uid, stat.Gid, os.Getuid(), os.Getgid())
	}
	// The release it points at must not have been re-owned through the link: it
	// keeps the web group the rest of the release tree carries.
	target, err := os.Stat(filepath.Join(site.RootPath, "app", "releases", "initial"))
	if err != nil {
		t.Fatal(err)
	}
	if !target.IsDir() {
		t.Fatal("current does not resolve to the initial release")
	}
}

// purgeSystem builds a HostSystem whose account lookup and userdel are faked,
// so the removal contract can be exercised without a real /etc/passwd.
func purgeSystem(t *testing.T, site Site, account *user.User) (*HostSystem, *[]string) {
	t.Helper()
	commands := new([]string)
	return &HostSystem{
		command: func(_ context.Context, name string, args ...string) ([]byte, error) {
			*commands = append(*commands, strings.Join(append([]string{name}, args...), " "))
			return nil, nil
		},
		lookupUser: func(name string) (*user.User, error) {
			if account == nil || name != site.UnixUser {
				return nil, user.UnknownUserError(name)
			}
			return account, nil
		},
	}, commands
}

// The two pieces of host state no plan covers: after a teardown neither the
// account nor the site tree may be left behind.
func TestRemoveSiteDeletesTheManagedAccountAndRoot(t *testing.T) {
	root := t.TempDir()
	site := Site{Slug: "demo", UnixUser: "nexa_demo", RootPath: filepath.Join(root, "demo")}
	if err := os.MkdirAll(filepath.Join(site.RootPath, "public"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(site.RootPath, "public", "index.php"), []byte("x"), 0o640); err != nil {
		t.Fatal(err)
	}
	system, commands := purgeSystem(t, site, &user.User{Uid: "996", Gid: "996", Username: site.UnixUser, HomeDir: site.RootPath})
	if err := system.RemoveSite(context.Background(), site); err != nil {
		t.Fatal(err)
	}
	if len(*commands) != 1 || (*commands)[0] != "userdel nexa_demo" {
		t.Fatalf("commands = %v, want a single userdel", *commands)
	}
	if _, err := os.Stat(site.RootPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("site root survived the teardown: %v", err)
	}
}

// A teardown is retried after an interruption, so removing what is already gone
// has to be success rather than an error that can never clear.
func TestRemoveSiteIsIdempotent(t *testing.T) {
	root := t.TempDir()
	site := Site{Slug: "demo", UnixUser: "nexa_demo", RootPath: filepath.Join(root, "demo")}
	system, commands := purgeSystem(t, site, nil)
	if err := system.RemoveSite(context.Background(), site); err != nil {
		t.Fatalf("removing an already-absent site: %v", err)
	}
	if len(*commands) != 0 {
		t.Fatalf("commands = %v, want none for an account that does not exist", *commands)
	}
}

// The agent runs as root. An account that does not match the managed contract —
// a privileged UID, or a home directory that is not this site's root — is never
// deleted, however the site row happens to name it.
func TestRemoveSiteRefusesAnAccountThatIsNotTheManagedOwner(t *testing.T) {
	root := t.TempDir()
	site := Site{Slug: "demo", UnixUser: "nexa_demo", RootPath: filepath.Join(root, "demo")}
	if err := os.MkdirAll(site.RootPath, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, account := range map[string]*user.User{
		"privileged UID":  {Uid: "0", Gid: "0", Username: site.UnixUser, HomeDir: site.RootPath},
		"foreign home":    {Uid: "996", Gid: "996", Username: site.UnixUser, HomeDir: "/home/operator"},
		"unparseable UID": {Uid: "root", Gid: "0", Username: site.UnixUser, HomeDir: site.RootPath},
	} {
		system, commands := purgeSystem(t, site, account)
		if err := system.RemoveSite(context.Background(), site); err == nil {
			t.Fatalf("%s: the account was deleted despite failing the managed contract", name)
		}
		if len(*commands) != 0 {
			t.Fatalf("%s: commands = %v, want none", name, *commands)
		}
		if _, err := os.Stat(site.RootPath); err != nil {
			t.Fatalf("%s: the site root was removed after a refused account deletion: %v", name, err)
		}
	}
}

// A symlink standing in for the site root must not be followed: removing it
// would delete whatever it points at, chosen by whoever could write the parent.
func TestRemoveSiteRefusesASymlinkedSiteRoot(t *testing.T) {
	root := t.TempDir()
	elsewhere := filepath.Join(root, "elsewhere")
	if err := os.MkdirAll(elsewhere, 0o755); err != nil {
		t.Fatal(err)
	}
	site := Site{Slug: "demo", UnixUser: "nexa_demo", RootPath: filepath.Join(root, "demo")}
	if err := os.Symlink(elsewhere, site.RootPath); err != nil {
		t.Fatal(err)
	}
	system, _ := purgeSystem(t, site, nil)
	if err := system.RemoveSite(context.Background(), site); err == nil {
		t.Fatal("a symlinked site root was accepted")
	}
	if _, err := os.Stat(elsewhere); err != nil {
		t.Fatalf("the symlink target was removed: %v", err)
	}
}

// Purge re-derives the identity from the renderer before anything is destroyed,
// so a site naming a path or account outside the managed layout never reaches
// the removal at all.
func TestPurgeRefusesASiteOutsideTheManagedLayout(t *testing.T) {
	system := new(fakeNodeSystem)
	operator, site := testHostOperator(t, system)
	site.RootPath = "/srv/somebody-elses/tree"
	if err := operator.Purge(context.Background(), site); err == nil {
		t.Fatal("Purge accepted a site root outside the managed sites root")
	}
	if len(system.calls) != 0 {
		t.Fatalf("calls = %v, want the node untouched", system.calls)
	}
}
