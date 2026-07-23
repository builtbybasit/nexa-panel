package selfupdate

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

// maxArchiveBytes caps a downloaded release tarball. It is generous headroom
// over a real release (a binary plus a small packaging tree) while still
// refusing a runaway or hostile response.
const maxArchiveBytes = 256 * 1024 * 1024

// maxBinaryBytes caps a bare nexa binary, whether extracted from a release or
// staged on the host by an operator.
const maxBinaryBytes = 256 * 1024 * 1024

// maxChecksumBytes caps the tiny checksum sidecar.
const maxChecksumBytes = 4 * 1024

// Downloader fetches a release asset by URL, capping the response so a hostile
// or misconfigured source cannot exhaust memory. It is an interface so the
// HTTP-facing implementation can be replaced in tests.
type Downloader interface {
	Fetch(ctx context.Context, url string, limit int64) ([]byte, error)
}

type httpDownloader struct {
	http   *http.Client
	tokens *releaseTokens
}

func newHTTPDownloader(client *http.Client, tokens *releaseTokens) *httpDownloader {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Minute}
	}
	if tokens == nil {
		tokens = newReleaseTokens("")
	}
	return &httpDownloader{http: client, tokens: tokens}
}

func (d *httpDownloader) Fetch(ctx context.Context, url string, limit int64) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build download request: %w", err)
	}
	// Release assets are fetched from their API URL, and that endpoint only
	// answers with the file itself when asked for octet-stream; with the JSON
	// Accept it returns the asset's metadata instead. Paired with the bearer
	// token this is the only combination that works on a private repository.
	request.Header.Set("Accept", "application/octet-stream")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if err := d.tokens.authorize(request); err != nil {
		return nil, err
	}
	response, err := sendReleaseRequest(ctx, d.http, request)
	if err != nil {
		return nil, fmt.Errorf("download release asset: %w", err)
	}
	defer response.Body.Close()
	if err := classifyReleaseStatus(response.StatusCode, response.Header, isAuthorized(request)); err != nil {
		return nil, err
	}
	// LimitReader+1 lets an over-limit body be detected rather than silently
	// truncated into a valid-looking-but-wrong artifact.
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read release asset: %w", err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("release asset exceeds the %d byte limit", limit)
	}
	return data, nil
}
