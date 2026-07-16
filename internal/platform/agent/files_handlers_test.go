package agent

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/platform/agentauth"
	filesoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/files"
	nodeoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/nodes"
	"github.com/nexa-panel/nexa-panel/internal/platform/operators/sitefs"
)

func startFilesAgent(t *testing.T) (*filesoperator.UnixClient, sitefs.Scope, string, string) {
	t.Helper()
	directory := t.TempDir()
	socketDirectory, err := os.MkdirTemp("/private/tmp", "nexa-agent-")
	if err != nil {
		t.Fatalf("create socket directory: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(socketDirectory) })
	socketPath := filepath.Join(socketDirectory, "agent.sock")
	tokenPath := filepath.Join(directory, "agent.token")
	token, err := agentauth.OpenOrCreate(tokenPath)
	if err != nil {
		t.Fatalf("create token: %v", err)
	}

	siteRoot := t.TempDir()
	scope := sitefs.Scope{SiteID: "site_1", Slug: "demo", RootPath: filepath.Join(siteRoot, "demo"), UnixUser: "nexa_demo"}
	for _, zone := range []string{"public", "private", "tmp", "backups", "logs"} {
		if err := os.MkdirAll(filepath.Join(scope.RootPath, zone), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	hostOperator, err := filesoperator.NewHostOperator(siteRoot, sitefs.NopOwnership())
	if err != nil {
		t.Fatal(err)
	}

	nodeOperator, _ := nodeoperator.NewFileOperator(filepath.Join(directory, "probe.conf"))
	server := New(socketPath, "test", token, nodeOperator, slog.New(slog.NewTextHandler(io.Discard, nil)), WithFilesOperator(hostOperator))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("agent did not stop")
		}
	})
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(socketPath); err == nil {
			break
		}
		select {
		case err := <-done:
			if err != nil && strings.Contains(err.Error(), "operation not permitted") {
				t.Skip("sandbox does not permit Unix socket binding")
			}
			t.Fatalf("agent stopped before creating socket: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("agent socket was not created")
		}
		time.Sleep(10 * time.Millisecond)
	}
	return filesoperator.NewUnixClient(socketPath, tokenPath), scope, socketPath, token
}

func TestFilesAgentRoundTrip(t *testing.T) {
	client, scope, _, _ := startFilesAgent(t)
	ctx := context.Background()

	written, err := client.Write(ctx, scope, "public/index.php", "<?php echo 1;\n", "")
	if err != nil || written.ETag == "" {
		t.Fatalf("Write = %+v, %v", written, err)
	}
	read, err := client.Read(ctx, scope, "public/index.php")
	if err != nil || read.Content != "<?php echo 1;\n" || read.ETag != written.ETag {
		t.Fatalf("Read = %+v, %v", read, err)
	}
	listing, err := client.List(ctx, scope, "public")
	if err != nil || len(listing.Entries) != 1 || listing.Entries[0].Name != "index.php" {
		t.Fatalf("List = %+v, %v", listing, err)
	}
	stat, err := client.Stat(ctx, scope, "public/index.php")
	if err != nil || stat.Kind != "file" {
		t.Fatalf("Stat = %+v, %v", stat, err)
	}
	if _, err := client.Mkdir(ctx, scope, "public/assets"); err != nil {
		t.Fatalf("Mkdir = %v", err)
	}
	moved, err := client.Move(ctx, scope, "public/index.php", "public/assets/index.php", false)
	if err != nil || moved.Name != "index.php" {
		t.Fatalf("Move = %+v, %v", moved, err)
	}
	copied, err := client.Copy(ctx, scope, "public/assets", "backups/assets")
	if err != nil || copied.Copied != 2 {
		t.Fatalf("Copy = %+v, %v", copied, err)
	}
	archive, err := client.Archive(ctx, scope, []string{"public/assets"}, "backups/assets.tar.gz")
	if err != nil || archive.Entries != 2 || archive.Entry.Name != "assets.tar.gz" {
		t.Fatalf("Archive = %+v, %v", archive, err)
	}
	extracted, err := client.Extract(ctx, scope, "backups/assets.tar.gz", "private/restore")
	if err != nil || extracted.Written != 2 {
		t.Fatalf("Extract = %+v, %v", extracted, err)
	}
	size, err := client.DirectorySize(ctx, scope, "public")
	if err != nil || size.Files != 1 || size.Dirs != 1 {
		t.Fatalf("DirectorySize = %+v, %v", size, err)
	}
	if err := client.Delete(ctx, scope, "backups/assets", true); err != nil {
		t.Fatalf("Delete = %v", err)
	}

	// Error kinds survive the agent boundary.
	_, err = client.Write(ctx, scope, "public/assets/index.php", "x", "")
	var operationErr *filesoperator.OperationError
	if !errors.As(err, &operationErr) || operationErr.Code != filesoperator.CodeExistsConflict {
		t.Fatalf("conflict passthrough = %v", err)
	}
	if _, err := client.Read(ctx, scope, "logs/../../outside"); !errors.As(err, &operationErr) || operationErr.Code != filesoperator.CodeInvalid {
		t.Fatalf("invalid passthrough = %v", err)
	}
	if _, err := client.Read(ctx, scope, "public/none"); !errors.As(err, &operationErr) || operationErr.Code != filesoperator.CodeNotFound {
		t.Fatalf("not-found passthrough = %v", err)
	}
}

func TestFilesAgentUploadAndDownloadStreaming(t *testing.T) {
	client, scope, socketPath, token := startFilesAgent(t)
	ctx := context.Background()

	payload := strings.Repeat("streaming-payload/", 4096)
	upload, err := client.UploadBegin(ctx, scope, "public/large.bin", int64(len(payload)), false)
	if err != nil {
		t.Fatalf("UploadBegin = %v", err)
	}
	half := len(payload) / 2
	if err := client.UploadChunk(ctx, scope, upload.UploadID, 0, strings.NewReader(payload[:half])); err != nil {
		t.Fatalf("first chunk = %v", err)
	}

	// A wrong offset surfaces the current offset for resumption.
	err = client.UploadChunk(ctx, scope, upload.UploadID, 0, strings.NewReader(payload[:half]))
	var operationErr *filesoperator.OperationError
	if !errors.As(err, &operationErr) || operationErr.Code != filesoperator.CodeOffsetConflict || operationErr.Offset != int64(half) {
		t.Fatalf("offset conflict = %+v", err)
	}

	if err := client.UploadChunk(ctx, scope, upload.UploadID, int64(half), strings.NewReader(payload[half:])); err != nil {
		t.Fatalf("second chunk = %v", err)
	}
	entry, err := client.UploadCommit(ctx, scope, upload.UploadID)
	if err != nil || entry.Size != int64(len(payload)) {
		t.Fatalf("UploadCommit = %+v, %v", entry, err)
	}

	reader, downloaded, err := client.Download(ctx, scope, "public/large.bin")
	if err != nil {
		t.Fatalf("Download = %v", err)
	}
	defer reader.Close()
	if downloaded.Name != "large.bin" || downloaded.Size != int64(len(payload)) {
		t.Fatalf("download entry = %+v", downloaded)
	}
	var received bytes.Buffer
	if _, err := io.Copy(&received, reader); err != nil || received.String() != payload {
		t.Fatalf("downloaded %d bytes, %v", received.Len(), err)
	}

	aborted, err := client.UploadBegin(ctx, scope, "public/aborted.bin", 16, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.UploadAbort(ctx, scope, aborted.UploadID); err != nil {
		t.Fatalf("UploadAbort = %v", err)
	}

	// Malformed offsets are rejected before touching the operator.
	raw := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
	}}}
	request, err := http.NewRequest(http.MethodPut, "http://unix/v1/files/uploads/"+upload.UploadID+"?offset=nonsense", strings.NewReader("x"))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := raw.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad offset status = %d", response.StatusCode)
	}

	// Authentication stays enforced on the streaming routes.
	unauthenticated, err := http.NewRequest(http.MethodGet, "http://unix/v1/files/download?path=public/large.bin", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err = raw.Do(unauthenticated)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated download status = %d", response.StatusCode)
	}
}
