package sites

import (
	"strings"
	"testing"
)

func TestRendererBuildsConfinedPerSiteArtifacts(t *testing.T) {
	site := Site{
		ID: "site-1", Slug: "demo-site", PrimaryDomain: "demo.example.com", PHPVersion: "8.4",
		UnixUser: "nexa_demo_site", RootPath: "/srv/nexa/sites/demo-site", SocketPath: "/run/php/nexa-demo-site.sock",
	}
	plan, err := (Renderer{}).Render(site)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Artifacts) != 3 {
		t.Fatalf("artifact count = %d", len(plan.Artifacts))
	}
	nginx := plan.Artifacts[2]
	if nginx.Path != "/etc/nginx/sites-available/nexa-demo-site.conf" || !strings.Contains(nginx.Content, "fastcgi_pass unix:/run/php/nexa-demo-site.sock;") {
		t.Fatalf("unexpected nginx artifact: %+v", nginx)
	}
	if !strings.Contains(plan.Artifacts[1].Content, "pm = ondemand") || !strings.Contains(plan.Artifacts[1].Content, "user = nexa_demo_site") {
		t.Fatalf("unexpected FPM artifact: %s", plan.Artifacts[1].Content)
	}
}

func ptrInt(v int) *int    { return &v }
func ptrBool(v bool) *bool { return &v }

func baselineSite() Site {
	return Site{
		ID: "site-1", Slug: "demo-site", PrimaryDomain: "demo.example.com", PHPVersion: "8.4",
		UnixUser: "nexa_demo_site", RootPath: "/srv/nexa/sites/demo-site", SocketPath: "/run/php/nexa-demo-site.sock",
	}
}

func mustRender(t *testing.T, site Site) Plan {
	t.Helper()
	plan, err := (Renderer{}).Render(site)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	return plan
}

func nginxOf(plan Plan) string {
	for _, a := range plan.Artifacts {
		if a.Kind == "nginx-site" {
			return a.Content
		}
	}
	return ""
}

func artifactOf(plan Plan, kind string) (Artifact, bool) {
	for _, a := range plan.Artifacts {
		if a.Kind == kind {
			return a, true
		}
	}
	return Artifact{}, false
}

// The zero-value Settings must render the hardened baseline: gzip, static-asset
// caching, symlink hardening, an upload cap, security headers, HTTP/2 on the TLS
// block, and a user-override include — all with exactly three artifacts.
func TestRendererBakesHardenedDefaults(t *testing.T) {
	site := baselineSite()
	site.TLS = &TLS{CertificatePath: "/etc/letsencrypt/live/demo.example.com/fullchain.pem", PrivateKeyPath: "/etc/letsencrypt/live/demo.example.com/privkey.pem"}
	site.TLSDomains = []string{"demo.example.com"}
	plan := mustRender(t, site)
	if len(plan.Artifacts) != 3 {
		t.Fatalf("artifact count = %d, want 3 (no conditional artifacts by default)", len(plan.Artifacts))
	}
	nginx := nginxOf(plan)
	for _, want := range []string{
		"disable_symlinks if_not_owner from=/srv/nexa/sites/demo-site/public;",
		"client_max_body_size 64m;",
		"add_header X-Content-Type-Options nosniff always;",
		"add_header X-Frame-Options SAMEORIGIN always;",
		"add_header Referrer-Policy strict-origin-when-cross-origin always;",
		"gzip on;",
		"gzip_comp_level 5;",
		"expires 30d;",
		"listen 443 ssl http2;",
		"include /etc/nginx/nexa-includes/nexa-demo-site.d/*.conf;",
		"auth_basic off;", // acme challenge is never gated
		"location / { return 301 https://demo.example.com$request_uri; }",
	} {
		if !strings.Contains(nginx, want) {
			t.Fatalf("hardened default missing %q:\n%s", want, nginx)
		}
	}
	// HSTS, HTTP/3, rate-limit, basic-auth, and the charset directive must stay
	// off unless opted in.
	for _, absent := range []string{"Strict-Transport-Security", "listen 443 quic;", "limit_req", "auth_basic \"", "charset "} {
		if strings.Contains(nginx, absent) {
			t.Fatalf("baseline unexpectedly contains %q:\n%s", absent, nginx)
		}
	}
}

