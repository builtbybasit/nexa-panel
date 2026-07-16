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

func (s *HostSystem) VerifyHost(ctx context.Context, site Site) error {
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
	body, err := io.ReadAll(io.LimitReader(response.Body, 4096))
	if err != nil {
		return err
	}
	if response.StatusCode >= 500 || (response.StatusCode < 300 && !strings.Contains(string(body), "Nexa Panel site "+site.Slug)) {
		return fmt.Errorf("unexpected local response status %d", response.StatusCode)
	}
	return nil
}
