package sitefs

import (
	"path/filepath"
	"testing"
)

func TestValidate(t *testing.T) {
	siteRoot := "/srv/nexa/sites"
	valid := Scope{SiteID: "site_1", Slug: "my-app", RootPath: filepath.Join(siteRoot, "my-app"), UnixUser: "nexa_my_app"}
	if err := Validate(valid, siteRoot); err != nil {
		t.Fatalf("valid scope rejected: %v", err)
	}
	cases := []struct {
		name  string
		scope Scope
	}{
		{"missing site id", Scope{Slug: "my-app", RootPath: filepath.Join(siteRoot, "my-app"), UnixUser: "nexa_my_app"}},
		{"uppercase slug", Scope{SiteID: "site_1", Slug: "My-App", RootPath: filepath.Join(siteRoot, "My-App"), UnixUser: "nexa_My_App"}},
		{"leading digit slug", Scope{SiteID: "site_1", Slug: "1app", RootPath: filepath.Join(siteRoot, "1app"), UnixUser: "nexa_1app"}},
		{"single character slug", Scope{SiteID: "site_1", Slug: "a", RootPath: filepath.Join(siteRoot, "a"), UnixUser: "nexa_a"}},
		{"traversal slug", Scope{SiteID: "site_1", Slug: "../etc", RootPath: filepath.Join(siteRoot, "../etc"), UnixUser: "nexa_.._etc"}},
		{"root path mismatch", Scope{SiteID: "site_1", Slug: "my-app", RootPath: "/etc", UnixUser: "nexa_my_app"}},
		{"root path outside layout", Scope{SiteID: "site_1", Slug: "my-app", RootPath: filepath.Join(siteRoot, "other"), UnixUser: "nexa_my_app"}},
		{"unix user mismatch", Scope{SiteID: "site_1", Slug: "my-app", RootPath: filepath.Join(siteRoot, "my-app"), UnixUser: "root"}},
		{"unix user without hyphen mapping", Scope{SiteID: "site_1", Slug: "my-app", RootPath: filepath.Join(siteRoot, "my-app"), UnixUser: "nexa_my-app"}},
	}
	for _, tc := range cases {
		if err := Validate(tc.scope, siteRoot); err == nil {
			t.Errorf("%s: scope %+v was accepted", tc.name, tc.scope)
		}
	}
}

func TestCleanRelative(t *testing.T) {
	valid := []struct {
		raw, want string
	}{
		{"", "."},
		{"/", "."},
		{".", "."},
		{"./", "."},
		{"a/b", "a/b"},
		{"./a", "a"},
		{"a/./b", "a/b"},
		{"a/../b", "b"},
		{"a//b", "a/b"},
		{"a/", "a"},
		{"público/ölçü.txt", "público/ölçü.txt"},
		{"tmp/.nexa-upload-abc", "tmp/.nexa-upload-abc"},
		{`back\slash`, `back\slash`},
	}
	for _, tc := range valid {
		got, err := CleanRelative(tc.raw)
		if err != nil || got != tc.want {
			t.Errorf("CleanRelative(%q) = %q, %v, want %q", tc.raw, got, err, tc.want)
		}
	}
	invalid := []string{
		"..",
		"../x",
		"a/../../b",
		"../../../../etc/passwd",
		"/etc/passwd",
		"/a",
		"a\x00b",
		"\x00",
	}
	for _, raw := range invalid {
		if got, err := CleanRelative(raw); err == nil {
			t.Errorf("CleanRelative(%q) = %q, want rejection", raw, got)
		}
	}
}
