package naming

import "testing"

func TestValidSiteSlug(t *testing.T) {
	valid := []string{"a1", "site", "my-site", "a" + string(make([]byte, 0)) + "bc"}
	for _, s := range valid {
		if !ValidSiteSlug(s) {
			t.Errorf("ValidSiteSlug(%q) = false, want true", s)
		}
	}
	invalid := []string{"", "A", "1site", "-site", "site_name", "x", "this-slug-is-far-too-long-to-be-accepted"}
	for _, s := range invalid {
		if ValidSiteSlug(s) {
			t.Errorf("ValidSiteSlug(%q) = true, want false", s)
		}
	}
}

func TestSiteUnixUser(t *testing.T) {
	cases := map[string]string{
		"site":    "nexa_site",
		"my-site": "nexa_my_site",
		"a-b-c":   "nexa_a_b_c",
	}
	for slug, want := range cases {
		if got := SiteUnixUser(slug); got != want {
			t.Errorf("SiteUnixUser(%q) = %q, want %q", slug, got, want)
		}
	}
}
