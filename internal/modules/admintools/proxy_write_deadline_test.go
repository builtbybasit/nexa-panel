package admintools

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strconv"
	"testing"
	"time"
)

// smallBufferListener shrinks the kernel send buffer on every accepted
// connection so a throttled client backpressures the upstream write within a few
// kilobytes. Without it the OS socket buffers can swallow a whole multi-megabyte
// body before the server ever blocks, and the WriteTimeout under test would
// never fire regardless of client speed.
type smallBufferListener struct{ net.Listener }

func (l smallBufferListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	if tcp, ok := conn.(*net.TCPConn); ok {
		_ = tcp.SetWriteBuffer(8 << 10)
	}
	return conn, nil
}

// downloadThroughProxy stands up a reverse proxy, in front of an upstream that
// serves `size` bytes, on an http.Server whose WriteTimeout is deliberately
// short. A client drains the body slowly so the whole transfer outlasts the
// WriteTimeout. When extend is true the handler lifts the per-connection write
// deadline before proxying, exactly as proxyHTTP does. It returns how many body
// bytes actually arrived before the stream ended.
func downloadThroughProxy(t *testing.T, size int, writeTimeout time.Duration, extend bool) int {
	t.Helper()

	payload := make([]byte, size)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		w.Header().Set("Content-Length", strconv.Itoa(size))
		w.WriteHeader(http.StatusOK)
		for written := 0; written < size; {
			chunk := 32 << 10
			if remaining := size - written; remaining < chunk {
				chunk = remaining
			}
			n, err := w.Write(payload[written : written+chunk])
			if err != nil {
				return
			}
			written += n
		}
	}))
	t.Cleanup(upstream.Close)

	target, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if extend {
			extendProxyWriteDeadline(w)
		}
		proxy.ServeHTTP(w, r)
	})

	base, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: handler, WriteTimeout: writeTimeout}
	go func() { _ = server.Serve(smallBufferListener{base}) }()
	t.Cleanup(func() { _ = server.Close() })

	client := &http.Client{Transport: &http.Transport{
		DisableKeepAlives: true,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			conn, err := (&net.Dialer{}).DialContext(ctx, network, address)
			if err == nil {
				if tcp, ok := conn.(*net.TCPConn); ok {
					_ = tcp.SetReadBuffer(8 << 10)
				}
			}
			return conn, err
		},
	}}
	request, err := http.NewRequest(http.MethodGet, "http://"+base.Addr().String()+"/vendor.others.js", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()

	buf := make([]byte, 8<<10)
	bodyBytes := 0
	for {
		n, readErr := response.Body.Read(buf)
		bodyBytes += n
		if readErr != nil {
			break
		}
		// Draining far slower than the upstream produces forces the server's
		// write to block, so a live WriteTimeout severs the stream mid-body.
		time.Sleep(2 * time.Millisecond)
	}
	return bodyBytes
}

// TestAdminToolProxyLargeAssetSurvivesTheServerWriteTimeout is the regression
// guard for the pgAdmin "Loading… forever" defect: vendor.others.js (~9.8 MB)
// streams to the browser with nginx proxy_buffering off, so a real load outlasts
// the API server's 30s WriteTimeout and the stream is severed mid-body — which
// Chrome reports as ERR_HTTP2_PROTOCOL_ERROR on a 200. The panel must lift the
// per-connection write deadline for tool proxy responses.
func TestAdminToolProxyLargeAssetSurvivesTheServerWriteTimeout(t *testing.T) {
	const size = 2 << 20 // 2 MiB — enough to outrun the throttle before the timeout.
	const writeTimeout = 200 * time.Millisecond

	truncated := downloadThroughProxy(t, size, writeTimeout, false)
	if truncated >= size {
		t.Fatalf("expected the short WriteTimeout to sever a slow large transfer, but the whole %d bytes arrived; the throttle/timeout no longer reproduce the defect", size)
	}

	full := downloadThroughProxy(t, size, writeTimeout, true)
	if full != size {
		t.Fatalf("extending the write deadline should deliver the whole asset: got %d of %d bytes", full, size)
	}
}
