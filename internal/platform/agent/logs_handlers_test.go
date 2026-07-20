package agent

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/platform/agentauth"
	logsoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/logs"
	nodeoperator "github.com/nexa-panel/nexa-panel/internal/platform/operators/nodes"
	"github.com/nexa-panel/nexa-panel/internal/platform/operators/sitefs"
)

func startLogsAgent(t *testing.T) (*logsoperator.UnixClient, sitefs.Scope, string, string) {
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
	if err := os.MkdirAll(filepath.Join(scope.RootPath, "logs", "tasks"), 0o750); err != nil {
		t.Fatal(err)
	}
	hostOperator, err := logsoperator.NewHostOperator(siteRoot)
	if err != nil {
		t.Fatal(err)
	}

	nodeOperator, _ := nodeoperator.NewFileOperator(filepath.Join(directory, "probe.conf"))
	server := New(socketPath, "test", token, nodeOperator, slog.New(slog.NewTextHandler(io.Discard, nil)), WithLogsOperator(hostOperator))
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
	return logsoperator.NewUnixClient(socketPath, tokenPath), scope, socketPath, token
}

func TestLogsAgentRoundTrip(t *testing.T) {
	client, scope, _, _ := startLogsAgent(t)
	ctx := context.Background()
	writeLog := func(relative, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(scope.RootPath, "logs", filepath.FromSlash(relative)), []byte(content), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	writeLog("access.log", "GET / 200\nGET /admin 403\npartial")
	writeLog("tasks/1234.log", "task ran\n")

	listing, err := client.List(ctx, scope)
	if err != nil {
		t.Fatalf("List = %v", err)
	}
	names := make([]string, 0, len(listing.Items))
	for _, item := range listing.Items {
		names = append(names, item.Name)
	}
	if !reflect.DeepEqual(names, []string{"access.log", "tasks/1234.log"}) {
		t.Fatalf("List names = %v", names)
	}

	tail, err := client.Read(ctx, scope, logsoperator.ReadRequest{Name: "access.log"})
	if err != nil {
		t.Fatalf("tail Read = %v", err)
	}
	if !reflect.DeepEqual(tail.Lines, []string{"GET / 200", "GET /admin 403", "partial"}) || tail.NextOffset != tail.Size {
		t.Fatalf("tail = %+v", tail)
	}

	offset := int64(0)
	incremental, err := client.Read(ctx, scope, logsoperator.ReadRequest{Name: "access.log", FromOffset: &offset, Filter: "403"})
	if err != nil {
		t.Fatalf("incremental Read = %v", err)
	}
	if !reflect.DeepEqual(incremental.Lines, []string{"GET /admin 403"}) || incremental.NextOffset != 25 {
		t.Fatalf("incremental = %+v", incremental)
	}

	// Rotation survives the agent boundary.
	writeLog("access.log", "new\n")
	rotated, err := client.Read(ctx, scope, logsoperator.ReadRequest{Name: "access.log", FromOffset: &incremental.NextOffset})
	if err != nil || !rotated.Rotated || rotated.NextOffset != 0 {
		t.Fatalf("rotated = %+v, %v", rotated, err)
	}

	// Error kinds survive the agent boundary.
	var operationErr *logsoperator.OperationError
	if _, err := client.Read(ctx, scope, logsoperator.ReadRequest{Name: "missing.log"}); !errors.As(err, &operationErr) || operationErr.Code != logsoperator.CodeNotFound {
		t.Fatalf("not-found passthrough = %v", err)
	}
	if _, err := client.Read(ctx, scope, logsoperator.ReadRequest{Name: "../secret"}); !errors.As(err, &operationErr) || operationErr.Code != logsoperator.CodeInvalid {
		t.Fatalf("invalid passthrough = %v", err)
	}
	if _, _, err := client.Download(ctx, scope, ".hidden"); !errors.As(err, &operationErr) || operationErr.Code != logsoperator.CodeInvalid {
		t.Fatalf("hidden download passthrough = %v", err)
	}
}

func TestLogsAgentDownloadStreamingAndAuth(t *testing.T) {
	client, scope, socketPath, _ := startLogsAgent(t)
	ctx := context.Background()

	payload := strings.Repeat("log line payload\n", 8192)
	if err := os.WriteFile(filepath.Join(scope.RootPath, "logs", "error.log"), []byte(payload), 0o640); err != nil {
		t.Fatal(err)
	}

	reader, entry, err := client.Download(ctx, scope, "error.log")
	if err != nil {
		t.Fatalf("Download = %v", err)
	}
	defer reader.Close()
	if entry.Name != "error.log" || entry.Size != int64(len(payload)) {
		t.Fatalf("download entry = %+v", entry)
	}
	received, err := io.ReadAll(reader)
	if err != nil || string(received) != payload {
		t.Fatalf("downloaded %d bytes, %v", len(received), err)
	}

	// Authentication stays enforced on the streaming route.
	raw := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
	}}}
	unauthenticated, err := http.NewRequest(http.MethodGet, "http://unix/v1/logs/download?name=error.log", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := raw.Do(unauthenticated)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated download status = %d", response.StatusCode)
	}
}