// The byte-exact re-render gate (validatePlan) depends on Render being a pure
// function of Settings: rendering the same site twice must be identical.
func TestRendererIsDeterministic(t *testing.T) {
	site := baselineSite()
	body := 128
	site.Settings = Settings{
		HSTS: true, HTTP3: true, GzipLevel: 7, ClientMaxBodyMB: &body, Charset: "utf-8",
		IndexFiles: []string{"app.php", "index.html"}, WorkingDirectory: "app/current",
		RateLimit:   RateLimit{Enabled: true, RequestsPerSecond: 25, Burst: 40},
		LogRotation: LogRotation{Enabled: true, KeepFiles: 9, Frequency: "hourly"},
	}
	first := mustRender(t, site)
	second := mustRender(t, site)
	if len(first.Artifacts) != len(second.Artifacts) {
		t.Fatalf("artifact count drifted: %d vs %d", len(first.Artifacts), len(second.Artifacts))
	}
	for i := range first.Artifacts {
		if first.Artifacts[i] != second.Artifacts[i] {
			t.Fatalf("artifact %d not deterministic:\n%q\nvs\n%q", i, first.Artifacts[i].Content, second.Artifacts[i].Content)
		}
	}
}

func TestRendererSettingsTogglesChangeExactDirectives(t *testing.T) {
	tls := &TLS{CertificatePath: "/etc/letsencrypt/live/demo.example.com/fullchain.pem", PrivateKeyPath: "/etc/letsencrypt/live/demo.example.com/privkey.pem"}
	cases := []struct {
		name     string
		mutate   func(*Settings)
		contains []string
		absent   []string
	}{
		{"hsts+http3", func(s *Settings) { s.HSTS = true; s.HTTP3 = true }, []string{
			`add_header Strict-Transport-Security "max-age=15552000; includeSubDomains" always;`,
			"listen 443 quic;", "http3 on;", `add_header Alt-Svc 'h3=":443"; ma=86400' always;`,
		}, nil},
		{"http2 off", func(s *Settings) { s.HTTP2 = ptrBool(false) }, []string{"listen 443 ssl;"}, []string{"http2"}},
		{"https redirect off", func(s *Settings) { s.HTTPSRedirect = ptrBool(false) }, []string{"try_files $uri $uri/ /index.php?$query_string;"}, []string{"return 301 https://"}},
		{"gzip off", func(s *Settings) { s.Gzip = ptrBool(false) }, nil, []string{"gzip on;"}},
		{"static cache disabled", func(s *Settings) { s.StaticCacheDays = ptrInt(0) }, nil, []string{"expires "}},
		{
			"custom expiry+body", func(s *Settings) { s.StaticCacheDays = ptrInt(7); s.ClientMaxBodyMB = ptrInt(256) },
			[]string{"expires 7d;", "client_max_body_size 256m;"},
			nil,
		},
		{"access log off", func(s *Settings) { s.AccessLog = ptrBool(false) }, []string{"access_log off;"}, nil},
		{"charset", func(s *Settings) { s.Charset = "windows-1251" }, []string{"    charset windows-1251;"}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			site := baselineSite()
			site.TLS = tls
			site.TLSDomains = []string{"demo.example.com"}
			tc.mutate(&site.Settings)
			nginx := nginxOf(mustRender(t, site))
			for _, want := range tc.contains {
				if !strings.Contains(nginx, want) {
					t.Fatalf("missing %q:\n%s", want, nginx)
				}
			}
			for _, absent := range tc.absent {
				if strings.Contains(nginx, absent) {
					t.Fatalf("unexpected %q:\n%s", absent, nginx)
				}
			}
		})
	}
}

// The subdirectory override moves the served root beneath public/ — and must
// never be able to climb out of it into the site's logs/, private/, tmp/, or
// backups/ siblings, which is why it is public-relative rather than root-relative.
func TestRendererSubdirectoryMovesDocumentRoot(t *testing.T) {
	site := baselineSite()
	site.Settings.Subdirectory = "app/public"
	nginx := nginxOf(mustRender(t, site))
	for _, want := range []string{
		"root /srv/nexa/sites/demo-site/public/app/public;",
		"disable_symlinks if_not_owner from=/srv/nexa/sites/demo-site/public/app/public;",
	} {
		if !strings.Contains(nginx, want) {
			t.Fatalf("missing %q:\n%s", want, nginx)
		}
	}

	// The default still serves public/ itself, byte-for-byte as before.
	plain := nginxOf(mustRender(t, baselineSite()))
	if !strings.Contains(plain, "root /srv/nexa/sites/demo-site/public;") {
		t.Fatalf("default document root changed:\n%s", plain)
	}
}

