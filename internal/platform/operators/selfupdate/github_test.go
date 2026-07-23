package selfupdate

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// releasePayload is the release JSON GitHub serves, with the asset API URLs the
// operator must use on a private repository.
func releasePayload(base string) string {
	payload := map[string]any{
		"tag_name": "v0.5.0",
		"body":     "notes",
		"assets": []map[string]any{
			{"name": "nexa-panel-linux-amd64.tar.gz", "url": base + "/repos/builtbybasit/nexa-panel/releases/assets/11", "browser_download_url": base + "/never/use/this"},
			{"name": "nexa-panel-linux-amd64.tar.gz.sha256", "url": base + "/repos/builtbybasit/nexa-panel/releases/assets/12", "browser_download_url": base + "/never/use/this.sha256"},
			{"name": "nexa-panel-linux-amd64.tar.gz.sig", "url": base + "/repos/builtbybasit/nexa-panel/releases/assets/13", "browser_download_url": base + "/never/use/this.sig"},
		},
	}
	encoded, _ := json.Marshal(payload)
	return string(encoded)
}

// withAPIBase points the package's release host at a local server for the
// duration of a test, which is the seam the package already uses.
func withAPIBase(t *testing.T, handler http.Handler) string {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	previous := gitHubAPIBase
	gitHubAPIBase = server.URL
	t.Cleanup(func() { gitHubAPIBase = previous })
	return server.URL
}

func TestGitHubSourceResolvesAssetAPIURLs(t *testing.T) {
	t.Setenv(releaseTokenEnv, "")
	var requested string
	withAPIBase(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(releasePayload("BASE")))
	}))

	source := newGitHubSource(nil, newReleaseTokens(filepath.Join(t.TempDir(), "absent")))
	release, err := source.Latest(context.Background(), "amd64")
	if err != nil {
		t.Fatalf("latest: %v", err)
	}
	// The trusted repository is compile-time fixed; the request must go to it.
	if requested != "/repos/builtbybasit/nexa-panel/releases/latest" {
		t.Fatalf("requested %q, want the builtbybasit/nexa-panel release path", requested)
	}
	// The asset API URL, never browser_download_url: the latter serves HTML or
	// a 404 for a private repository even with a valid token.
	if release.AssetURL != "BASE/repos/builtbybasit/nexa-panel/releases/assets/11" {
		t.Fatalf("asset URL = %q, want the asset API URL", release.AssetURL)
	}
	if release.ChecksumURL != "BASE/repos/builtbybasit/nexa-panel/releases/assets/12" {
		t.Fatalf("checksum URL = %q, want the asset API URL", release.ChecksumURL)
	}
	if release.SignatureURL != "BASE/repos/builtbybasit/nexa-panel/releases/assets/13" {
		t.Fatalf("signature URL = %q, want the asset API URL", release.SignatureURL)
	}
	if strings.Contains(release.AssetURL, "never/use/this") {
		t.Fatal("the browser download URL must never be used")
	}
}

func TestGitHubSourceByVersionUsesTheTag(t *testing.T) {
	t.Setenv(releaseTokenEnv, "")
	var requested string
	withAPIBase(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested = r.URL.Path
		_, _ = w.Write([]byte(releasePayload("BASE")))
	}))

	source := newGitHubSource(nil, newReleaseTokens(filepath.Join(t.TempDir(), "absent")))
	if _, err := source.ByVersion(context.Background(), "amd64", "0.5.0"); err != nil {
		t.Fatalf("by version: %v", err)
	}
	if requested != "/repos/builtbybasit/nexa-panel/releases/tags/v0.5.0" {
		t.Fatalf("requested %q", requested)
	}
}

func TestReleaseMetadataRequiresDetachedSignature(t *testing.T) {
	payload := gitHubRelease{
		TagName: "v0.5.0",
		Assets: []gitHubAsset{
			{Name: "nexa-panel-linux-amd64.tar.gz", URL: "archive"},
			{Name: "nexa-panel-linux-amd64.tar.gz.sha256", URL: "checksum"},
		},
	}
	if _, err := releaseFromPayload(payload, "amd64"); err == nil || !strings.Contains(err.Error(), "signature") {
		t.Fatalf("unsigned release should be rejected, got %v", err)
	}
}

