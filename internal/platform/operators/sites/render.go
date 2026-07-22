package sites

import (
	"context"
	"errors"
	"path/filepath"
	"regexp"
	"slices"
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
	// htpasswdLine guards the basic-auth secret boundary: every line the operator
	// renders into auth_basic_user_file must be a hashed "user:$2y$..." entry, so
	// a plaintext password can never reach the node even if a caller mishandles it.
	htpasswdLine = regexp.MustCompile(`^[A-Za-z0-9._@-]+:\$(?:2[aby]|apr1|5|6)\$[./A-Za-z0-9$]+$`)
	// usernamePattern constrains the basic-auth account name to a shell/path-safe
	// set so it is safe both as an htpasswd field and in the rendered directive.
	usernamePattern = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)
	// subdirectorySegment allows one plain path component. "." and ".." match it
	// too, so relativePath rejects those separately.
	subdirectorySegment = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
	// indexFileName allows a single ordinary filename. The leading character may
	// not be a dot, which rules out "." and ".." and dotfiles in one stroke; no
	// slash, space, quote, or semicolon can appear, so an entry can never break
	// out of the rendered `index ...;` directive.
	indexFileName = regexp.MustCompile(`^[A-Za-z0-9_-][A-Za-z0-9._-]*$`)
)

// phpFloor is the oldest PHP branch Nexa serves.
const phpFloor = "7.4"

// maxIndexFiles caps the `index` list. Nginx tries them in order on every
// directory request, so an unbounded list is a per-request cost, not just a
// long line.
const maxIndexFiles = 8

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
	Settings      Settings `json:"settings,omitempty"`
}

// Settings carries the editable per-site Nginx and PHP-FPM knobs. Its zero value
// reproduces the hardened baseline byte-for-byte, so a site that has never been
// tuned — or a Plan caller that has not yet been wired to thread settings —
// renders identically, the same nil-safety Routes and TLS already enjoy. That
// property is what lets Settings ride through the byte-exact re-render gate in
// validatePlan without special-casing.
type Settings struct {
	// (a) HTTPS hardening
	HSTS       bool  `json:"hsts,omitempty"`       // false => off (hardened default)
	HSTSMaxAge int   `json:"hstsMaxAge,omitempty"` // 0 => 15552000 (180d); used only when HSTS
	HTTP2      *bool `json:"http2,omitempty"`      // nil => on
	HTTP3      bool  `json:"http3,omitempty"`      // false => off
	// HTTPSRedirect governs the forced HTTP->HTTPS 301 on the port-80 block. It
	// only has an effect when the site has TLS. nil => on, which reproduces the
	// historical unconditional redirect byte-for-byte; an explicit false makes
	// port 80 serve the same vhost body as 443. The ACME challenge location sits
	// above the branch and stays reachable over plain HTTP in both modes, so
	// certificate issuance and renewal are never affected by this toggle.
	HTTPSRedirect *bool `json:"httpsRedirect,omitempty"`

	// (b) Static & performance
	// Subdirectory selects the served document root, relative to the site's
	// public/ directory. "" serves public/ itself. It is deliberately NOT
	// relative to the site root: logs/, private/, tmp/, and backups/ are
	// siblings of public/, so a root-relative value could publish them.
	Subdirectory string `json:"subdirectory,omitempty"`
	// Charset, when set, emits an nginx `charset <value>;` directive in the shared
	// vhost body. "" emits nothing at all — nginx's own default is `charset off`,
	// so the empty value is both the historical behaviour and byte-identical to the
	// pre-settings artifact, which is what lets an already-active site re-render
	// through validatePlan unchanged. Only charsetAllowlist values are accepted, so
	// the token can never break out of the directive.
	Charset         string `json:"charset,omitempty"`
	Gzip            *bool  `json:"gzip,omitempty"`            // nil => on
	GzipLevel       int    `json:"gzipLevel,omitempty"`       // 0 => 5
	StaticCacheDays *int   `json:"staticCacheDays,omitempty"` // nil => 30; explicit 0 => no expires header
	ClientMaxBodyMB *int   `json:"clientMaxBodyMb,omitempty"` // nil => 64

	// (c) PHP-FPM & logs
	PMMode        string `json:"pmMode,omitempty"`        // "" => ondemand
	PMMaxChildren int    `json:"pmMaxChildren,omitempty"` // 0 => 3
	PMMaxRequests *int   `json:"pmMaxRequests,omitempty"` // nil => 500; explicit 0 => unlimited
	AccessLog     *bool  `json:"accessLog,omitempty"`     // nil => on (nginx access_log)
	ErrorLog      *bool  `json:"errorLog,omitempty"`      // nil => on

	// (d) Access control
	BasicAuth BasicAuth `json:"basicAuth,omitempty"`
	RateLimit RateLimit `json:"rateLimit,omitempty"`

	// (e) Application entry points
	// IndexFiles is the ordered nginx `index` list — the files tried when a
	// directory is requested. Empty renders the baseline "index.php index.html".
	// Names are plain filenames (validateIndexFiles refuses separators, traversal,
	// whitespace, quotes, and semicolons) so they cannot break out of the
	// directive, and they are emitted in exactly the order stored: this is a
	// slice, never a map, because a map's iteration order would break the
	// byte-exact re-render gate in validatePlan.
	IndexFiles []string `json:"indexFiles,omitempty"`
	// WorkingDirectory is the PHP-FPM `chdir` for this site's workers, relative to
	// the SITE ROOT. "" chdirs to the site root itself. It is deliberately NOT the
	// same axis as Subdirectory: Subdirectory is relative to public/ and selects
	// the document root Nginx *publishes*, which is why it may never escape
	// public/. This one publishes nothing — it only sets a worker's initial
	// working directory — so it is root-relative and may legitimately name
	// public/, app/, or private/. Both are validated by the same relativePath
	// helper, so neither can climb out of the site root.
	WorkingDirectory string `json:"workingDirectory,omitempty"`

	// (f) Log rotation
	LogRotation LogRotation `json:"logRotation,omitempty"`
}