func TestRendererRejectsUnsafeSubdirectories(t *testing.T) {
	for name, value := range map[string]string{
		"traversal":        "../private",
		"nested traversal": "app/../../logs",
		"absolute":         "/etc",
		"trailing slash":   "app/",
		"double slash":     "app//public",
		"dot segment":      "./app",
		"backslash":        `app\public`,
		"colon":            "app:public",
		"too deep":         "a/b/c/d/e/f/g/h/i",
		"space":            "my app",
	} {
		t.Run(name, func(t *testing.T) {
			site := baselineSite()
			site.Settings.Subdirectory = value
			if _, err := (Renderer{}).Render(site); err == nil {
				t.Fatalf("subdirectory %q must be rejected", value)
			}
		})
	}
	// Ordinary nested paths remain usable.
	for _, value := range []string{"public", "app/public", "dist", "web-root_2.0"} {
		site := baselineSite()
		site.Settings.Subdirectory = value
		if _, err := (Renderer{}).Render(site); err != nil {
			t.Fatalf("subdirectory %q should be accepted: %v", value, err)
		}
	}
}

// The charset directive is strictly opt-in. An empty value must emit nothing at
// all: validatePlan re-renders every already-active site and demands byte-exact
// artifact equality, so a stray `charset utf-8;` — or even a stray blank line —
// would fail activation on every site that has never touched this setting.
func TestRendererCharsetIsOptInAndByteExactWhenUnset(t *testing.T) {
	base := nginxOf(mustRender(t, baselineSite()))
	if strings.Contains(base, "charset") {
		t.Fatalf("baseline vhost must emit no charset directive:\n%s", base)
	}

	// An explicitly-empty value is indistinguishable from an absent one.
	empty := baselineSite()
	empty.Settings.Charset = ""
	if got := nginxOf(mustRender(t, empty)); got != base {
		t.Fatalf("an empty charset changed the rendered vhost:\n%s\nvs\n%s", got, base)
	}

	// A set value adds exactly one line and changes nothing else.
	utf8 := baselineSite()
	utf8.Settings.Charset = "utf-8"
	rendered := nginxOf(mustRender(t, utf8))
	if !strings.Contains(rendered, "\n    charset utf-8;\n") {
		t.Fatalf("charset directive missing or misindented:\n%s", rendered)
	}
	if strings.Replace(rendered, "    charset utf-8;\n", "", 1) != base {
		t.Fatalf("charset changed more than its own line:\n%s", rendered)
	}

	// "off" is a legal nginx value and must survive the allowlist.
	off := baselineSite()
	off.Settings.Charset = "off"
	if !strings.Contains(nginxOf(mustRender(t, off)), "    charset off;\n") {
		t.Fatal("charset off must be renderable")
	}

	// Anything outside the allowlist — including injection attempts and values
	// that differ only in case or whitespace — is refused at plan time.
	for _, bad := range []string{"utf8", "UTF-8", " utf-8", "utf-8;", "utf-8; return 301 http://evil.example.com", "latin1", "utf-8\n    root /etc;", "on"} {
		site := baselineSite()
		site.Settings.Charset = bad
		if _, err := (Renderer{}).Render(site); err == nil {
			t.Fatalf("charset %q must be rejected", bad)
		}
	}
}

// The forced HTTP->HTTPS 301 is the default and stays byte-identical whether it
// arrives as an absent setting or an explicit true — that equivalence is what
// keeps validatePlan green for every site that predates this toggle. Turning it
// off makes port 80 serve the site instead, and the ACME challenge location must
// survive both modes so certificate renewal never depends on the setting.
func TestRendererHTTPSRedirectToggle(t *testing.T) {
	tls := &TLS{CertificatePath: "/etc/letsencrypt/live/demo.example.com/fullchain.pem", PrivateKeyPath: "/etc/letsencrypt/live/demo.example.com/privkey.pem"}

	on := baselineSite()
	on.TLS, on.TLSDomains = tls, []string{"demo.example.com"}
	onNginx := nginxOf(mustRender(t, on))
	if !strings.Contains(onNginx, "    location / { return 301 https://demo.example.com$request_uri; }") {
		t.Fatalf("an untuned TLS site must keep the forced HTTPS redirect:\n%s", onNginx)
	}

	explicit := on
	explicit.Settings.HTTPSRedirect = ptrBool(true)
	if nginxOf(mustRender(t, explicit)) != onNginx {
		t.Fatal("an explicit httpsRedirect=true must render byte-identically to the untuned default")
	}

	off := on
	off.Settings.HTTPSRedirect = ptrBool(false)
	offNginx := nginxOf(mustRender(t, off))
	if strings.Contains(offNginx, "return 301 https://demo.example.com$request_uri;") {
		t.Fatalf("the forced redirect must be gone when disabled:\n%s", offNginx)
	}
	// Port 80 now serves the same vhost body as 443.
	if got := strings.Count(offNginx, "fastcgi_pass unix:/run/php/nexa-demo-site.sock;"); got != 2 {
		t.Fatalf("vhost body should serve on both the HTTP and TLS blocks, got %d:\n%s", got, offNginx)
	}
	// Certificate issuance/renewal must not depend on the toggle.
	if got := strings.Count(offNginx, "location ^~ /.well-known/acme-challenge/ { root /srv/nexa/acme; auth_basic off; }"); got != 1 {
		t.Fatalf("ACME challenge must stay reachable over plain HTTP, got %d occurrences:\n%s", got, offNginx)
	}
	// VerifyHost probes over plain HTTP and identifies the block by header.
	if got := strings.Count(offNginx, "add_header X-Nexa-Site demo-site always;"); got != 2 {
		t.Fatalf("both server blocks must keep the verification header, got %d:\n%s", got, offNginx)
	}

	// Without TLS the setting is inert: it must not perturb the rendered vhost.
	plainOff := baselineSite()
	plainOff.Settings.HTTPSRedirect = ptrBool(false)
	if nginxOf(mustRender(t, plainOff)) != nginxOf(mustRender(t, baselineSite())) {
		t.Fatal("httpsRedirect must be inert for a site without TLS")
	}
}

