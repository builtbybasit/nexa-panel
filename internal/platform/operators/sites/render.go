package sites

import (
	"context"
	"errors"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	slugPattern   = regexp.MustCompile(`^[a-z][a-z0-9-]{1,31}$`)
	domainPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+$`)
	// phpVersionPattern gates the shape only. PHP branches are not enumerated
	// here: the Applications page offers whatever the node's PHP repository
	// publishes, and a site must be able to run any branch that can be installed.
	// The one rule is the floor below.
	phpVersionPattern = regexp.MustCompile(`^[0-9]{1,2}\.[0-9]{1,2}$`)
)

// phpFloor is the oldest PHP branch Nexa serves.
const phpFloor = "7.4"

// phpVersionSupported accepts any well-formed branch at or above the floor, so a
// PHP release published after this code was written is usable without an edit.
func phpVersionSupported(version string) bool {
	if !phpVersionPattern.MatchString(version) {
		return false
	}
	return comparePHPVersions(version, phpFloor) >= 0
}

// comparePHPVersions orders dotted numeric versions field by field, so "8.10"
// sorts above "8.9" rather than below it as strings would.
func comparePHPVersions(a, b string) int {
	left, right := strings.Split(a, "."), strings.Split(b, ".")
	for index := 0; index < len(left) || index < len(right); index++ {
		var first, second int
		if index < len(left) {
			first, _ = strconv.Atoi(left[index])
		}
		if index < len(right) {
			second, _ = strconv.Atoi(right[index])
		}
		if first != second {
			return first - second
		}
	}
	return 0
}

type Site struct {
	ID            string   `json:"id"`
	Slug          string   `json:"slug"`
	PrimaryDomain string   `json:"primaryDomain"`
	PHPVersion    string   `json:"phpVersion"`
	UnixUser      string   `json:"unixUser"`
	RootPath      string   `json:"rootPath"`
	SocketPath    string   `json:"socketPath"`
	Routes        []Route  `json:"routes,omitempty"`
	TLS           *TLS     `json:"tls,omitempty"`
	TLSDomains    []string `json:"tlsDomains,omitempty"`
}

type Route struct {
	Hostname       string `json:"hostname"`
	Kind           string `json:"kind"`
	RedirectTarget string `json:"redirectTarget,omitempty"`
}

type TLS struct {
	CertificatePath string `json:"certificatePath"`
	PrivateKeyPath  string `json:"privateKeyPath"`
}

type Artifact struct {
	Kind    string `json:"kind"`
	Path    string `json:"path"`
	Mode    uint32 `json:"mode"`
	Content string `json:"content"`
}

type Plan struct {
	ID            string     `json:"id"`
	Kind          string     `json:"kind"`
	Site          Site       `json:"site"`
	Artifacts     []Artifact `json:"artifacts"`
	Before        []Snapshot `json:"before"`
	EnabledBefore bool       `json:"enabledBefore"`
	Warnings      []string   `json:"warnings"`
	PlannedAt     time.Time  `json:"plannedAt"`
	ExpiresAt     time.Time  `json:"expiresAt"`
	Signature     string     `json:"signature,omitempty"`
}

const PlanKind = "nexa.site.activation.v1"

type Snapshot struct {
	Path    string `json:"path"`
	Exists  bool   `json:"exists"`
	Mode    uint32 `json:"mode,omitempty"`
	Digest  string `json:"digest,omitempty"`
	Content string `json:"content,omitempty"`
	UID     int    `json:"uid,omitempty"`
	GID     int    `json:"gid,omitempty"`
}

type Observation struct {
	SiteID     string     `json:"siteId"`
	Active     bool       `json:"active"`
	Artifacts  []Snapshot `json:"artifacts"`
	VerifiedAt time.Time  `json:"verifiedAt"`
}

type Operator interface {
	Plan(ctx context.Context, site Site) (Plan, error)
	Apply(ctx context.Context, plan Plan) (Observation, error)
	Rollback(ctx context.Context, plan Plan) (Observation, error)
}

type Renderer struct {
	NginxAvailableRoot string
	PHPConfigRoot      string
	SiteRoot           string
	SocketRoot         string
}

func (r Renderer) Render(site Site) (Plan, error) {
	if err := r.validate(site); err != nil {
		return Plan{}, err
	}
	nginxRoot := r.NginxAvailableRoot
	if nginxRoot == "" {
		nginxRoot = "/etc/nginx/sites-available"
	}
	phpRoot := r.PHPConfigRoot
	if phpRoot == "" {
		phpRoot = "/etc/php"
	}

	nginx, err := renderNginx(site)
	if err != nil {
		return Plan{}, err
	}
	fpm, err := execute(fpmTemplate, site)
	if err != nil {
		return Plan{}, err
	}
	index, err := execute(indexTemplate, site)
	if err != nil {
		return Plan{}, err
	}
	return Plan{Site: site, Artifacts: []Artifact{
		{Kind: "site-root", Path: filepath.Join(site.RootPath, "public", "index.php"), Mode: 0o640, Content: index},
		{Kind: "php-fpm-pool", Path: filepath.Join(phpRoot, site.PHPVersion, "fpm", "pool.d", "nexa-"+site.Slug+".conf"), Mode: 0o640, Content: fpm},
		{Kind: "nginx-site", Path: filepath.Join(nginxRoot, "nexa-"+site.Slug+".conf"), Mode: 0o640, Content: nginx},
	}, Warnings: []string{"Activation requires PHP-FPM and Nginx validation on the managed node."}}, nil
}

func (r Renderer) validate(site Site) error {
	if site.ID == "" || !slugPattern.MatchString(site.Slug) || !domainPattern.MatchString(site.PrimaryDomain) || !phpVersionSupported(site.PHPVersion) {
		return errors.New("site identity, slug, primary domain, and a PHP " + phpFloor + " or newer runtime are required")
	}
	expectedUser := "nexa_" + strings.ReplaceAll(site.Slug, "-", "_")
	if site.UnixUser != expectedUser {
		return errors.New("site Unix owner must be derived from its slug")
	}
	siteRoot := r.SiteRoot
	if siteRoot == "" {
		siteRoot = "/srv/nexa/sites"
	}
	socketRoot := r.SocketRoot
	if socketRoot == "" {
		socketRoot = "/run/php"
	}
	expectedRoot := filepath.Join(siteRoot, site.Slug)
	expectedSocket := filepath.Join(socketRoot, "nexa-"+site.Slug+".sock")
	if filepath.Clean(site.RootPath) != expectedRoot || filepath.Clean(site.SocketPath) != expectedSocket {
		return errors.New("site root and socket paths must be derived from its slug")
	}
	seen := map[string]struct{}{site.PrimaryDomain: {}}
	for _, route := range site.Routes {
		if !domainPattern.MatchString(route.Hostname) {
			return errors.New("site route hostname is invalid")
		}
		if _, exists := seen[route.Hostname]; exists {
			return errors.New("site route hostnames must be unique")
		}
		seen[route.Hostname] = struct{}{}
		if route.Kind != "alias" && route.Kind != "subdomain" && route.Kind != "redirect" {
			return errors.New("site route kind is invalid")
		}
		if route.Kind == "redirect" {
			if !domainPattern.MatchString(route.RedirectTarget) {
				return errors.New("redirect target must be a hostname")
			}
		} else if route.RedirectTarget != "" {
			return errors.New("only redirect routes may have a target")
		}
	}
	if site.TLS != nil {
		certificateRoot := filepath.Join("/etc/letsencrypt/live", site.PrimaryDomain)
		if filepath.Clean(site.TLS.CertificatePath) != filepath.Join(certificateRoot, "fullchain.pem") || filepath.Clean(site.TLS.PrivateKeyPath) != filepath.Join(certificateRoot, "privkey.pem") {
			return errors.New("TLS paths must use the primary domain certificate directory")
		}
		allowedTLS := map[string]struct{}{site.PrimaryDomain: {}}
		for _, route := range site.Routes {
			if route.Kind != "redirect" {
				allowedTLS[route.Hostname] = struct{}{}
			}
		}
		for _, hostname := range site.TLSDomains {
			if _, ok := allowedTLS[hostname]; !ok {
				return errors.New("TLS domain is not attached to the site")
			}
		}
	} else if len(site.TLSDomains) > 0 {
		return errors.New("TLS domains require certificate paths")
	}
	return nil
}

const nginxTemplate = `# Managed by Nexa Panel.
{{range .Redirects}}server {
    listen 80;
    listen [::]:80;
    server_name {{.Hostname}};
    location ^~ /.well-known/acme-challenge/ { root /srv/nexa/acme; }
    location / { return 301 https://{{.RedirectTarget}}$request_uri; }
}
{{end}}
server {
    listen 80;
    listen [::]:80;
    server_name {{.Names}};
    add_header X-Nexa-Site {{.Site.Slug}} always;
    location ^~ /.well-known/acme-challenge/ { root /srv/nexa/acme; }
{{if .Site.TLS}}    location / { return 301 https://{{.Site.PrimaryDomain}}$request_uri; }
{{else}}    root {{.Site.RootPath}}/public;
    index index.php index.html;

    access_log {{.Site.RootPath}}/logs/access.log;
    error_log {{.Site.RootPath}}/logs/error.log;

    location / {
        try_files $uri $uri/ /index.php?$query_string;
    }

    location ~ \.php$ {
        try_files $uri =404;
        include fastcgi_params;
        fastcgi_param SCRIPT_FILENAME $document_root$fastcgi_script_name;
        fastcgi_pass unix:{{.Site.SocketPath}};
    }

    location ~ /\. {
        deny all;
    }
{{end}}
}
{{if .Site.TLS}}
server {
    listen 443 ssl;
    listen [::]:443 ssl;
    server_name {{.TLSNames}};
    add_header X-Nexa-Site {{.Site.Slug}} always;
    ssl_certificate {{.Site.TLS.CertificatePath}};
    ssl_certificate_key {{.Site.TLS.PrivateKeyPath}};
    ssl_protocols TLSv1.2 TLSv1.3;
    root {{.Site.RootPath}}/public;
    index index.php index.html;
    access_log {{.Site.RootPath}}/logs/access.log;
    error_log {{.Site.RootPath}}/logs/error.log;
    location / { try_files $uri $uri/ /index.php?$query_string; }
    location ~ \.php$ {
        try_files $uri =404;
        include fastcgi_params;
        fastcgi_param SCRIPT_FILENAME $document_root$fastcgi_script_name;
        fastcgi_pass unix:{{.Site.SocketPath}};
    }
    location ~ /\. { deny all; }
}
{{end}}
`

const fpmTemplate = `; Managed by Nexa Panel.
[nexa-{{.Slug}}]
user = {{.UnixUser}}
group = {{.UnixUser}}
listen = {{.SocketPath}}
listen.owner = www-data
listen.group = www-data
listen.mode = 0660
pm = ondemand
pm.max_children = 3
pm.process_idle_timeout = 10s
pm.max_requests = 500
chdir = {{.RootPath}}
catch_workers_output = yes
clear_env = yes
security.limit_extensions = .php
php_admin_value[error_log] = {{.RootPath}}/logs/php-error.log
php_admin_flag[log_errors] = on
`

const indexTemplate = `<?php
header('Content-Type: text/plain; charset=utf-8');
echo "Nexa Panel site {{.Slug}} is ready on PHP " . PHP_VERSION . "\n";
`