type BasicAuth struct {
	Enabled  bool   `json:"enabled,omitempty"`
	Realm    string `json:"realm,omitempty"`    // "" => "Restricted"
	Username string `json:"username,omitempty"` // htpasswd account name
	// PasswordHash is a bcrypt/apr1 hash ("$2y$..."), never plaintext. It is a
	// hash, so it is safe to persist and re-emit byte-identically on every
	// re-plan; the control plane hashes the plaintext at the HTTP boundary and
	// only ever stores this. Blanked before the site is returned to a browser.
	PasswordHash string `json:"passwordHash,omitempty"`
}

type RateLimit struct {
	Enabled           bool `json:"enabled,omitempty"`
	RequestsPerSecond int  `json:"requestsPerSecond,omitempty"` // 0 => 10
	Burst             int  `json:"burst,omitempty"`             // 0 => 20
}

// LogRotation controls the optional per-site logrotate stanza. Its zero value is
// "off": no /etc/logrotate.d file is emitted and the plan keeps the historical
// three-artifact shape byte-for-byte.
type LogRotation struct {
	Enabled   bool   `json:"enabled,omitempty"`
	KeepFiles int    `json:"keepFiles,omitempty"` // 0 => 14; a 0 rotation count is never meaningful
	Frequency string `json:"frequency,omitempty"` // "" => daily; hourly | daily | weekly
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
	ID        string     `json:"id"`
	Kind      string     `json:"kind"`
	Site      Site       `json:"site"`
	Artifacts []Artifact `json:"artifacts"`
	Before    []Snapshot `json:"before"`
	// Retired lists managed paths this site must NOT have after the plan is
	// applied — the conditional artifacts whose setting is currently off. It is
	// derived purely from Settings by Render, and validatePlan re-derives and
	// compares it exactly, so a plan can only ever delete a path the renderer
	// itself owns for this slug. RetiredBefore holds their pre-apply snapshots so
	// a rollback can put back whatever the removal took away.
	Retired       []string   `json:"retired,omitempty"`
	RetiredBefore []Snapshot `json:"retiredBefore,omitempty"`
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
	// PlanTeardown issues the synthetic plan that strips a site from the node:
	// the same rendered artifacts, but with an empty before-state, so rolling it
	// back removes every managed file and disables the vhost.
	//
	// It exists as its own operator call because the plan is signed by the agent
	// and the signature covers the whole plan. The control plane used to build
	// this shape by mutating an already-signed activation plan, which invalidated
	// that signature and made every teardown fail with "The site plan was not
	// issued by this agent." The agent must therefore issue the exact plan it will
	// later be asked to execute.
	PlanTeardown(ctx context.Context, site Site) (Plan, error)
	Apply(ctx context.Context, plan Plan) (Observation, error)
	Rollback(ctx context.Context, plan Plan) (Observation, error)
}

