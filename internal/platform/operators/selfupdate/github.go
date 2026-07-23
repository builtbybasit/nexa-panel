package selfupdate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// gitHubAPIBase is the REST host queried for release metadata. It is a package
// variable rather than a constant only so tests can point it at a local server;
// production never changes it.
var gitHubAPIBase = "https://api.github.com"

// The release source answers a credential problem, a quota problem, and a
// missing release with overlapping status codes, and every one of them needs a
// different fix on the node: install a token, replace an expired one, widen a
// too-narrow one, or simply wait. Collapsing them into "token rejected" — as
// this operator once did — sends an operator to rotate a perfectly good
// credential while the node is only being rate limited, so each cause gets its
// own sentinel and its own sentence.
var (
	// errReleaseTokenMissing is the unauthenticated 401/403: the node sent no
	// credential at all, so there is nothing to rotate, only something to install.
	errReleaseTokenMissing = errors.New("this node has no release token: the release repository is private and rejects unauthenticated requests")
	// errReleaseTokenRejected is the authenticated 401: GitHub answers "Bad
	// credentials" for a token that has expired, been revoked, or never existed.
	errReleaseTokenRejected = errors.New("this node's release token was rejected: it has expired, been revoked, or does not belong to the release repository")
	// errReleaseTokenInsufficientScope is the authenticated 403 that names the
	// permission it wanted. The credential is live; it is simply too narrow, and
	// rotating it to another equally narrow token fixes nothing.
	errReleaseTokenInsufficientScope = errors.New("this node's release token is valid but lacks read-only Contents access to the release repository")
	// errReleaseRateLimited is the quota case. It is never a credential fault,
	// and the only useful action is to wait.
	errReleaseRateLimited = errors.New("the release source is rate limiting this node")
	// errReleaseNotFound is returned when no release matches. On a private
	// repository this is also what an unauthenticated request sees, which is why
	// the message mentions both possibilities.
	errReleaseNotFound = errors.New("no matching release was published, or this node's release token cannot see it")
)

// rateLimitedError carries how long the source asked the node to wait so the
// message can say it. It unwraps to errReleaseRateLimited so callers classify
// with errors.Is rather than by string.
type rateLimitedError struct {
	wait time.Duration
	// secondary distinguishes the abuse-detection limit (which is per-burst and
	// clears in seconds) from the exhausted hourly quota (which does not).
	secondary bool
}

func (e *rateLimitedError) Error() string {
	kind := "hourly request quota"
	if e.secondary {
		kind = "secondary rate limit"
	}
	if e.wait > 0 {
		return fmt.Sprintf("the release source's %s is exhausted; it will accept requests again in about %s", kind, e.wait.Round(time.Second))
	}
	return fmt.Sprintf("the release source's %s is exhausted; retry later", kind)
}

func (e *rateLimitedError) Unwrap() error { return errReleaseRateLimited }

// releaseRequestAttempts bounds how many times one request is issued, so a
// source that answers 429 forever cannot hold an update check open.
const releaseRequestAttempts = 3

// maxRateLimitWait is the longest delay worth sleeping through in-process. A
// secondary limit clears in seconds and is worth waiting out; an exhausted
// hourly quota resets up to an hour later, and blocking an agent RPC for that is
// worse than returning an error that says when to come back.
const maxRateLimitWait = 90 * time.Second

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
	response, err := sendReleaseRequest(ctx, s.http, request)
	if err != nil {
		return Release{}, fmt.Errorf("query release source: %w", err)
	}
	defer response.Body.Close()
	if err := classifyReleaseStatus(response.StatusCode, response.Header, isAuthorized(request)); err != nil {
		return Release{}, err
	}
	var payload gitHubRelease
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&payload); err != nil {
		return Release{}, fmt.Errorf("decode release metadata: %w", err)
	}
	return releaseFromPayload(payload, arch)
}

// isAuthorized reports whether a request actually carried the release
// credential. It is read off the request rather than tracked separately so
// "did this node send a token" cannot drift from what was sent.
func isAuthorized(request *http.Request) bool {
	return request.Header.Get("Authorization") != ""
}

