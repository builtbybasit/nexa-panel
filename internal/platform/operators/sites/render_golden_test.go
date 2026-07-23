package sites

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"testing"
)

// The golden digests below were captured from the renderer as it stood BEFORE
// deployment modes existed. Every already-active site re-plans through
// validatePlan's byte-exact gate (activation.go), so a standard-mode render that
// differs from these bytes by so much as a newline breaks every site on every
// node. Treat a failure here as a bug in the change, never as a stale fixture:
// the only legitimate reason to edit a digest is a deliberate, separately
// reviewed change to the standard-mode output itself.
func TestStandardRenderIsByteIdenticalToPreDeployerBaseline(t *testing.T) {
	for _, tc := range []struct {
		name   string
		site   Site
		digest string
	}{
		{"baseline", baselineSite(), "50bd739c3bcbb0dab955e0264bc1406c257f5610408634eae90b240791d45ad7"},
		{"tls-and-routes", goldenRoutedSite(), "06e3ced94ea8fcf3066247185bc72463cbabbefee18c179ce33e745c00874b43"},
		{"every-setting", goldenTunedSite(), "cdb30e604176e38537d5446ea3777bf4643e6e2a220fd2e1ebdd899d2638e494"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rendered := goldenPlanText(t, tc.site)
			sum := sha256.Sum256([]byte(rendered))
			if got := hex.EncodeToString(sum[:]); got != tc.digest {
				t.Fatalf("standard-mode render drifted: digest = %s, want %s\n--- rendered ---\n%s", got, tc.digest, rendered)
			}
		})
	}
}

// The same three fixtures with the mode set explicitly to "standard" must render
// the same bytes as the empty mode existing rows carry, so the migration that
// backfills the column cannot itself change a single plan.
func TestExplicitStandardModeRendersLikeTheEmptyMode(t *testing.T) {
	for _, site := range []Site{baselineSite(), goldenRoutedSite(), goldenTunedSite()} {
		implicit := goldenPlanText(t, site)
		site.DeploymentMode = "standard"
		if explicit := goldenPlanText(t, site); explicit != implicit {
			t.Fatalf("explicit standard mode drifted from the empty mode for %q", site.Slug)
		}
	}
}

// Deployer mode moves the served root into the release tree and stops emitting
// the managed site-root index.php, which is what keeps a deploy from ever
// reading as drift on a managed path.
func TestDeployerModeServesTheReleaseTree(t *testing.T) {
	site := baselineSite()
	site.DeploymentMode = "deployer"
	plan := mustRender(t, site)
	if len(plan.Artifacts) != 2 {
		t.Fatalf("artifact count = %d, want 2 (no site-root artifact in deployer mode)", len(plan.Artifacts))
	}
	if _, ok := artifactOf(plan, "site-root"); ok {
		t.Fatal("deployer mode emitted a managed site-root artifact")
	}
	// The site-root path is not retired either: a mode switch must never delete
	// whatever the site served in standard mode.
	for _, path := range plan.Retired {
		if strings.Contains(path, "/srv/nexa/sites/demo-site/") {
			t.Fatalf("deployer mode retired a site path: %s", path)
		}
	}
	nginx := nginxOf(plan)
	for _, want := range []string{
		"root /srv/nexa/sites/demo-site/app/current/public;",
		"disable_symlinks if_not_owner from=/srv/nexa/sites/demo-site/app;",
		"access_log /srv/nexa/sites/demo-site/logs/access.log;",
	} {
		if !strings.Contains(nginx, want) {
			t.Fatalf("deployer vhost missing %q\n%s", want, nginx)
		}
	}
	fpm, ok := artifactOf(plan, "php-fpm-pool")
	if !ok {
		t.Fatal("missing FPM artifact")
	}
	if !strings.Contains(fpm.Content, "chdir = /srv/nexa/sites/demo-site/app/current\n") {
		t.Fatalf("deployer FPM pool chdir is not the current release:\n%s", fpm.Content)
	}
}

// Both per-site path knobs stay relative to their deployer-mode bases, so a
// tuned site keeps working after the switch.
func TestDeployerModeKeepsSubdirectoryAndWorkingDirectoryRelative(t *testing.T) {
	site := baselineSite()
	site.DeploymentMode = "deployer"
	site.Settings.Subdirectory = "web"
	site.Settings.WorkingDirectory = "private"
	plan := mustRender(t, site)
	if !strings.Contains(nginxOf(plan), "root /srv/nexa/sites/demo-site/app/current/public/web;") {
		t.Fatalf("subdirectory is not resolved under the current release:\n%s", nginxOf(plan))
	}
	fpm, _ := artifactOf(plan, "php-fpm-pool")
	if !strings.Contains(fpm.Content, "chdir = /srv/nexa/sites/demo-site/app/current/private\n") {
		t.Fatalf("working directory is not resolved under the current release:\n%s", fpm.Content)
	}
}

