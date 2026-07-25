package sites

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"text/template"
)

const (
	defaultGzipLevel       = 5
	defaultExpiresDays     = 30
	defaultClientMaxBodyMB = 64
	defaultHSTSMaxAge      = 15552000 // 180 days
	defaultRateReqPerSec   = 10
	defaultRateBurst       = 20
	defaultFPMMaxChildren  = 3
	defaultFPMMaxRequests  = 500
	basicAuthDefaultRealm  = "Restricted"

	defaultLogRotateKeep      = 14
	defaultLogRotateFrequency = "daily"
)

// effectiveSettings holds fully-resolved values the templates reference. Every
// field is concrete (no pointers, no nils) so the templates stay trivial and
// missingkey=error can never fire. It is a pure function of Site.Settings plus
// the site's slug/paths, which is what keeps validatePlan's byte-exact re-render
// stable.
type effectiveSettings struct {
	DocRoot         string
	SymlinkFrom     string
	IndexFiles      string
	FrontController string
	Gzip            bool
	GzipLevel       int
	StaticCache     bool
	ExpiresDays     int
	ClientMaxBodyMB int
	Charset         string
	AccessLog       bool
	ErrorLog        bool
	HTTP2           bool
	HTTP3           bool
	HTTPSRedirect   bool
	HSTS            bool
	HSTSHeaderValue string
	OverrideGlob    string
	BasicAuth       bool
	BasicAuthRealm  string
	HtpasswdPath    string
	RateLimit       bool
	RateZone        string
	RateBurst       int
}

func intOr(p *int, fallback int) int {
	if p != nil {
		return *p
	}
	return fallback
}

func boolOr(p *bool, fallback bool) bool {
	if p != nil {
		return *p
	}
	return fallback
}

// zeroOr treats a plain-int 0 as "unset" and substitutes the default. Used for
// fields where 0 is never a meaningful value (gzip level, worker count, rate).
func zeroOr(value, fallback int) int {
	if value == 0 {
		return fallback
	}
	return value
}

// rateZone maps a slug to a valid nginx shared-memory zone name; hyphens are not
// permitted in zone identifiers.
func rateZone(slug string) string { return "nexa_" + strings.ReplaceAll(slug, "-", "_") }

// releaseRoot is the release tree a deployer-mode site is served from. It is
// nested one level below the site root so {root} itself stays root-owned and
// non-group-writable: sshd refuses a ChrootDirectory it does not own, and the
// per-site SFTP jail chroots to {root}.
func releaseRoot(site Site) string { return filepath.Join(site.RootPath, "app") }

// documentRoot resolves the served root: the site's public/ directory — or, in
// deployer mode, the public/ directory of whichever release {root}/app/current
// points at — plus the optional subdirectory beneath it. validateSubdirectory
// has already refused any traversal, so this can never escape public/.
//
// See symlinkFrom for why the rendered disable_symlinks prefix is NOT this path
// in deployer mode.
func documentRoot(site Site) string {
	root := filepath.Join(site.RootPath, "public")
	if deployerLayout(site) {
		root = filepath.Join(releaseRoot(site), "current", "public")
	}
	if site.Settings.Subdirectory == "" {
		return root
	}
	return filepath.Join(root, site.Settings.Subdirectory)
}

// symlinkFrom is the `disable_symlinks if_not_owner from=` prefix. nginx only
// ownership-checks the path components that come *after* this prefix, so the
// prefix decides which links a site can plant and have nginx follow blindly.
//
// In standard mode the prefix is the document root itself: {root} is root-owned,
// so the site account can only plant links below public/, which are all checked.
//
// In deployer mode the document root contains `current`, a symlink the site
// account owns and re-points on every deploy — and {root}/app, the directory
// holding it, is site-owned by design (prepareDeployerLayout). Anchoring the
// prefix at the document root would put `current` inside the unchecked region
// and let the site aim its own vhost at any path on the host: nginx's worker
// runs as www-data, so it would happily serve another site's tree. The prefix is
// therefore the release root, which makes `current` an ownership-checked
// component: a link the site owns pointing at a release the site owns passes,
// and one pointing at another account's tree is refused with EACCES.
//
// This is why prepareCurrentSymlink lchowns the link it seeds to the site
// account — a root-owned link over a site-owned release would fail the very
// check this prefix turns on.
func symlinkFrom(site Site) string {
	if deployerLayout(site) {
		return releaseRoot(site)
	}
	return documentRoot(site)
}

