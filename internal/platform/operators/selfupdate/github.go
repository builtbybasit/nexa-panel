package selfupdate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// gitHubAPIBase is the REST host queried for release metadata. It is a package
// variable rather than a constant only so tests can point it at a local server;
// production never changes it.
var gitHubAPIBase = "https://api.github.com"

// errReleaseTokenRejected is returned when the release source answers 401 or
// 403. It is distinct from a 404 on purpose: "no such release" and "this node
// cannot authenticate to the release repository" need different fixes, and a
// private repository answers 404 to an anonymous caller too, so the message has
// to name the credential as the likely cause.
var errReleaseTokenRejected = errors.New("release token missing or invalid: the node could not authenticate to the release repository")

// errReleaseNotFound is returned when no release matches. On a private
// repository this is also what an unauthenticated request sees, which is why
// the message mentions both possibilities.
var errReleaseNotFound = errors.New("no matching release was published, or this node's release token cannot see it")

// gitHubSource resolves releases from the trusted repository's GitHub Releases.
type gitHubSource struct {
	http   *http.Client
	base   string
	tokens *releaseTokens
}

func newGitHubSource(client *http.Client, tokens *releaseTokens) *gitHubSource {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	if tokens == nil {
		tokens = newReleaseTokens("")
	}
	return &gitHubSource{http: client, base: gitHubAPIBase, tokens: tokens}
}

// gitHubAsset is one published file in a release. URL is the asset's API URL,
// not its browser_download_url: on a private repository the browser URL serves
// HTML or a 404 even to a token-bearing client, and only the API URL fetched
// with Accept: application/octet-stream returns the bytes.
type gitHubAsset struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

// gitHubRelease is the subset of the release payload the operator relies on.
type gitHubRelease struct {
	TagName     string        `json:"tag_name"`
	Body        string        `json:"body"`
	Draft       bool          `json:"draft"`
	Prerelease  bool          `json:"prerelease"`
	PublishedAt time.Time     `json:"published_at"`
	Assets      []gitHubAsset `json:"assets"`
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
	// The token is optional. A node without one issues exactly the same request
	// unauthenticated, so nothing here changes if the repository is ever opened.
	if err := s.tokens.authorize(request); err != nil {
		return Release{}, err
	}
	response, err := s.http.Do(request)
	if err != nil {
		return Release{}, fmt.Errorf("query release source: %w", err)
	}
	defer response.Body.Close()
	if err := classifyReleaseStatus(response.StatusCode); err != nil {
		return Release{}, err
	}
	var payload gitHubRelease
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&payload); err != nil {
		return Release{}, fmt.Errorf("decode release metadata: %w", err)
	}
	return releaseFromPayload(payload, arch)
}

// classifyReleaseStatus turns a response status into the actionable error for
// that failure, or nil for a success. It is shared by the metadata and the asset
// download so both name the same causes the same way.
func classifyReleaseStatus(status int) error {
	switch {
	case status == http.StatusOK:
		return nil
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return errReleaseTokenRejected
	case status == http.StatusNotFound:
		return errReleaseNotFound
	default:
		return fmt.Errorf("release source returned status %d", status)
	}
}

// releaseFromPayload validates a release payload and resolves this node's
// architecture-specific tarball and checksum URLs. Drafts and pre-releases are
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
	assetName := fmt.Sprintf("nexa-panel-linux-%s.tar.gz", suffix)
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
		return Release{}, fmt.Errorf("release %s has no %s archive and checksum", payload.TagName, suffix)
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