func TestGitHubSourceSendsTheTokenWhenPresent(t *testing.T) {
	t.Setenv(releaseTokenEnv, "")
	var authorization string
	withAPIBase(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(releasePayload("BASE")))
	}))

	path := filepath.Join(t.TempDir(), "release.token")
	if err := os.WriteFile(path, []byte("ghp_secret\n"), 0o600); err != nil {
		t.Fatalf("write token: %v", err)
	}
	source := newGitHubSource(nil, newReleaseTokens(path))
	if _, err := source.Latest(context.Background(), "amd64"); err != nil {
		t.Fatalf("latest: %v", err)
	}
	if authorization != "Bearer ghp_secret" {
		t.Fatalf("authorization = %q, want the bearer token", authorization)
	}
}

func TestGitHubSourceOmitsAuthorizationWithoutAToken(t *testing.T) {
	t.Setenv(releaseTokenEnv, "")
	var hadAuthorization bool
	withAPIBase(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, hadAuthorization = r.Header["Authorization"]
		_, _ = w.Write([]byte(releasePayload("BASE")))
	}))

	source := newGitHubSource(nil, newReleaseTokens(filepath.Join(t.TempDir(), "absent")))
	if _, err := source.Latest(context.Background(), "amd64"); err != nil {
		t.Fatalf("latest: %v", err)
	}
	if hadAuthorization {
		t.Fatal("a node without a token must issue the request unauthenticated")
	}
}

// seedToken writes a mode-0600 release token and returns its path.
func seedToken(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "release.token")
	if err := os.WriteFile(path, []byte("github_pat_secret\n"), 0o600); err != nil {
		t.Fatalf("write token: %v", err)
	}
	return path
}

// absentToken returns a path no token file exists at, standing in for a node the
// installer has not been given a credential for.
func absentToken(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "absent")
}

// credentialFailure is one release-source refusal and the cause a node operator
// has to be told about. Every one of these was previously reported as "release
// token missing or invalid", which sends someone to rotate a credential that is
// fine (rate limiting) or to install one that is already installed but too
// narrow (insufficient scope).
type credentialFailure struct {
	name    string
	status  int
	header  map[string]string
	token   func(*testing.T) string
	want    error
	message string
}

func credentialFailures() []credentialFailure {
	return []credentialFailure{
		{
			name:   "missing credential",
			status: http.StatusUnauthorized,
			token:  absentToken,
			want:   errReleaseTokenMissing,
		},
		{
			name:   "missing credential against a private repository",
			status: http.StatusNotFound,
			token:  absentToken,
			want:   errReleaseNotFound,
			// A private repository 404s an anonymous caller, so the absent token
			// has to be named or the message sends someone to look for a release
			// that is published.
			message: "no release token",
		},
		{
			name:   "expired or revoked credential",
			status: http.StatusUnauthorized,
			token:  seedToken,
			want:   errReleaseTokenRejected,
		},
		{
			name:   "insufficient scope",
			status: http.StatusForbidden,
			header: map[string]string{"X-Accepted-GitHub-Permissions": "contents=read"},
			token:  seedToken,
			want:   errReleaseTokenInsufficientScope,
		},
		{
			name:   "insufficient scope on a classic token",
			status: http.StatusForbidden,
			header: map[string]string{"X-Accepted-OAuth-Scopes": "repo"},
			token:  seedToken,
			want:   errReleaseTokenInsufficientScope,
		},
		{
			name:   "exhausted hourly quota",
			status: http.StatusForbidden,
			header: map[string]string{
				"X-RateLimit-Remaining": "0",
				"X-RateLimit-Reset":     strconv.FormatInt(time.Now().Add(37*time.Minute).Unix(), 10),
			},
			token:   seedToken,
			want:    errReleaseRateLimited,
			message: "hourly request quota",
		},
		{
			name:    "secondary rate limit",
			status:  http.StatusForbidden,
			header:  map[string]string{"Retry-After": "600"},
			token:   seedToken,
			want:    errReleaseRateLimited,
			message: "secondary rate limit",
		},
		{
			name:   "too many requests",
			status: http.StatusTooManyRequests,
			token:  seedToken,
			want:   errReleaseRateLimited,
		},
		{
			name:   "no such release",
			status: http.StatusNotFound,
			token:  seedToken,
			want:   errReleaseNotFound,
		},
	}
}

