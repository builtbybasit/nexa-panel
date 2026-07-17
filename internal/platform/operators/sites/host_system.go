package sites

import (
	"context"
	"strconv"

	"net/http"

	"net"
	"path/filepath"

	"os/user"

	"fmt"
	"io"
	"strings"

	"os"

	"os/exec"
	"time"
)

func NewHostSystem() *HostSystem {
	dialer := &net.Dialer{Timeout: 3 * time.Second}
	return &HostSystem{
		command: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, name, args...).CombinedOutput()
		},
		client: &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, "tcp", "127.0.0.1:80")
		}}, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }, Timeout: 5 * time.Second},
		verifyTimeout:  15 * time.Second,
		verifyInterval: 250 * time.Millisecond,
	}
}

func (s *HostSystem) PrepareSite(ctx context.Context, site Site) error {
	account, err := user.Lookup(site.UnixUser)
	if err != nil {
		if _, commandErr := s.command(ctx, "useradd", "--system", "--user-group", "--home-dir", site.RootPath, "--shell", "/usr/sbin/nologin", site.UnixUser); commandErr != nil {
			return commandErr
		}
		account, err = user.Lookup(site.UnixUser)
	}
	if err != nil {
		return err
	}
	uid, err := strconv.Atoi(account.Uid)
	if err != nil {
		return err
	}
	group, err := user.LookupGroup("www-data")
	if err != nil {
		return err
	}
	gid, err := strconv.Atoi(group.Gid)
	if err != nil {
		return err
	}
	for path, mode := range map[string]os.FileMode{
		site.RootPath: 0o750, filepath.Join(site.RootPath, "public"): 0o750,
		filepath.Join(site.RootPath, "logs"): 0o770, filepath.Join(site.RootPath, "tmp"): 0o700,
		filepath.Join(site.RootPath, "private"): 0o700, filepath.Join(site.RootPath, "backups"): 0o700,
	} {
		if err := os.MkdirAll(path, mode); err != nil {
			return err
		}
		if err := os.Chmod(path, mode); err != nil {
			return err
		}
		if err := os.Chown(path, uid, gid); err != nil {
			return err
		}
	}
	return nil
}

func (s *HostSystem) SecureArtifacts(_ context.Context, site Site, artifacts []Artifact) error {
	account, err := user.Lookup(site.UnixUser)
	if err != nil {
		return err
	}
	uid, err := strconv.Atoi(account.Uid)
	if err != nil {
		return err
	}
	group, err := user.LookupGroup("www-data")
	if err != nil {
		return err
	}
	gid, err := strconv.Atoi(group.Gid)
	if err != nil {
		return err
	}
	for _, artifact := range artifacts {
		if artifact.Kind == "site-root" {
			if err := os.Chown(artifact.Path, uid, gid); err != nil {
				return err
			}
		}
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