type Renderer struct {
	NginxAvailableRoot string
	PHPConfigRoot      string
	SiteRoot           string
	SocketRoot         string
	NginxConfDRoot     string // default /etc/nginx/conf.d — holds the per-site limit_req_zone (http{} scope)
	NginxIncludesRoot  string // default /etc/nginx/nexa-includes — htpasswd file + user override drop-in dir
	LogrotateRoot      string // default /etc/logrotate.d — per-site rotation stanza, only written when log rotation is enabled
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
	confdRoot := r.NginxConfDRoot
	if confdRoot == "" {
		confdRoot = "/etc/nginx/conf.d"
	}
	includesRoot := r.NginxIncludesRoot
	if includesRoot == "" {
		includesRoot = "/etc/nginx/nexa-includes"
	}
	logrotateRoot := r.LogrotateRoot
	if logrotateRoot == "" {
		logrotateRoot = "/etc/logrotate.d"
	}

	nginx, err := renderNginx(site, includesRoot)
	if err != nil {
		return Plan{}, err
	}
	fpm, err := executeData(fpmTemplate, fpmDataFor(site))
	if err != nil {
		return Plan{}, err
	}
	index, err := execute(indexTemplate, site)
	if err != nil {
		return Plan{}, err
	}
	// The three core artifacts always render. The rate-limit zone, htpasswd file,
	// and logrotate stanza are conditional: each is appended when its setting is on
	// and named in Retired when it is off, both in a fixed order so re-renders are
	// deterministic. validatePlan matches artifacts by Kind, so a variable set is
	// safe — an all-zero Settings simply emits the three-artifact baseline.
	//
	// Retired is what makes turning a setting back off actually take effect on the
	// node. Apply only writes the artifacts a plan carries, so without an explicit
	// removal list a file written by an earlier activation would survive forever.
	// For the rate-limit zone and the htpasswd file the leftover is inert (an
	// unreferenced zone, an unreferenced password file), but a stale logrotate
	// stanza keeps rotating the site's logs while the UI reports rotation off.
	artifacts := []Artifact{
		{Kind: "site-root", Path: filepath.Join(site.RootPath, "public", "index.php"), Mode: 0o640, Content: index},
		{Kind: "php-fpm-pool", Path: filepath.Join(phpRoot, site.PHPVersion, "fpm", "pool.d", "nexa-"+site.Slug+".conf"), Mode: 0o640, Content: fpm},
		{Kind: "nginx-site", Path: filepath.Join(nginxRoot, "nexa-"+site.Slug+".conf"), Mode: 0o640, Content: nginx},
	}
	// Every conditional artifact is described once, so its path is identical
	// whether it is being written or retired and the two can never drift apart.
	conditionals := []struct {
		enabled  bool
		artifact Artifact
	}{
		{site.Settings.RateLimit.Enabled, Artifact{Kind: "nginx-ratelimit", Path: filepath.Join(confdRoot, "nexa-"+site.Slug+"-ratelimit.conf"), Mode: 0o644, Content: rateZoneArtifact(site)}},
		{site.Settings.BasicAuth.Enabled, Artifact{Kind: "nginx-htpasswd", Path: filepath.Join(includesRoot, "nexa-"+site.Slug+".htpasswd"), Mode: 0o640, Content: htpasswdArtifact(site)}},
		{site.Settings.LogRotation.Enabled, Artifact{Kind: "logrotate", Path: filepath.Join(logrotateRoot, "nexa-"+site.Slug), Mode: 0o644, Content: logrotateArtifact(site)}},
	}
	retired := make([]string, 0, len(conditionals))
	for _, conditional := range conditionals {
		if conditional.enabled {
			artifacts = append(artifacts, conditional.artifact)
			continue
		}
		retired = append(retired, conditional.artifact.Path)
	}
	return Plan{Site: site, Artifacts: artifacts, Retired: retired, Warnings: []string{"Activation requires PHP-FPM and Nginx validation on the managed node."}}, nil
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
	return validateSettings(site.Settings)
}