// The index directive and the FPM chdir are configurable, but their zero value
// must reproduce the historical directives byte-for-byte — validatePlan
// re-renders every active site and demands exact equality.
func TestRendererIndexFilesAndWorkingDirectoryDefaultToTheBaseline(t *testing.T) {
	base := mustRender(t, baselineSite())
	if !strings.Contains(nginxOf(base), "    index index.php index.html;\n") {
		t.Fatalf("baseline index directive changed:\n%s", nginxOf(base))
	}
	if !strings.Contains(nginxOf(base), "        try_files $uri $uri/ /index.php?$query_string;\n") {
		t.Fatalf("baseline front-controller fallback changed:\n%s", nginxOf(base))
	}
	if !strings.Contains(nginxlessFPM(t, baselineSite()), "chdir = /srv/nexa/sites/demo-site\n") {
		t.Fatalf("baseline chdir changed:\n%s", nginxlessFPM(t, baselineSite()))
	}

	// An explicit list equal to the default — which is exactly what the UI posts
	// for a never-tuned site — must be byte-identical to the zero value.
	explicit := baselineSite()
	explicit.Settings.IndexFiles = []string{"index.php", "index.html"}
	if nginxOf(mustRender(t, explicit)) != nginxOf(base) {
		t.Fatal("an explicitly-submitted default index list must render identically to the zero value")
	}
	pool := baselineSite()
	pool.Settings.WorkingDirectory = ""
	if nginxlessFPM(t, pool) != nginxlessFPM(t, baselineSite()) {
		t.Fatal("an empty working subdirectory must render identically to the zero value")
	}
}

func TestRendererIndexFilesDriveTheDirectiveAndFrontController(t *testing.T) {
	custom := baselineSite()
	custom.Settings.IndexFiles = []string{"app.php", "index.html", "index.htm"}
	custom.Settings.WorkingDirectory = "public"
	nginx := nginxOf(mustRender(t, custom))
	if !strings.Contains(nginx, "    index app.php index.html index.htm;\n") {
		t.Fatalf("index order not preserved verbatim:\n%s", nginx)
	}
	if !strings.Contains(nginx, "try_files $uri $uri/ /app.php?$query_string;") {
		t.Fatalf("front controller did not follow the first .php entry:\n%s", nginx)
	}
	if strings.Contains(nginx, "index index.php index.html;") {
		t.Fatalf("a configured list must replace the baseline entirely:\n%s", nginx)
	}
	if !strings.Contains(nginxlessFPM(t, custom), "chdir = /srv/nexa/sites/demo-site/public\n") {
		t.Fatalf("chdir did not follow the working subdirectory:\n%s", nginxlessFPM(t, custom))
	}

	// A static-only list leaves the PHP fallback at the historical literal
	// rather than pointing `location /` at an HTML file.
	static := baselineSite()
	static.Settings.IndexFiles = []string{"index.html"}
	staticNginx := nginxOf(mustRender(t, static))
	if !strings.Contains(staticNginx, "    index index.html;\n") {
		t.Fatalf("static index list not rendered:\n%s", staticNginx)
	}
	if !strings.Contains(staticNginx, "try_files $uri $uri/ /index.php?$query_string;") {
		t.Fatalf("front controller must stay index.php when no .php index is configured:\n%s", staticNginx)
	}
}

