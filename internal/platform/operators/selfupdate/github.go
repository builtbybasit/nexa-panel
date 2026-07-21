package selfupdate

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// gitHubAPIBase is the REST host queried for release metadata. It is a package
// variable rather than a constant only so tests can point it at a local server;
// production never changes it.
var gitHubAPIBase = "https://api.github.com"

// gitHubSource resolves releases from the trusted repository's GitHub Releases.
type gitHubSource struct {
	http *http.Client
	base string
}

func newGitHubSource(client *http.Client) *gitHubSource {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &gitHubSource{http: client, base: gitHubAPIBase}
}

// gitHubRelease is the subset of the release payload the operator relies on.
type gitHubRelease struct {
	TagName     string    `json:"tag_name"`
	Body        string    `json:"body"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
	PublishedAt time.Time `json:"published_at"`
	Assets      []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

func (s *gitHubSource) Latest(ctx context.Context, arch string) (Release, error) {
	return s.fetch(ctx, arch, s.base+fmt.Sprintf("/repos/%s/%s/releases/latest", repositoryOwner, repositoryName))
}

func (s *gitHubSource) ByVersion(ctx context.Context, arch, version string) (Release, error) {
	normalized := normalizeVersion(version)
	if normalized == "" {
		return Release{}, fmt.Errorf("%q is not a valid release version", version)
	}
	tag := "v" + normalized
	return s.fetch(ctx, arch, s.base+fmt.Sprintf("/repos/%s/%s/releases/tags/%s", repositoryOwner, repositoryName, tag))
}

func (s *gitHubSource) fetch(ctx context.Context, arch, url string) (Release, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Release{}, fmt.Errorf("build release request: %w", err)
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	response, err := s.http.Do(request)
	if err != nil {
		return Release{}, fmt.Errorf("query release source: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return Release{}, fmt.Errorf("no matching release was published")
	}
	if response.StatusCode != http.StatusOK {
		return Release{}, fmt.Errorf("release source returned status %d", response.StatusCode)
	}
	var payload gitHubRelease
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&payload); err != nil {
		return Release{}, fmt.Errorf("decode release metadata: %w", err)
	}
	return releaseFromPayload(payload, arch)
}

// releaseFromPayload validates a release payload and resolves this node's
// architecture-specific asset and checksum URLs. Drafts and pre-releases are
// rejected so a node never updates onto an unfinished build.
func releaseFromPayload(payload gitHubRelease, arch string) (Release, error) {
	if payload.Draft || payload.Prerelease {
		return Release{}, fmt.Errorf("the latest release is not generally available")
	}
	version := normalizeVersion(payload.TagName)
	if version == "" {
		return Release{}, fmt.Errorf("release %q has no recognizable version tag", payload.TagName)
	}
	suffix, ok := assetArch(arch)
	if !ok {
		return Release{}, fmt.Errorf("self-update is unsupported on this architecture")
	}
	assetName := fmt.Sprintf("nexa-linux-%s", suffix)
	checksumName := assetName + ".sha256"
	var assetURL, checksumURL string
	for _, asset := range payload.Assets {
		switch asset.Name {
		case assetName:
			assetURL = asset.URL
		case checksumName:
			checksumURL = asset.URL
		}
	}
	if assetURL == "" || checksumURL == "" {
		return Release{}, fmt.Errorf("release %s has no %s binary and checksum", payload.TagName, suffix)
	}
	return Release{
		Version:     version,
		Tag:         payload.TagName,
		Notes:       payload.Body,
		AssetURL:    assetURL,
		ChecksumURL: checksumURL,
		PublishedAt: payload.PublishedAt,
	}, nil
}