// sendReleaseRequest issues a request, waiting out a rate limit the source
// itself says is short. Retrying is only ever done for a rate limit: every other
// failure is a decision (a bad credential, a missing release) that a second
// identical request cannot change.
func sendReleaseRequest(ctx context.Context, client *http.Client, request *http.Request) (*http.Response, error) {
	for attempt := 1; ; attempt++ {
		response, err := client.Do(request.Clone(ctx))
		if err != nil {
			return nil, err
		}
		limit := rateLimitFrom(response.StatusCode, response.Header, time.Now())
		if limit == nil || attempt >= releaseRequestAttempts || limit.wait > maxRateLimitWait {
			return response, nil
		}
		// The retried body is never read, but it has to be drained-and-closed or
		// the connection cannot be reused for the retry.
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<16))
		_ = response.Body.Close()
		timer := time.NewTimer(limit.wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

// rateLimitFrom reports whether a response is a rate limit, and how long it asks
// the caller to wait, or nil when it is not a rate limit at all.
//
// GitHub signals three different things through two status codes. A 429 is
// always a limit. A 403 is a limit only when it carries one of the limit
// headers — Retry-After for the secondary (abuse-detection) limit, or an
// exhausted X-RateLimit-Remaining for the hourly quota; a 403 without either is
// a permission decision about the credential, not a quota.
func rateLimitFrom(status int, header http.Header, now time.Time) *rateLimitedError {
	if status != http.StatusTooManyRequests && status != http.StatusForbidden {
		return nil
	}
	if value := strings.TrimSpace(header.Get("Retry-After")); value != "" {
		if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 {
			return &rateLimitedError{wait: time.Duration(seconds) * time.Second, secondary: true}
		}
	}
	if strings.TrimSpace(header.Get("X-RateLimit-Remaining")) == "0" {
		limit := &rateLimitedError{}
		if reset, err := strconv.ParseInt(strings.TrimSpace(header.Get("X-RateLimit-Reset")), 10, 64); err == nil {
			if wait := time.Unix(reset, 0).Sub(now); wait > 0 {
				limit.wait = wait
			}
		}
		return limit
	}
	if status == http.StatusTooManyRequests {
		return &rateLimitedError{}
	}
	return nil
}

// classifyReleaseStatus turns a response into the actionable error for that
// failure, or nil for a success. It is shared by the metadata and the asset
// download so both name the same causes the same way.
func classifyReleaseStatus(status int, header http.Header, authenticated bool) error {
	if status == http.StatusOK {
		return nil
	}
	if limit := rateLimitFrom(status, header, time.Now()); limit != nil {
		return limit
	}
	switch status {
	case http.StatusUnauthorized:
		if !authenticated {
			return errReleaseTokenMissing
		}
		return errReleaseTokenRejected
	case http.StatusForbidden:
		// A fine-grained token that is missing a permission is refused with the
		// permission it needed named in a response header; a classic token missing
		// a scope gets the OAuth equivalent. Either way the credential itself is
		// live, so this is a re-issue with wider access, not a rotation.
		if header.Get("X-Accepted-GitHub-Permissions") != "" || header.Get("X-Accepted-OAuth-Scopes") != "" {
			return errReleaseTokenInsufficientScope
		}
		if !authenticated {
			return errReleaseTokenMissing
		}
		return errReleaseTokenRejected
	case http.StatusNotFound:
		if !authenticated {
			// A private repository is a 404 to an anonymous caller, so on a node
			// with no credential the missing token is the far likelier cause.
			return fmt.Errorf("%w; this node sent no release token", errReleaseNotFound)
		}
		return errReleaseNotFound
	default:
		return fmt.Errorf("release source returned status %d", status)
	}
}

// releaseFromPayload validates a release payload and resolves this node's
// architecture-specific tarball, checksum, and detached-signature URLs. Drafts
// and pre-releases are rejected so a node never updates onto an unfinished
// build.
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
	signatureName := assetName + ".sig"
	var assetURL, checksumURL, signatureURL string
	for _, asset := range payload.Assets {
		switch asset.Name {
		case assetName:
			assetURL = asset.URL
		case checksumName:
			checksumURL = asset.URL
		case signatureName:
			signatureURL = asset.URL
		}
	}
	if assetURL == "" || checksumURL == "" || signatureURL == "" {
		return Release{}, fmt.Errorf("release %s has no complete %s archive, checksum, and signature set", payload.TagName, suffix)
	}
	return Release{
		Version:      version,
		Tag:          payload.TagName,
		Notes:        payload.Body,
		AssetName:    assetName,
		AssetURL:     assetURL,
		ChecksumURL:  checksumURL,
		SignatureURL: signatureURL,
		PublishedAt:  payload.PublishedAt,
	}, nil
}