// defaultIndexFiles is the historical directory-index list. A zero Settings must
// render exactly these names in exactly this order.
var defaultIndexFiles = []string{"index.php", "index.html"}

// indexFilesOr resolves the configured list or the baseline. The returned slice
// is only read and joined, never sorted or mutated, so the rendered order is
// byte-for-byte the stored order.
func indexFilesOr(files []string) []string {
	if len(files) == 0 {
		return defaultIndexFiles
	}
	return files
}

// frontController picks the PHP entry point that `location /` falls back to: the
// first configured index whose name ends in .php, or index.php when the list has
// none. It follows the index list rather than staying pinned to index.php
// because there is no other way to serve a framework whose front controller is
// app.php — the per-site override drop-in is included *inside* the same server
// block, so a second `location /` there would be a duplicate-location error, not
// an override. Degrading to the literal index.php for a PHP-less list keeps the
// historical behaviour for a purely static index list.
func frontController(files []string) string {
	for _, name := range indexFilesOr(files) {
		if strings.HasSuffix(name, ".php") {
			return name
		}
	}
	return "index.php"
}

func effective(site Site, includesRoot string) effectiveSettings {
	s := site.Settings
	e := effectiveSettings{
		DocRoot:         documentRoot(site),
		SymlinkFrom:     symlinkFrom(site),
		IndexFiles:      strings.Join(indexFilesOr(s.IndexFiles), " "),
		FrontController: frontController(s.IndexFiles),
		Gzip:            boolOr(s.Gzip, true),
		GzipLevel:       zeroOr(s.GzipLevel, defaultGzipLevel),
		ClientMaxBodyMB: intOr(s.ClientMaxBodyMB, defaultClientMaxBodyMB),
		Charset:         s.Charset,
		AccessLog:       boolOr(s.AccessLog, true),
		ErrorLog:        boolOr(s.ErrorLog, true),
		HTTP2:           boolOr(s.HTTP2, true),
		HTTP3:           s.HTTP3,
		HTTPSRedirect:   boolOr(s.HTTPSRedirect, true),
		OverrideGlob:    filepath.Join(includesRoot, "nexa-"+site.Slug+".d", "*.conf"),
	}
	// A nil StaticCacheDays keeps the 30-day default; an explicit 0 turns the
	// expires header off entirely while still serving the static location.
	if s.StaticCacheDays == nil {
		e.StaticCache, e.ExpiresDays = true, defaultExpiresDays
	} else if *s.StaticCacheDays > 0 {
		e.StaticCache, e.ExpiresDays = true, *s.StaticCacheDays
	}
	if s.HSTS {
		e.HSTS = true
		e.HSTSHeaderValue = "max-age=" + strconv.Itoa(zeroOr(s.HSTSMaxAge, defaultHSTSMaxAge)) + "; includeSubDomains"
	}
	if s.BasicAuth.Enabled {
		e.BasicAuth = true
		e.BasicAuthRealm = s.BasicAuth.Realm
		if e.BasicAuthRealm == "" {
			e.BasicAuthRealm = basicAuthDefaultRealm
		}
		e.HtpasswdPath = filepath.Join(includesRoot, "nexa-"+site.Slug+".htpasswd")
	}
	if s.RateLimit.Enabled {
		e.RateLimit = true
		e.RateZone = rateZone(site.Slug)
		e.RateBurst = zeroOr(s.RateLimit.Burst, defaultRateBurst)
	}
	return e
}

