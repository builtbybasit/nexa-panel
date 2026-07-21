package selfupdate

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

// maxBinaryBytes caps a downloaded release binary. It is generous headroom over
// a real nexa binary while still refusing a runaway or hostile response.
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
	http *http.Client
}

func newHTTPDownloader(client *http.Client) *httpDownloader {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Minute}
	}
	return &httpDownloader{http: client}
}

func (d *httpDownloader) Fetch(ctx context.Context, url string, limit int64) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build download request: %w", err)
	}
	response, err := d.http.Do(request)
	if err != nil {
		return nil, fmt.Errorf("download release asset: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("release asset download returned status %d", response.StatusCode)
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