// ValidateSettings lets the control plane reject a malformed settings payload
// synchronously (HTTP 422) before it is ever persisted or enqueued, applying the
// exact same bounds the renderer enforces at plan time.
func ValidateSettings(s Settings) error { return validateSettings(s) }

// validateSettings bounds every tunable so a malformed value is rejected at plan
// time (and identically at re-render time, keeping validatePlan byte-exact). All
// checks are pure functions of Settings, so they never depend on node state.
func validateSettings(s Settings) error {
	if err := validateSubdirectory(s.Subdirectory); err != nil {
		return err
	}
	if err := validateIndexFiles(s.IndexFiles); err != nil {
		return err
	}
	if err := relativePath("working subdirectory", s.WorkingDirectory, "the site root"); err != nil {
		return err
	}
	if !charsetSupported(s.Charset) {
		return errors.New("character set must be empty or one of: " + strings.Join(charsetAllowlist, ", "))
	}
	if s.GzipLevel != 0 && (s.GzipLevel < 1 || s.GzipLevel > 9) {
		return errors.New("gzip level must be between 1 and 9")
	}
	if s.StaticCacheDays != nil && (*s.StaticCacheDays < 0 || *s.StaticCacheDays > 3650) {
		return errors.New("static cache expiry days must be between 0 and 3650")
	}
	if s.ClientMaxBodyMB != nil && (*s.ClientMaxBodyMB < 1 || *s.ClientMaxBodyMB > 2048) {
		return errors.New("client_max_body_size must be between 1 and 2048 MB")
	}
	if s.HSTSMaxAge != 0 && (s.HSTSMaxAge < 0 || s.HSTSMaxAge > 63072000) {
		return errors.New("HSTS max-age must be between 0 and 63072000 seconds")
	}
	switch s.PMMode {
	case "", "ondemand", "dynamic", "static":
	default:
		return errors.New("PHP-FPM process manager must be ondemand, dynamic, or static")
	}
	if s.PMMaxChildren != 0 && (s.PMMaxChildren < 1 || s.PMMaxChildren > 1024) {
		return errors.New("PHP-FPM max_children must be between 1 and 1024")
	}
	if s.PMMaxRequests != nil && (*s.PMMaxRequests < 0 || *s.PMMaxRequests > 100000) {
		return errors.New("PHP-FPM max_requests must be between 0 and 100000")
	}
	if !isPrintableRealm(s.BasicAuth.Realm) {
		return errors.New("basic-auth realm contains invalid characters")
	}
	if s.BasicAuth.Enabled {
		if !usernamePattern.MatchString(s.BasicAuth.Username) {
			return errors.New("basic-auth username must be 1-32 lowercase letters, digits, hyphen, or underscore")
		}
		if !htpasswdLine.MatchString(s.BasicAuth.Username + ":" + s.BasicAuth.PasswordHash) {
			return errors.New("basic-auth secret must be a hashed credential, never plaintext")
		}
	}
	if s.RateLimit.Enabled {
		if s.RateLimit.RequestsPerSecond != 0 && (s.RateLimit.RequestsPerSecond < 1 || s.RateLimit.RequestsPerSecond > 10000) {
			return errors.New("rate limit requests/sec must be between 1 and 10000")
		}
		if s.RateLimit.Burst != 0 && (s.RateLimit.Burst < 0 || s.RateLimit.Burst > 10000) {
			return errors.New("rate limit burst must be between 0 and 10000")
		}
	}
	// Bounds are checked unconditionally (not only when Enabled), matching the
	// PMMode/Realm precedent: a garbage value must never be persistable, and the
	// same check must hold at re-render time so validatePlan stays byte-exact.
	switch s.LogRotation.Frequency {
	case "", "hourly", "daily", "weekly":
	default:
		return errors.New("log rotation frequency must be hourly, daily, or weekly")
	}
	if s.LogRotation.KeepFiles != 0 && (s.LogRotation.KeepFiles < 1 || s.LogRotation.KeepFiles > 365) {
		return errors.New("log rotation must keep between 1 and 365 stored files")
	}
	return nil
}