func statusHandler(status int, header map[string]string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		for key, value := range header {
			w.Header().Set(key, value)
		}
		w.WriteHeader(status)
	})
}

func TestGitHubSourceClassifiesCredentialAndQuotaFailures(t *testing.T) {
	for _, testCase := range credentialFailures() {
		t.Run(testCase.name, func(t *testing.T) {
			t.Setenv(releaseTokenEnv, "")
			withAPIBase(t, statusHandler(testCase.status, testCase.header))
			source := newGitHubSource(nil, newReleaseTokens(testCase.token(t)))
			_, err := source.Latest(context.Background(), "amd64")
			if !errors.Is(err, testCase.want) {
				t.Fatalf("status %d produced %v, want %v", testCase.status, err, testCase.want)
			}
			if testCase.message != "" && !strings.Contains(err.Error(), testCase.message) {
				t.Fatalf("error %q does not mention %q", err, testCase.message)
			}
			// A quota refusal is never a credential fault, and saying so sends an
			// operator to rotate a working token.
			if errors.Is(err, errReleaseRateLimited) && errors.Is(err, errReleaseTokenRejected) {
				t.Fatalf("a rate limit must not be reported as a rejected credential: %v", err)
			}
		})
	}
}

func TestGitHubSourceWaitsOutAShortRateLimit(t *testing.T) {
	t.Setenv(releaseTokenEnv, "")
	var attempts int
	withAPIBase(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		if attempts == 1 {
			// A secondary rate limit clears in seconds; the node should wait it
			// out rather than fail an update check on it.
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(releasePayload("BASE")))
	}))

	source := newGitHubSource(nil, newReleaseTokens(absentToken(t)))
	release, err := source.Latest(context.Background(), "amd64")
	if err != nil {
		t.Fatalf("latest: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("made %d attempts, want the rate-limited one to be retried once", attempts)
	}
	if release.Version != "0.5.0" {
		t.Fatalf("resolved %q", release.Version)
	}
}

func TestGitHubSourceStopsRetryingAPersistentRateLimit(t *testing.T) {
	t.Setenv(releaseTokenEnv, "")
	var attempts int
	withAPIBase(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusTooManyRequests)
	}))

	source := newGitHubSource(nil, newReleaseTokens(absentToken(t)))
	_, err := source.Latest(context.Background(), "amd64")
	if !errors.Is(err, errReleaseRateLimited) {
		t.Fatalf("latest: %v, want a rate-limit error", err)
	}
	if attempts != releaseRequestAttempts {
		t.Fatalf("made %d attempts, want at most %d", attempts, releaseRequestAttempts)
	}
}

func TestGitHubSourceDoesNotWaitOutAnHourLongQuota(t *testing.T) {
	t.Setenv(releaseTokenEnv, "")
	var attempts int
	withAPIBase(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10))
		w.WriteHeader(http.StatusForbidden)
	}))

	source := newGitHubSource(nil, newReleaseTokens(seedToken(t)))
	_, err := source.Latest(context.Background(), "amd64")
	if !errors.Is(err, errReleaseRateLimited) {
		t.Fatalf("latest: %v, want a rate-limit error", err)
	}
	// Blocking the agent RPC for an hour is worse than an error that says when
	// the quota refills.
	if attempts != 1 {
		t.Fatalf("made %d attempts, want the hour-long wait to be reported rather than slept through", attempts)
	}
}