func TestRendererRejectsUnknownDeploymentMode(t *testing.T) {
	site := baselineSite()
	site.DeploymentMode = "Deployer"
	if _, err := (Renderer{}).Render(site); err == nil {
		t.Fatal("expected an unknown deployment mode to be rejected")
	}
}

// goldenPlanText serialises everything Apply compares byte-for-byte: the ordered
// artifact list with its kinds, paths, and modes, plus the retired paths.
func goldenPlanText(t *testing.T, site Site) string {
	t.Helper()
	plan := mustRender(t, site)
	var b strings.Builder
	for _, artifact := range plan.Artifacts {
		b.WriteString("### " + artifact.Kind + " " + artifact.Path + " " + strconv.FormatUint(uint64(artifact.Mode), 8) + "\n")
		b.WriteString(artifact.Content)
	}
	for _, path := range plan.Retired {
		b.WriteString("### retired " + path + "\n")
	}
	return b.String()
}

func goldenRoutedSite() Site {
	site := baselineSite()
	site.Routes = []Route{
		{Hostname: "alias.example.com", Kind: "alias"},
		{Hostname: "app.demo.example.com", Kind: "subdomain"},
		{Hostname: "old.example.com", Kind: "redirect", RedirectTarget: "demo.example.com"},
	}
	site.TLS = &TLS{CertificatePath: "/etc/letsencrypt/live/demo.example.com/fullchain.pem", PrivateKeyPath: "/etc/letsencrypt/live/demo.example.com/privkey.pem"}
	site.TLSDomains = []string{"demo.example.com", "alias.example.com", "app.demo.example.com"}
	return site
}

func goldenTunedSite() Site {
	site := goldenRoutedSite()
	site.Settings = Settings{
		HSTS: true, HSTSMaxAge: 63072000, HTTP2: ptrBool(false), HTTP3: true, HTTPSRedirect: ptrBool(false),
		Subdirectory: "web", Charset: "utf-8", Gzip: ptrBool(true), GzipLevel: 9,
		StaticCacheDays: ptrInt(7), ClientMaxBodyMB: ptrInt(128),
		PMMode: "dynamic", PMMaxChildren: 12, PMMaxRequests: ptrInt(1000),
		AccessLog: ptrBool(false), ErrorLog: ptrBool(true),
		BasicAuth:  BasicAuth{Enabled: true, Realm: "Staging", Username: "deployer", PasswordHash: "$2y$10$abcdefghijklmnopqrstuv"},
		RateLimit:  RateLimit{Enabled: true, RequestsPerSecond: 25, Burst: 50},
		IndexFiles: []string{"app.php", "index.html"}, WorkingDirectory: "private",
		LogRotation: LogRotation{Enabled: true, KeepFiles: 30, Frequency: "weekly"},
	}
	return site
}

// nginx only ownership-checks the path components that come *after* the
// disable_symlinks from= prefix. In deployer mode the document root contains
// `current` — a symlink the site account owns and re-points, in a directory
// ({root}/app) the site account owns by design — so anchoring the prefix at the
// document root would exempt exactly the one link a site can control, letting it
// aim its own vhost (served by the shared www-data worker) at another site's
// tree. The prefix is therefore the release root, which puts `current` inside
// the checked region.
func TestTheDeployerCurrentLinkIsOwnershipChecked(t *testing.T) {
	site := baselineSite()
	site.DeploymentMode = "deployer"
	site.Settings.Subdirectory = "web"
	nginx := nginxOf(mustRender(t, site))

	const prefix = "disable_symlinks if_not_owner from=/srv/nexa/sites/demo-site/app;"
	if !strings.Contains(nginx, prefix) {
		t.Fatalf("deployer vhost does not ownership-check the current link:\n%s", nginx)
	}
	if !strings.Contains(nginx, "root /srv/nexa/sites/demo-site/app/current/public/web;") {
		t.Fatalf("deployer vhost served the wrong root:\n%s", nginx)
	}
	if strings.Contains(nginx, "from=/srv/nexa/sites/demo-site/app/current") {
		t.Fatalf("the from= prefix still swallows the current link:\n%s", nginx)
	}
}

// Standard mode is untouched by that change: {root} is root-owned, so a site can
// only plant links below public/, and those are already after the prefix.
func TestStandardModeAnchorsTheSymlinkPrefixAtTheDocumentRoot(t *testing.T) {
	site := baselineSite()
	site.Settings.Subdirectory = "web"
	nginx := nginxOf(mustRender(t, site))
	if !strings.Contains(nginx, "disable_symlinks if_not_owner from=/srv/nexa/sites/demo-site/public/web;") {
		t.Fatalf("standard-mode prefix drifted:\n%s", nginx)
	}
}