// relativePath constrains a caller-supplied path to a clean relative path made
// of plain segments. Traversal, absolute paths, and non-canonical values are
// refused, so nothing validated here can climb out of the directory it is later
// joined onto. label names the field in the error message and relativeTo names
// its base, which is the only reason this is parameterised: one validator, one
// set of rules, several settings — a second, subtly-different anti-traversal
// check is exactly the drift this avoids.
func relativePath(label, value, relativeTo string) error {
	if value == "" {
		return nil
	}
	if len(value) > 128 {
		return errors.New(label + " must be 128 characters or fewer")
	}
	if strings.ContainsAny(value, `\:`) || strings.ContainsRune(value, 0) {
		return errors.New(label + " may not contain backslashes, colons, or null bytes")
	}
	if strings.HasPrefix(value, "/") {
		return errors.New(label + " must be relative to " + relativeTo)
	}
	// A canonical value is required so the rendered path is exactly what was
	// reviewed: no "a//b", no trailing slash, no "./" segments.
	if filepath.Clean(value) != value {
		return errors.New(label + " must be a canonical relative path without redundant or trailing separators")
	}
	segments := strings.Split(value, "/")
	if len(segments) > 8 {
		return errors.New(label + " may be at most 8 levels deep")
	}
	for _, segment := range segments {
		if segment == "." || segment == ".." {
			return errors.New(label + " may not contain relative path segments")
		}
		if !subdirectorySegment.MatchString(segment) {
			return errors.New(label + " segments may use only letters, digits, dot, hyphen, and underscore")
		}
	}
	return nil
}

// validateSubdirectory constrains the document-root override to a clean relative
// path beneath the site's public/ directory: the served root must never be able
// to climb into the site's logs/, private/, tmp/, or backups/ siblings, nor
// outside the site root entirely.
func validateSubdirectory(subdirectory string) error {
	return relativePath("subdirectory", subdirectory, "the site's public directory")
}

// validateIndexFiles bounds the `index` directive. Every entry must be a bare
// filename: no separator, no traversal, no whitespace, quote, or semicolon that
// could terminate the directive and inject configuration. The duplicate check
// uses a map for membership only — the map is never ranged over, so rendering
// stays a deterministic function of the stored slice order.
func validateIndexFiles(files []string) error {
	if len(files) == 0 {
		return nil
	}
	if len(files) > maxIndexFiles {
		return errors.New("application files may list at most " + strconv.Itoa(maxIndexFiles) + " filenames")
	}
	seen := make(map[string]struct{}, len(files))
	for _, name := range files {
		if name == "" {
			return errors.New("application file names may not be empty")
		}
		if len(name) > 64 {
			return errors.New("application file names must be 64 characters or fewer")
		}
		if !indexFileName.MatchString(name) {
			return errors.New("application file names must be plain filenames using letters, digits, dot, hyphen, and underscore, with no directory separators and no leading dot")
		}
		if _, exists := seen[name]; exists {
			return errors.New("application file names must be unique")
		}
		seen[name] = struct{}{}
	}
	return nil
}

