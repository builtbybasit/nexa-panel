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
