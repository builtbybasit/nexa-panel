package selfupdate

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
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

func TestGitHubSourceDistinguishesUnauthorizedFromMissing(t *testing.T) {
	t.Setenv(releaseTokenEnv, "")
	for status, want := range map[int]error{
		http.StatusUnauthorized: errReleaseTokenRejected,
		http.StatusForbidden:    errReleaseTokenRejected,
		http.StatusNotFound:     errReleaseNotFound,
	} {
		code := status
		withAPIBase(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(code)
		}))
		source := newGitHubSource(nil, newReleaseTokens(filepath.Join(t.TempDir(), "absent")))
		_, err := source.Latest(context.Background(), "amd64")
		if !errors.Is(err, want) {
			t.Fatalf("status %d produced %v, want %v", code, err, want)
		}
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

func TestDownloaderDistinguishesUnauthorizedFromMissing(t *testing.T) {
	t.Setenv(releaseTokenEnv, "")
	for status, want := range map[int]error{
		http.StatusUnauthorized: errReleaseTokenRejected,
		http.StatusForbidden:    errReleaseTokenRejected,
		http.StatusNotFound:     errReleaseNotFound,
	} {
		code := status
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(code)
		}))
		downloader := newHTTPDownloader(nil, newReleaseTokens(filepath.Join(t.TempDir(), "absent")))
		_, err := downloader.Fetch(context.Background(), server.URL, maxArchiveBytes)
		server.Close()
		if !errors.Is(err, want) {
			t.Fatalf("status %d produced %v, want %v", code, err, want)
		}
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