// charsetAllowlist is the exact set of nginx `charset` tokens Nexa will emit,
// in the order the UI offers them. It is an ordered slice, never a map: the
// renderer must be a pure, deterministic function of Settings, and a slice is
// also what makes the error message and any future consumer stable. The empty
// string is deliberately NOT a member — "" means "emit no directive at all",
// which is the value that preserves the pre-settings baseline byte-for-byte.
var charsetAllowlist = []string{
	"off",
	"utf-8",
	"iso-8859-1",
	"iso-8859-2",
	"iso-8859-15",
	"windows-1251",
	"windows-1252",
	"koi8-r",
	"koi8-u",
	"big5",
	"euc-jp",
	"euc-kr",
	"gb2312",
	"shift_jis",
}

// charsetSupported accepts the empty default plus the allowlist verbatim. The
// comparison is exact — no case folding, no trimming — so the token rendered
// into the directive is always the canonical lowercase form and can never carry
// a space, semicolon, newline, or comment character into the vhost. The HTTP
// boundary (normalizeSettings) lowercases and trims before this runs, so a user
// typing "UTF-8" is still accepted end to end.
func charsetSupported(charset string) bool {
	return charset == "" || slices.Contains(charsetAllowlist, charset)
}

// isPrintableRealm rejects quotes, backslashes, and control characters so the
// realm string cannot break out of the auth_basic "..." directive.
func isPrintableRealm(realm string) bool {
	if len(realm) > 64 {
		return false
	}
	for _, c := range realm {
		if c < 0x20 || c > 0x7E || c == '"' || c == '\\' {
			return false
		}
	}
	return true
}

// nginxTemplate is the single vhost template used by both the hardened baseline
// and per-site settings. Every referenced field exists on the nginxData struct
// (.Site.*, .Names, .TLSNames, .Redirects, .Eff.*), so missingkey=error can
// never fire. The shared vhost body is defined once and included into whichever
// server block actually serves the site (HTTP when no TLS, HTTPS when TLS).
//
// HTTP/2 is enabled with the `listen ... http2` parameter rather than the newer
// standalone `http2 on;` directive, which reads as the modern form but is only
// understood from nginx 1.25.1. Ubuntu 24.04 — the platform scripts/install.sh
// targets — ships nginx 1.24.0, where `http2 on;` is an unknown directive: it
// fails `nginx -t`, so ValidateNginx rejects and every HTTPS activation rolls
// back. The listen parameter is accepted by both 1.24 and 1.25+ (newer builds
// only log a deprecation warning, which does not fail the config test), so it is
// the one portable spelling. Do NOT "modernise" this without raising the
// supported nginx floor, and do not branch on the node's version either: Render
// must stay a pure function of Settings or validatePlan's byte-exact re-render
// gate breaks.
const nginxTemplate = `# Managed by Nexa Panel.
{{- define "vhostBody"}}
    root {{.Eff.DocRoot}};
    index {{.Eff.IndexFiles}};
    disable_symlinks if_not_owner from={{.Eff.DocRoot}};
    client_max_body_size {{.Eff.ClientMaxBodyMB}}m;
    add_header X-Content-Type-Options nosniff always;
    add_header X-Frame-Options SAMEORIGIN always;
    add_header Referrer-Policy strict-origin-when-cross-origin always;
{{if .Eff.Charset}}    charset {{.Eff.Charset}};
{{end}}{{if .Eff.AccessLog}}    access_log {{.Site.RootPath}}/logs/access.log;
{{else}}    access_log off;
{{end}}{{if .Eff.ErrorLog}}    error_log {{.Site.RootPath}}/logs/error.log;
{{end}}{{if .Eff.Gzip}}    gzip on;
    gzip_vary on;
    gzip_proxied any;
    gzip_comp_level {{.Eff.GzipLevel}};
    gzip_min_length 256;
    gzip_types text/plain text/css application/json application/javascript text/xml application/xml application/xml+rss text/javascript image/svg+xml;
{{end}}{{if .Eff.BasicAuth}}    auth_basic "{{.Eff.BasicAuthRealm}}";
    auth_basic_user_file {{.Eff.HtpasswdPath}};
{{end}}{{if .Eff.RateLimit}}    limit_req zone={{.Eff.RateZone}} burst={{.Eff.RateBurst}} nodelay;
{{end}}    include {{.Eff.OverrideGlob}};

    location ~* \.(?:css|js|mjs|jpg|jpeg|png|gif|ico|svg|webp|avif|woff2?|ttf|eot|map)$ {
{{if .Eff.StaticCache}}        expires {{.Eff.ExpiresDays}}d;
{{end}}        access_log off;
        try_files $uri =404;
    }

    location / {
        try_files $uri $uri/ /{{.Eff.FrontController}}?$query_string;
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
{{- end}}
{{range .Redirects}}server {
    listen 80;
    listen [::]:80;
    server_name {{.Hostname}};
    location ^~ /.well-known/acme-challenge/ { root /srv/nexa/acme; auth_basic off; }
    location / { return 301 https://{{.RedirectTarget}}$request_uri; }
}
{{end}}server {
    listen 80;
    listen [::]:80;
    server_name {{.Names}};
    add_header X-Nexa-Site {{.Site.Slug}} always;
    location ^~ /.well-known/acme-challenge/ { root /srv/nexa/acme; auth_basic off; }
{{if and .Site.TLS .Eff.HTTPSRedirect}}    location / { return 301 https://{{.Site.PrimaryDomain}}$request_uri; }
{{else}}{{template "vhostBody" .}}
{{end}}}
{{if .Site.TLS}}
server {
    listen 443 ssl{{if .Eff.HTTP2}} http2{{end}};
    listen [::]:443 ssl{{if .Eff.HTTP2}} http2{{end}};
{{if .Eff.HTTP3}}    listen 443 quic;
    listen [::]:443 quic;
    http3 on;
{{end}}    server_name {{.TLSNames}};
    add_header X-Nexa-Site {{.Site.Slug}} always;
    ssl_certificate {{.Site.TLS.CertificatePath}};
    ssl_certificate_key {{.Site.TLS.PrivateKeyPath}};
    ssl_protocols TLSv1.2 TLSv1.3;
{{if .Eff.HSTS}}    add_header Strict-Transport-Security "{{.Eff.HSTSHeaderValue}}" always;
{{end}}{{if .Eff.HTTP3}}    add_header Alt-Svc 'h3=":443"; ma=86400' always;
{{end}}{{template "vhostBody" .}}
}
{{end}}
`