func TestRendererRejectsUnsafeIndexFilesAndWorkingDirectories(t *testing.T) {
	for name, mutate := range map[string]func(*Settings){
		"index separator": func(s *Settings) { s.IndexFiles = []string{"app/index.php"} },
		"index traversal": func(s *Settings) { s.IndexFiles = []string{"../index.php"} },
		"index space":     func(s *Settings) { s.IndexFiles = []string{"my index.php"} },
		"index semicolon": func(s *Settings) { s.IndexFiles = []string{"index.php; root /etc"} },
		"index quote":     func(s *Settings) { s.IndexFiles = []string{`index".php`} },
		"index dotfile":   func(s *Settings) { s.IndexFiles = []string{".env"} },
		"index dot":       func(s *Settings) { s.IndexFiles = []string{".."} },
		"index empty":     func(s *Settings) { s.IndexFiles = []string{"index.php", ""} },
		"index duplicate": func(s *Settings) { s.IndexFiles = []string{"index.php", "index.php"} },
		"index too many": func(s *Settings) {
			s.IndexFiles = []string{"a", "b", "c", "d", "e", "f", "g", "h", "i"}
		},
		"chdir traversal": func(s *Settings) { s.WorkingDirectory = "../other-site" },
		"chdir nested up": func(s *Settings) { s.WorkingDirectory = "app/../../etc" },
		"chdir absolute":  func(s *Settings) { s.WorkingDirectory = "/etc" },
		"chdir trailing":  func(s *Settings) { s.WorkingDirectory = "app/" },
		"chdir backslash": func(s *Settings) { s.WorkingDirectory = `app\public` },
		"chdir too deep":  func(s *Settings) { s.WorkingDirectory = "a/b/c/d/e/f/g/h/i" },
	} {
		t.Run(name, func(t *testing.T) {
			site := baselineSite()
			mutate(&site.Settings)
			if _, err := (Renderer{}).Render(site); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
	// Ordinary values remain usable, including a chdir outside public/ — which
	// is the whole point of this field being site-root-relative.
	for _, dir := range []string{"public", "app/current", "private"} {
		site := baselineSite()
		site.Settings.WorkingDirectory = dir
		site.Settings.IndexFiles = []string{"index.php", "index.html", "default.php"}
		if _, err := (Renderer{}).Render(site); err != nil {
			t.Fatalf("working subdirectory %q should be accepted: %v", dir, err)
		}
	}
}

func TestRendererLogRotationEmitsLogrotateStanza(t *testing.T) {
	site := baselineSite()
	site.Settings.LogRotation = LogRotation{Enabled: true, KeepFiles: 7, Frequency: "weekly"}
	plan := mustRender(t, site)
	file, ok := artifactOf(plan, "logrotate")
	if !ok {
		t.Fatalf("expected logrotate artifact, got %d artifacts", len(plan.Artifacts))
	}
	// logrotate.d files are read by a root-run tool and must not be group- or
	// world-writable, so 0644 root-owned matches the ratelimit conf convention.
	if file.Path != "/etc/logrotate.d/nexa-demo-site" || file.Mode != 0o644 {
		t.Fatalf("unexpected logrotate artifact: %+v", file)
	}
	for _, want := range []string{
		"# Managed by Nexa Panel.\n",
		"/srv/nexa/sites/demo-site/logs/access.log\n",
		"/srv/nexa/sites/demo-site/logs/error.log\n",
		"/srv/nexa/sites/demo-site/logs/php-error.log\n",
		"    weekly\n",
		"    rotate 7\n",
		"    missingok\n",
		"    notifempty\n",
		"    compress\n",
		"    delaycompress\n",
		"    copytruncate\n",
		"    su nexa_demo_site www-data\n",
	} {
		if !strings.Contains(file.Content, want) {
			t.Fatalf("logrotate stanza missing %q:\n%s", want, file.Content)
		}
	}
	// A postrotate script would run as the unprivileged `su` account and could
	// not signal Nginx to reopen its logs; `create` is ignored under
	// copytruncate. Both must stay out of the stanza.
	for _, absent := range []string{"postrotate", "endscript", "create "} {
		if strings.Contains(file.Content, absent) {
			t.Fatalf("logrotate stanza must not contain %q:\n%s", absent, file.Content)
		}
	}
}

func TestRendererLogRotationDefaultsAndZeroValueAbsence(t *testing.T) {
	// The zero value must emit nothing at all — the byte-exact baseline.
	plan := mustRender(t, baselineSite())
	if _, ok := artifactOf(plan, "logrotate"); ok {
		t.Fatal("log rotation must not emit an artifact unless it is explicitly enabled")
	}
	if len(plan.Artifacts) != 3 {
		t.Fatalf("artifact count = %d, want the 3-artifact baseline", len(plan.Artifacts))
	}
	// Enabled with unset knobs falls back to daily / 14 stored files.
	site := baselineSite()
	site.Settings.LogRotation = LogRotation{Enabled: true}
	file, ok := artifactOf(mustRender(t, site), "logrotate")
	if !ok {
		t.Fatal("missing logrotate artifact")
	}
	if !strings.Contains(file.Content, "    daily\n") || !strings.Contains(file.Content, "    rotate 14\n") {
		t.Fatalf("unexpected log rotation defaults:\n%s", file.Content)
	}
}

func TestRendererLogrotateRootIsRedirectable(t *testing.T) {
	site := baselineSite()
	site.Settings.LogRotation = LogRotation{Enabled: true}
	plan, err := (Renderer{LogrotateRoot: "/opt/nexa-test/logrotate.d"}).Render(site)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	file, ok := artifactOf(plan, "logrotate")
	if !ok || file.Path != "/opt/nexa-test/logrotate.d/nexa-demo-site" {
		t.Fatalf("LogrotateRoot not honoured: %+v (ok=%v)", file, ok)
	}
}

func TestRendererRateLimitEmitsHTTPScopeZone(t *testing.T) {
	site := baselineSite()
	site.Settings.RateLimit = RateLimit{Enabled: true, RequestsPerSecond: 25, Burst: 40}
	plan := mustRender(t, site)
	zone, ok := artifactOf(plan, "nginx-ratelimit")
	if !ok {
		t.Fatalf("expected nginx-ratelimit artifact, got %d artifacts", len(plan.Artifacts))
	}
	if zone.Path != "/etc/nginx/conf.d/nexa-demo-site-ratelimit.conf" {
		t.Fatalf("unexpected zone path %q", zone.Path)
	}
	if !strings.Contains(zone.Content, "zone=nexa_demo_site:10m rate=25r/s;") {
		t.Fatalf("unexpected zone body: %s", zone.Content)
	}
	if !strings.Contains(nginxOf(plan), "limit_req zone=nexa_demo_site burst=40 nodelay;") {
		t.Fatalf("vhost missing limit_req usage:\n%s", nginxOf(plan))
	}
}

func TestRendererBasicAuthEmitsHtpasswdAndRejectsPlaintext(t *testing.T) {
	hashed := baselineSite()
	hashed.Settings.BasicAuth = BasicAuth{
		Enabled: true, Username: "deploy",
		PasswordHash: "$2y$10$abcdefghijklmnopqrstuv",
	}
	plan := mustRender(t, hashed)
	file, ok := artifactOf(plan, "nginx-htpasswd")
	if !ok || file.Mode != 0o640 {
		t.Fatalf("expected nginx-htpasswd artifact mode 0640, got %+v (ok=%v)", file, ok)
	}
	if file.Content != "deploy:$2y$10$abcdefghijklmnopqrstuv\n" {
		t.Fatalf("unexpected htpasswd body: %q", file.Content)
	}
	if !strings.Contains(nginxOf(plan), `auth_basic "Restricted";`) {
		t.Fatalf("vhost missing auth_basic directive:\n%s", nginxOf(plan))
	}

	plaintext := baselineSite()
	plaintext.Settings.BasicAuth = BasicAuth{Enabled: true, Username: "deploy", PasswordHash: "hunter2"}
	if _, err := (Renderer{}).Render(plaintext); err == nil {
		t.Fatal("expected rejection: plaintext must never reach auth_basic_user_file")
	}
}

func TestRendererFPMDynamicModeAndBaselineParity(t *testing.T) {
	// Zero-value pool is byte-identical to the historical ondemand pool.
	base := nginxlessFPM(t, baselineSite())
	for _, want := range []string{"pm = ondemand", "pm.max_children = 3", "pm.process_idle_timeout = 10s", "pm.max_requests = 500", "log_errors] = on"} {
		if !strings.Contains(base, want) {
			t.Fatalf("baseline pool missing %q:\n%s", want, base)
		}
	}
	dyn := baselineSite()
	dyn.Settings.PMMode = "dynamic"
	dyn.Settings.PMMaxChildren = 10
	dyn.Settings.ErrorLog = ptrBool(false)
	pool := nginxlessFPM(t, dyn)
	for _, want := range []string{"pm = dynamic", "pm.max_children = 10", "pm.start_servers = 5", "pm.min_spare_servers = 1", "pm.max_spare_servers = 10", "log_errors] = off"} {
		if !strings.Contains(pool, want) {
			t.Fatalf("dynamic pool missing %q:\n%s", want, pool)
		}
	}
	if strings.Contains(pool, "pm.process_idle_timeout") {
		t.Fatalf("dynamic pool must not carry the ondemand idle timeout:\n%s", pool)
	}
}

func nginxlessFPM(t *testing.T, site Site) string {
	t.Helper()
	plan := mustRender(t, site)
	fpm, ok := artifactOf(plan, "php-fpm-pool")
	if !ok {
		t.Fatal("missing php-fpm-pool artifact")
	}
	return fpm.Content
}

func TestRendererRejectsOutOfBoundSettings(t *testing.T) {
	for name, mutate := range map[string]func(*Settings){
		"gzip level high":   func(s *Settings) { s.GzipLevel = 10 },
		"expiry too long":   func(s *Settings) { s.StaticCacheDays = ptrInt(4000) },
		"body too large":    func(s *Settings) { s.ClientMaxBodyMB = ptrInt(9000) },
		"bad pm":            func(s *Settings) { s.PMMode = "aggressive" },
		"children zero-ish": func(s *Settings) { s.PMMaxChildren = -1 },
		"children too many": func(s *Settings) { s.PMMaxChildren = 5000 },
		"hsts age high":     func(s *Settings) { s.HSTSMaxAge = 99999999 },
		"bad realm":         func(s *Settings) { s.BasicAuth.Realm = "has\"quote" },
		"basic auth no user": func(s *Settings) {
			s.BasicAuth = BasicAuth{Enabled: true, PasswordHash: "$2y$10$abcdefghijklmnopqrstuv"}
		},
		"rate too fast": func(s *Settings) { s.RateLimit = RateLimit{Enabled: true, RequestsPerSecond: 20000} },
		"bad rotation frequency": func(s *Settings) {
			s.LogRotation = LogRotation{Enabled: true, Frequency: "monthly"}
		},
		"rotation frequency bad while disabled": func(s *Settings) {
			s.LogRotation = LogRotation{Frequency: "fortnightly"}
		},
		"rotation keep too high": func(s *Settings) { s.LogRotation = LogRotation{Enabled: true, KeepFiles: 400} },
		"rotation keep negative": func(s *Settings) { s.LogRotation = LogRotation{Enabled: true, KeepFiles: -1} },
	} {
		t.Run(name, func(t *testing.T) {
			site := baselineSite()
			mutate(&site.Settings)
			if _, err := (Renderer{}).Render(site); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestRendererRejectsClientControlledPathsAndPHP7(t *testing.T) {
	valid := Site{ID: "site-1", Slug: "demo-site", PrimaryDomain: "demo.example.com", PHPVersion: "8.4", UnixUser: "nexa_demo_site", RootPath: "/srv/nexa/sites/demo-site", SocketPath: "/run/php/nexa-demo-site.sock"}
	for name, mutate := range map[string]func(*Site){
		"root traversal": func(site *Site) { site.RootPath = "/etc" },
		"wrong owner":    func(site *Site) { site.UnixUser = "root" },
		"PHP 7.3":        func(site *Site) { site.PHPVersion = "7.3" },
	} {
		t.Run(name, func(t *testing.T) {
			site := valid
			mutate(&site)
			if _, err := (Renderer{}).Render(site); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

// A PHP branch published after this code was written must be usable by a site;
// the renderer enforces the floor, not a list of known branches.
func TestRendererAcceptsAnyPHPBranchAtOrAboveTheFloor(t *testing.T) {
	for _, version := range []string{"7.4", "8.0", "8.5", "8.10", "9.0", "10.2"} {
		site := Site{ID: "site-1", Slug: "demo-site", PrimaryDomain: "demo.example.com", PHPVersion: version, UnixUser: "nexa_demo_site", RootPath: "/srv/nexa/sites/demo-site", SocketPath: "/run/php/nexa-demo-site.sock"}
		if _, err := (Renderer{}).Render(site); err != nil {
			t.Fatalf("PHP %s should be renderable: %v", version, err)
		}
	}
	for _, version := range []string{"7.3", "5.6", "8", "8.3.1", "8.x", "8.3; rm -rf /", ""} {
		site := Site{ID: "site-1", Slug: "demo-site", PrimaryDomain: "demo.example.com", PHPVersion: version, UnixUser: "nexa_demo_site", RootPath: "/srv/nexa/sites/demo-site", SocketPath: "/run/php/nexa-demo-site.sock"}
		if _, err := (Renderer{}).Render(site); err == nil {
			t.Fatalf("PHP %q should be rejected below the %s floor or as malformed", version, phpFloor)
		}
	}
}

func TestRendererAllowsLegacyPHP74(t *testing.T) {
	site := Site{ID: "site-1", Slug: "legacy-site", PrimaryDomain: "legacy.example.com", PHPVersion: "7.4", UnixUser: "nexa_legacy_site", RootPath: "/srv/nexa/sites/legacy-site", SocketPath: "/run/php/nexa-legacy-site.sock"}
	if _, err := (Renderer{}).Render(site); err != nil {
		t.Fatalf("PHP 7.4 should remain renderable: %v", err)
	}
}

func TestRendererSeparatesHTTPRoutesFromCertificateSANs(t *testing.T) {
	site := Site{ID: "site-1", Slug: "demo-site", PrimaryDomain: "demo.example.com", PHPVersion: "8.4", UnixUser: "nexa_demo_site", RootPath: "/srv/nexa/sites/demo-site", SocketPath: "/run/php/nexa-demo-site.sock", Routes: []Route{{Hostname: "www.demo.example.com", Kind: "alias"}, {Hostname: "old.example.com", Kind: "redirect", RedirectTarget: "demo.example.com"}}, TLS: &TLS{CertificatePath: "/etc/letsencrypt/live/demo.example.com/fullchain.pem", PrivateKeyPath: "/etc/letsencrypt/live/demo.example.com/privkey.pem"}, TLSDomains: []string{"demo.example.com"}}
	plan, err := (Renderer{}).Render(site)
	if err != nil {
		t.Fatal(err)
	}
	nginx := plan.Artifacts[2].Content
	// The alias is served over HTTP but is not a certificate SAN, so only the
	// bare primary domain may appear on the TLS block's server_name.
	if !strings.Contains(nginx, "server_name demo.example.com www.demo.example.com;") || !strings.Contains(nginx, "server_name demo.example.com;") || !strings.Contains(nginx, "ssl_certificate /etc/letsencrypt/live/demo.example.com/fullchain.pem;") || !strings.Contains(nginx, "return 301 https://demo.example.com$request_uri;") {
		t.Fatalf("unexpected routing config:\n%s", nginx)
	}
}

// Verification identifies the serving block by header, so every block that
// answers for the site must carry it; otherwise a healthy site is rolled back.
func TestRendererLabelsServerBlocksWithTheSiteHeader(t *testing.T) {
	site := Site{ID: "site-1", Slug: "demo-site", PrimaryDomain: "demo.example.com", PHPVersion: "8.4", UnixUser: "nexa_demo_site", RootPath: "/srv/nexa/sites/demo-site", SocketPath: "/run/php/nexa-demo-site.sock", TLS: &TLS{CertificatePath: "/etc/letsencrypt/live/demo.example.com/fullchain.pem", PrivateKeyPath: "/etc/letsencrypt/live/demo.example.com/privkey.pem"}, TLSDomains: []string{"demo.example.com"}}
	plan, err := (Renderer{}).Render(site)
	if err != nil {
		t.Fatal(err)
	}
	nginx := plan.Artifacts[2].Content
	if got := strings.Count(nginx, "add_header X-Nexa-Site demo-site always;"); got != 2 {
		t.Fatalf("site header appears %d times, want it on both the HTTP and TLS blocks:\n%s", got, nginx)
	}
}

// A conditional artifact that is switched off must be named for removal, not
// merely omitted: Apply only writes the artifacts a plan carries, so without an
// explicit retirement a stanza written by an earlier activation would survive on
// the node. For log rotation that is not inert — the logs would keep rotating
// while the UI reports rotation off.
func TestRendererRetiresDisabledConditionalArtifacts(t *testing.T) {
	plan := mustRender(t, baselineSite())
	want := []string{
		"/etc/nginx/conf.d/nexa-demo-site-ratelimit.conf",
		"/etc/nginx/nexa-includes/nexa-demo-site.htpasswd",
		"/etc/logrotate.d/nexa-demo-site",
	}
	if len(plan.Retired) != len(want) {
		t.Fatalf("retired = %v, want all three conditional paths %v", plan.Retired, want)
	}
	for index, path := range want {
		if plan.Retired[index] != path {
			t.Fatalf("retired[%d] = %q, want %q (order must be fixed for byte-stable re-renders)", index, plan.Retired[index], path)
		}
	}

	// Enabling a setting moves its path out of Retired and into Artifacts; the
	// two lists must never both claim it.
	site := baselineSite()
	site.Settings.LogRotation = LogRotation{Enabled: true, KeepFiles: 7, Frequency: "weekly"}
	enabled := mustRender(t, site)
	for _, path := range enabled.Retired {
		if path == "/etc/logrotate.d/nexa-demo-site" {
			t.Fatalf("an enabled artifact is still listed for removal: %v", enabled.Retired)
		}
	}
	var found bool
	for _, artifact := range enabled.Artifacts {
		if artifact.Kind == "logrotate" && artifact.Path == "/etc/logrotate.d/nexa-demo-site" {
			found = true
		}
	}
	if !found {
		t.Fatalf("enabled logrotate artifact missing from %d artifacts", len(enabled.Artifacts))
	}
}