func TestRateLimitReadsTheWaitTheSourceAsksFor(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	header := http.Header{}
	header.Set("X-RateLimit-Remaining", "0")
	header.Set("X-RateLimit-Reset", strconv.FormatInt(now.Add(20*time.Minute).Unix(), 10))
	limit := rateLimitFrom(http.StatusForbidden, header, now)
	if limit == nil {
		t.Fatal("an exhausted quota was not recognised as a rate limit")
	}
	if limit.wait != 20*time.Minute || limit.secondary {
		t.Fatalf("read %+v, want a 20m primary limit", limit)
	}
	// The operator has to be told when to come back, not just that it failed.
	if !strings.Contains(limit.Error(), "20m0s") {
		t.Fatalf("error %q does not say how long to wait", limit)
	}

	secondary := rateLimitFrom(http.StatusForbidden, http.Header{"Retry-After": []string{"45"}}, now)
	if secondary == nil || secondary.wait != 45*time.Second || !secondary.secondary {
		t.Fatalf("read %+v, want a 45s secondary limit", secondary)
	}
}

func TestForbiddenWithoutQuotaHeadersStaysACredentialFailure(t *testing.T) {
	// A bare 403 is a permission decision, not a quota, and must not be softened
	// into "try again later".
	err := classifyReleaseStatus(http.StatusForbidden, http.Header{}, true)
	if !errors.Is(err, errReleaseTokenRejected) {
		t.Fatalf("bare 403 produced %v, want a credential failure", err)
	}
	if errors.Is(err, errReleaseRateLimited) {
		t.Fatalf("bare 403 must not be reported as a rate limit: %v", err)
	}
}

func TestDownloaderFetchesAssetsAsOctetStream(t *testing.T) {
	t.Setenv(releaseTokenEnv, "")
	var accept, authorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		accept = r.Header.Get("Accept")
		authorization = r.Header.Get("Authorization")
		_, _ = w.Write([]byte("asset-bytes"))
	}))
	t.Cleanup(server.Close)

	path := filepath.Join(t.TempDir(), "release.token")
	if err := os.WriteFile(path, []byte("ghp_secret"), 0o600); err != nil {
		t.Fatalf("write token: %v", err)
	}
	downloader := newHTTPDownloader(nil, newReleaseTokens(path))
	data, err := downloader.Fetch(context.Background(), server.URL+"/repos/o/r/releases/assets/11", maxArchiveBytes)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if string(data) != "asset-bytes" {
		t.Fatalf("fetched %q", data)
	}
	// Without octet-stream the asset API returns the asset's JSON metadata
	// instead of the file.
	if accept != "application/octet-stream" {
		t.Fatalf("accept = %q", accept)
	}
	if authorization != "Bearer ghp_secret" {
		t.Fatalf("authorization = %q", authorization)
	}
}

func TestDownloaderClassifiesCredentialAndQuotaFailures(t *testing.T) {
	// The asset download is a separate request against a separate endpoint, and a
	// token can be refused there alone (an asset URL outlives the metadata call);
	// it therefore has to name the same causes the same way.
	for _, testCase := range credentialFailures() {
		t.Run(testCase.name, func(t *testing.T) {
			t.Setenv(releaseTokenEnv, "")
			server := httptest.NewServer(statusHandler(testCase.status, testCase.header))
			defer server.Close()
			downloader := newHTTPDownloader(nil, newReleaseTokens(testCase.token(t)))
			_, err := downloader.Fetch(context.Background(), server.URL, maxArchiveBytes)
			if !errors.Is(err, testCase.want) {
				t.Fatalf("status %d produced %v, want %v", testCase.status, err, testCase.want)
			}
		})
	}
}

func TestDownloaderRefusesAMalformedTokenFileWithoutLeakingIt(t *testing.T) {
	t.Setenv(releaseTokenEnv, "")
	path := filepath.Join(t.TempDir(), "release.token")
	if err := os.WriteFile(path, []byte("ghp_secret"), 0o644); err != nil {
		t.Fatalf("write token: %v", err)
	}
	downloader := newHTTPDownloader(nil, newReleaseTokens(path))
	_, err := downloader.Fetch(context.Background(), "https://example.invalid/asset", maxArchiveBytes)
	if err == nil {
		t.Fatal("expected a world-readable token file to abort the download")
	}
	if strings.Contains(err.Error(), "ghp_secret") {
		t.Fatalf("the error must never quote the credential: %v", err)
	}
}