// fpmTemplate renders the per-site pool from a computed fpmData value. With an
// all-zero Settings it is byte-identical to the historical pool (ondemand, 3
// children, 500 requests, log_errors on). PHP-FPM access logging is deliberately
// not wired to the AccessLog toggle: nginx already records every request, so a
// second per-request log here would be redundant I/O — AccessLog governs the
// nginx access_log only.
const fpmTemplate = `; Managed by Nexa Panel.
[nexa-{{.Slug}}]
user = {{.UnixUser}}
group = {{.UnixUser}}
listen = {{.SocketPath}}
listen.owner = www-data
listen.group = www-data
listen.mode = 0660
pm = {{.PM}}
pm.max_children = {{.MaxChildren}}
{{if eq .PM "ondemand"}}pm.process_idle_timeout = 10s
{{end}}{{if eq .PM "dynamic"}}pm.start_servers = {{.StartServers}}
pm.min_spare_servers = {{.MinSpareServers}}
pm.max_spare_servers = {{.MaxSpareServers}}
{{end}}pm.max_requests = {{.MaxRequests}}
chdir = {{.ChdirPath}}
catch_workers_output = yes
clear_env = yes
security.limit_extensions = .php
php_admin_value[error_log] = {{.RootPath}}/logs/php-error.log
php_admin_flag[log_errors] = {{if .ErrorLog}}on{{else}}off{{end}}
`

const indexTemplate = `<?php
header('Content-Type: text/plain; charset=utf-8');
echo "Nexa Panel site {{.Slug}} is ready on PHP " . PHP_VERSION . "\n";
`