// fpmDataFor computes the pool template inputs. The dynamic-mode knobs are
// derived from max_children so the UI need only expose the child cap; the
// derivation is pure, so it stays byte-stable across re-renders.
func fpmDataFor(site Site) any {
	f := site.Settings
	pm := f.PMMode
	if pm == "" {
		pm = "ondemand"
	}
	children := zeroOr(f.PMMaxChildren, defaultFPMMaxChildren)
	// An empty WorkingDirectory must not go through filepath.Join at all: the
	// baseline chdir is the site root path exactly as given. In deployer mode the
	// base is {root}/app/current instead, so a worker never starts in a directory
	// that release rotation has left behind; WorkingDirectory stays relative to
	// that base, which keeps it pointing inside the live release.
	base := site.RootPath
	if deployerLayout(site) {
		base = filepath.Join(releaseRoot(site), "current")
	}
	chdir := base
	if f.WorkingDirectory != "" {
		chdir = filepath.Join(base, f.WorkingDirectory)
	}
	start := children / 2
	if start < 1 {
		start = 1
	}
	// children is validated to 0 (→ default 3) or [1, 1024], so it is always at
	// least 1 here and min_spare of 1 never exceeds max_spare.
	maxSpare := children
	minSpare := 1
	return struct {
		Slug, UnixUser, SocketPath, RootPath, PM       string
		ChdirPath                                      string
		MaxChildren, MaxRequests                       int
		StartServers, MinSpareServers, MaxSpareServers int
		ErrorLog                                       bool
	}{
		Slug: site.Slug, UnixUser: site.UnixUser, SocketPath: site.SocketPath, RootPath: site.RootPath, ChdirPath: chdir,
		PM: pm, MaxChildren: children, MaxRequests: intOr(f.PMMaxRequests, defaultFPMMaxRequests),
		StartServers: start, MinSpareServers: minSpare, MaxSpareServers: maxSpare,
		ErrorLog: boolOr(f.ErrorLog, true),
	}
}

// rateZoneArtifact renders the http{}-scope limit_req_zone for the site. It must
// live in a conf.d file (not the vhost) because limit_req_zone is only valid at
// http scope; one zone per site keeps per-site rendering independent.
func rateZoneArtifact(site Site) string {
	rps := zeroOr(site.Settings.RateLimit.RequestsPerSecond, defaultRateReqPerSec)
	return "# Managed by Nexa Panel.\n" +
		"limit_req_zone $binary_remote_addr zone=" + rateZone(site.Slug) +
		":10m rate=" + strconv.Itoa(rps) + "r/s;\n"
}

// htpasswdArtifact renders the auth_basic_user_file body from the stored hash.
// The hash is validated (never plaintext) in validateSettings before we get here.
func htpasswdArtifact(site Site) string {
	return site.Settings.BasicAuth.Username + ":" + site.Settings.BasicAuth.PasswordHash + "\n"
}

