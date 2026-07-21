package selfupdate

import "testing"

func TestNormalizeVersion(t *testing.T) {
	cases := map[string]string{
		"v0.2.0":       "0.2.0",
		" 0.2.0 ":      "0.2.0",
		"0.1.0-dev":    "0.1.0-dev",
		"v1.10.3-rc.1": "1.10.3-rc.1",
		"garbage":      "",
		"1.2":          "",
		"1.2.3.4":      "",
		"":             "",
	}
	for input, want := range cases {
		if got := normalizeVersion(input); got != want {
			t.Errorf("normalizeVersion(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestIsNewer(t *testing.T) {
	cases := []struct {
		candidate, baseline string
		want                bool
	}{
		{"0.2.0", "0.1.0", true},
		{"0.1.1", "0.1.0", true},
		{"1.0.0", "0.9.9", true},
		{"0.1.0", "0.1.0", false},
		{"0.0.9", "0.1.0", false},
		{"0.1.0", "0.2.0", false},
		// A final release outranks a pre-release of the same triple; the reverse is
		// not an update.
		{"0.2.0", "0.2.0-rc.1", true},
		{"0.2.0-rc.1", "0.2.0", false},
	}
	for _, tc := range cases {
		if got := isNewer(tc.candidate, tc.baseline); got != tc.want {
			t.Errorf("isNewer(%q, %q) = %v, want %v", tc.candidate, tc.baseline, got, tc.want)
		}
	}
}

func TestParseChecksum(t *testing.T) {
	digest := "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	got, err := parseChecksum(digest + "  nexa-linux-amd64\n")
	if err != nil {
		t.Fatalf("parseChecksum: %v", err)
	}
	if got != digest {
		t.Fatalf("digest = %q, want %q", got, digest)
	}
	if _, err := parseChecksum("not-a-digest  file\n"); err == nil {
		t.Fatal("expected a non-hex digest to be rejected")
	}
	if _, err := parseChecksum(""); err == nil {
		t.Fatal("expected an empty checksum file to be rejected")
	}
}

func TestReleaseFromPayloadResolvesArchAsset(t *testing.T) {
	payload := gitHubRelease{TagName: "v0.3.0"}
	payload.Assets = []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	}{
		{Name: "nexa-linux-amd64", URL: "https://d/amd64"},
		{Name: "nexa-linux-amd64.sha256", URL: "https://d/amd64.sha256"},
		{Name: "nexa-linux-arm64", URL: "https://d/arm64"},
		{Name: "nexa-linux-arm64.sha256", URL: "https://d/arm64.sha256"},
	}
	release, err := releaseFromPayload(payload, "arm64")
	if err != nil {
		t.Fatalf("releaseFromPayload: %v", err)
	}
	if release.AssetURL != "https://d/arm64" || release.ChecksumURL != "https://d/arm64.sha256" {
		t.Fatalf("unexpected asset resolution: %+v", release)
	}
	if release.Version != "0.3.0" {
		t.Fatalf("version = %q, want 0.3.0", release.Version)
	}
}

func TestReleaseFromPayloadRejectsPrerelease(t *testing.T) {
	payload := gitHubRelease{TagName: "v0.3.0", Prerelease: true}
	if _, err := releaseFromPayload(payload, "amd64"); err == nil {
		t.Fatal("expected a pre-release to be rejected")
	}
	draft := gitHubRelease{TagName: "v0.3.0", Draft: true}
	if _, err := releaseFromPayload(draft, "amd64"); err == nil {
		t.Fatal("expected a draft to be rejected")
	}
}

func TestReleaseFromPayloadRequiresMatchingAssets(t *testing.T) {
	payload := gitHubRelease{TagName: "v0.3.0"}
	payload.Assets = []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	}{
		{Name: "nexa-linux-amd64", URL: "https://d/amd64"},
	}
	if _, err := releaseFromPayload(payload, "amd64"); err == nil {
		t.Fatal("expected a release missing its checksum asset to be rejected")
	}
}
