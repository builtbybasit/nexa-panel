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

func TestPrepareSiteRejectsManagedDirectorySymlink(t *testing.T) {
	root := filepath.Join(t.TempDir(), "site")
	if err := os.MkdirAll(root, 0o750); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(t.TempDir(), "victim")
	if err := os.Mkdir(victim, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(root, "logs")); err != nil {
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