// logrotateArtifact renders the per-site /etc/logrotate.d stanza. Like
// rateZoneArtifact and htpasswdArtifact it is plain string concatenation rather
// than a text/template: the file is only ever emitted when rotation is enabled,
// so there is no {{if}}/{{end}} whitespace to get wrong, and the byte output is
// provably a pure function of Settings.
//
// The three log paths are written from a fixed literal slice — never a map —
// so the rendered order is identical on every re-render, which is what
// validatePlan's byte-exact comparison requires.
//
// copytruncate is deliberate, and is the least-surprising correct form here:
//
//  1. The site's logs/ directory is 0770 nexa_<slug>:www-data. logrotate refuses
//     to rotate files under a directory that is group-writable and not
//     root-owned unless an `su` directive is present, so `su` is mandatory.
//  2. Under `su`, logrotate performs the rotation — including any postrotate
//     script — as the unprivileged site account. That account cannot read
//     /run/nginx.pid's target process nor signal Nginx's root-owned master, so
//     the conventional `kill -USR1` postrotate would fail every night and leave
//     Nginx appending to the renamed inode forever. A silently broken postrotate
//     on a shared node is exactly the hazard to avoid.
//  3. PHP-FPM's php_admin_value[error_log] is opened by the engine per worker
//     and is NOT reopened on SIGUSR1, so no signal can fix php-error.log at all.
//
// copytruncate sidesteps all three: every descriptor stays valid, nothing is
// signalled, and the distro's own /etc/logrotate.d/nginx is left alone. The cost
// is a narrow window in which lines written between the copy and the truncate
// can be lost — an acceptable trade for per-site request logs. `create` is
// intentionally omitted because logrotate ignores it under copytruncate.
// delaycompress defers compressing the freshest rotation by one cycle, so a slow
// writer that raced the truncate is never gzipped mid-append.
func logrotateArtifact(site Site) string {
	logs := filepath.Join(site.RootPath, "logs")
	var body strings.Builder
	body.WriteString("# Managed by Nexa Panel.\n")
	for _, name := range []string{"access.log", "error.log", "php-error.log"} {
		body.WriteString(filepath.Join(logs, name))
		body.WriteString("\n")
	}
	body.WriteString("{\n")
	body.WriteString("    " + logrotateFrequency(site.Settings.LogRotation.Frequency) + "\n")
	body.WriteString("    rotate " + strconv.Itoa(zeroOr(site.Settings.LogRotation.KeepFiles, defaultLogRotateKeep)) + "\n")
	body.WriteString("    missingok\n")
	body.WriteString("    notifempty\n")
	body.WriteString("    compress\n")
	body.WriteString("    delaycompress\n")
	body.WriteString("    copytruncate\n")
	body.WriteString("    su " + site.UnixUser + " www-data\n")
	body.WriteString("}\n")
	return body.String()
}

// logrotateFrequency resolves the rotation interval; "" keeps the daily default.
// validateSettings has already refused anything outside the accepted set, so the
// value is safe to emit into the stanza verbatim.
func logrotateFrequency(frequency string) string {
	if frequency == "" {
		return defaultLogRotateFrequency
	}
	return frequency
}

func renderNginx(site Site, includesRoot string) (string, error) {
	names := []string{site.PrimaryDomain}
	redirects := make([]Route, 0)
	for _, route := range site.Routes {
		if route.Kind == "redirect" {
			redirects = append(redirects, route)
		} else {
			names = append(names, route.Hostname)
		}
	}
	tlsNames := []string{site.PrimaryDomain}
	if len(site.TLSDomains) > 0 {
		tlsNames = append([]string(nil), site.TLSDomains...)
	}
	data := struct {
		Site      Site
		Names     string
		TLSNames  string
		Redirects []Route
		Eff       effectiveSettings
	}{Site: site, Names: strings.Join(names, " "), TLSNames: strings.Join(tlsNames, " "), Redirects: redirects, Eff: effective(site, includesRoot)}
	return executeData(nginxTemplate, data)
}

func executeData(source string, data any) (string, error) {
	tmpl, err := template.New("artifact").Option("missingkey=error").Parse(source)
	if err != nil {
		return "", fmt.Errorf("parse managed site template: %w", err)
	}
	var output bytes.Buffer
	if err := tmpl.Execute(&output, data); err != nil {
		return "", fmt.Errorf("render managed site template: %w", err)
	}
	return output.String(), nil
}

func execute(source string, site Site) (string, error) {
	tmpl, err := template.New("artifact").Option("missingkey=error").Parse(source)
	if err != nil {
		return "", fmt.Errorf("parse managed site template: %w", err)
	}
	var output bytes.Buffer
	if err := tmpl.Execute(&output, site); err != nil {
		return "", fmt.Errorf("render managed site template: %w", err)
	}
	return output.String(), nil
}
