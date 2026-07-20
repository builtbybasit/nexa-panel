package logs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/nexa-panel/nexa-panel/internal/platform/operators/sitefs"
)

func newTestOperator(t *testing.T) (*HostOperator, sitefs.Scope) {
	t.Helper()
	siteRoot := t.TempDir()
	scope := sitefs.Scope{SiteID: "site_1", Slug: "demo", RootPath: filepath.Join(siteRoot, "demo"), UnixUser: "nexa_demo"}
	for _, directory := range []string{"public", "logs", "logs/tasks"} {
		if err := os.MkdirAll(filepath.Join(scope.RootPath, directory), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	operator, err := NewHostOperator(siteRoot)
	if err != nil {
		t.Fatal(err)
	}
	return operator, scope
}

func seedLog(t *testing.T, scope sitefs.Scope, relative, content string) string {
	t.Helper()
	full := filepath.Join(scope.RootPath, "logs", filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o640); err != nil {
		t.Fatal(err)
	}
	return full
}

func operationCode(t *testing.T, err error) string {
	t.Helper()
	var operationErr *OperationError
	if !errors.As(err, &operationErr) {
		t.Fatalf("error %v is not an OperationError", err)
	}
	return operationErr.Code
}

func offsetOf(value int64) *int64 { return &value }

func TestTailReadMath(t *testing.T) {
	operator, scope := newTestOperator(t)
	ctx := context.Background()

	seedLog(t, scope, "empty.log", "")
	result, err := operator.Read(ctx, scope, ReadRequest{Name: "empty.log"})
	if err != nil {
		t.Fatalf("empty tail: %v", err)
	}
	if len(result.Lines) != 0 || result.NextOffset != 0 || result.Size != 0 || result.Truncated || result.Rotated {
		t.Fatalf("empty tail = %+v", result)
	}

	// A file smaller than MaxBytes yields every line, including a trailing
	// partial line still being written.
	seedLog(t, scope, "small.log", "alpha\nbeta\ngamma")
	result, err = operator.Read(ctx, scope, ReadRequest{Name: "small.log"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.Lines, []string{"alpha", "beta", "gamma"}) || result.NextOffset != 16 || result.Size != 16 || result.Truncated {
		t.Fatalf("small tail = %+v", result)
	}

	// TailLines keeps only the newest lines.
	result, err = operator.Read(ctx, scope, ReadRequest{Name: "small.log", TailLines: 2})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.Lines, []string{"beta", "gamma"}) {
		t.Fatalf("tail lines = %+v", result)
	}

	// A window starting mid-line drops the leading partial line.
	seedLog(t, scope, "window.log", "abcdef\nghi\n") // 11 bytes
	result, err = operator.Read(ctx, scope, ReadRequest{Name: "window.log", MaxBytes: 8})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.Lines, []string{"ghi"}) || !result.Truncated || result.NextOffset != 11 {
		t.Fatalf("partial boundary tail = %+v", result)
	}

	// A window starting exactly after a newline keeps its first line whole.
	seedLog(t, scope, "aligned.log", "abc\ndef\n") // 8 bytes
	result, err = operator.Read(ctx, scope, ReadRequest{Name: "aligned.log", MaxBytes: 4})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.Lines, []string{"def"}) || !result.Truncated || result.NextOffset != 8 {
		t.Fatalf("aligned boundary tail = %+v", result)
	}

	// A window that contains no newline is all one oversized partial line.
	seedLog(t, scope, "oneline.log", strings.Repeat("x", 64))
	result, err = operator.Read(ctx, scope, ReadRequest{Name: "oneline.log", MaxBytes: 16})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Lines) != 0 || result.NextOffset != 64 || !result.Truncated {
		t.Fatalf("oversized line tail = %+v", result)
	}

	// CRLF endings are tolerated.
	seedLog(t, scope, "crlf.log", "alpha\r\nbeta\r\n")
	result, err = operator.Read(ctx, scope, ReadRequest{Name: "crlf.log"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.Lines, []string{"alpha", "beta"}) {
		t.Fatalf("crlf tail = %+v", result)
	}
}

func TestIncrementalReadAndRotation(t *testing.T) {
	operator, scope := newTestOperator(t)
	ctx := context.Background()
	full := seedLog(t, scope, "app.log", "one\ntwo\npart")

	// Only complete lines are returned; the offset stops after the last one.
	result, err := operator.Read(ctx, scope, ReadRequest{Name: "app.log", FromOffset: offsetOf(0)})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.Lines, []string{"one", "two"}) || result.NextOffset != 8 || result.Size != 12 || result.Truncated {
		t.Fatalf("incremental = %+v", result)
	}

	// Once the partial line completes, the next poll picks it up whole.
	if err := os.WriteFile(full, []byte("one\ntwo\npartial\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	result, err = operator.Read(ctx, scope, ReadRequest{Name: "app.log", FromOffset: offsetOf(result.NextOffset)})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.Lines, []string{"partial"}) || result.NextOffset != 16 {
		t.Fatalf("follow-up = %+v", result)
	}

	// Caught up: no lines, offset holds.
	result, err = operator.Read(ctx, scope, ReadRequest{Name: "app.log", FromOffset: offsetOf(16)})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Lines) != 0 || result.NextOffset != 16 || result.Rotated {
		t.Fatalf("caught up = %+v", result)
	}

	// A window with no newline yields nothing and does not advance.
	result, err = operator.Read(ctx, scope, ReadRequest{Name: "app.log", FromOffset: offsetOf(8), MaxBytes: 4})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Lines) != 0 || result.NextOffset != 8 || !result.Truncated {
		t.Fatalf("newline-free window = %+v", result)
	}

	// Truncation to a size below the offset signals rotation.
	if err := os.WriteFile(full, []byte("new\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	result, err = operator.Read(ctx, scope, ReadRequest{Name: "app.log", FromOffset: offsetOf(16)})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Rotated || result.NextOffset != 0 || len(result.Lines) != 0 || result.Size != 4 {
		t.Fatalf("rotation = %+v", result)
	}
}

func TestReadMaxBytesTruncation(t *testing.T) {
	operator, scope := newTestOperator(t)
	seedLog(t, scope, "big.log", "aaaa\nbbbb\ncccc\ndddd\n") // 20 bytes

	result, err := operator.Read(context.Background(), scope, ReadRequest{Name: "big.log", FromOffset: offsetOf(0), MaxBytes: 12})
	if err != nil {
		t.Fatal(err)
	}
	// The 12-byte window covers "aaaa\nbbbb\ncc": two complete lines.
	if !reflect.DeepEqual(result.Lines, []string{"aaaa", "bbbb"}) || result.NextOffset != 10 || !result.Truncated {
		t.Fatalf("truncated incremental = %+v", result)
	}
}

func TestReadFilterIsCaseInsensitive(t *testing.T) {
	operator, scope := newTestOperator(t)
	seedLog(t, scope, "app.log", "GET /ok\nERROR boom\nget /still\nerror again\n")
	ctx := context.Background()

	result, err := operator.Read(ctx, scope, ReadRequest{Name: "app.log", Filter: "eRrOr"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.Lines, []string{"ERROR boom", "error again"}) {
		t.Fatalf("tail filter = %+v", result)
	}
	result, err = operator.Read(ctx, scope, ReadRequest{Name: "app.log", FromOffset: offsetOf(0), Filter: "ERROR"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.Lines, []string{"ERROR boom", "error again"}) {
		t.Fatalf("incremental filter = %+v", result)
	}
	if result.NextOffset != result.Size {
		t.Fatalf("filter must not hold back the offset: %+v", result)
	}
}

func TestReadNameValidation(t *testing.T) {
	operator, scope := newTestOperator(t)
	seedLog(t, scope, "access.log", "ok\n")
	ctx := context.Background()

	for _, name := range []string{"", ".", "../public/index.php", "/etc/passwd", ".hidden", "tasks/.hidden", "with space.log", "nul\x00.log", "-dash-first"} {
		if _, err := operator.Read(ctx, scope, ReadRequest{Name: name}); operationCode(t, err) != CodeInvalid {
			t.Fatalf("name %q accepted", name)
		}
		if _, _, err := operator.Download(ctx, scope, name); operationCode(t, err) != CodeInvalid {
			t.Fatalf("download name %q accepted", name)
		}
	}
	if _, err := operator.Read(ctx, scope, ReadRequest{Name: "missing.log"}); operationCode(t, err) != CodeNotFound {
		t.Fatal("missing log must be not-found")
	}
	if _, err := operator.Read(ctx, scope, ReadRequest{Name: "access.log", FromOffset: offsetOf(-1)}); operationCode(t, err) != CodeInvalid {
		t.Fatal("negative offset accepted")
	}
	badScope := scope
	badScope.RootPath = "/elsewhere/demo"
	if _, err := operator.Read(ctx, badScope, ReadRequest{Name: "access.log"}); operationCode(t, err) != CodeInvalid {
		t.Fatal("invalid scope accepted")
	}
}

func TestListDiscoversRegularFilesOnly(t *testing.T) {
	operator, scope := newTestOperator(t)
	ctx := context.Background()
	seedLog(t, scope, "access.log", "a\n")
	seedLog(t, scope, "error.log", "e\n")
	seedLog(t, scope, "tasks/1234.log", "t\n")
	if err := os.Symlink(filepath.Join(scope.RootPath, "public"), filepath.Join(scope.RootPath, "logs", "escape")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(scope.RootPath, "logs", "access.log"), filepath.Join(scope.RootPath, "logs", "link.log")); err != nil {
		t.Fatal(err)
	}

	listing, err := operator.List(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(listing.Items))
	for _, item := range listing.Items {
		names = append(names, item.Name)
	}
	if !reflect.DeepEqual(names, []string{"access.log", "error.log", "tasks/1234.log"}) {
		t.Fatalf("listing = %v", names)
	}
	if listing.Items[0].Size != 2 || listing.Items[0].ModifiedAt.IsZero() {
		t.Fatalf("entry metadata = %+v", listing.Items[0])
	}

	// Symlinks are also refused for reads and downloads, not just hidden.
	if _, err := operator.Read(ctx, scope, ReadRequest{Name: "link.log"}); operationCode(t, err) != CodeInvalid {
		t.Fatal("symlink read accepted")
	}
	if _, _, err := operator.Download(ctx, scope, "link.log"); operationCode(t, err) != CodeInvalid {
		t.Fatal("symlink download accepted")
	}
}

func TestListMissingDirectoriesAndCap(t *testing.T) {
	operator, scope := newTestOperator(t)
	ctx := context.Background()

	// A site without a logs directory has nothing to show — not an error.
	if err := os.RemoveAll(filepath.Join(scope.RootPath, "logs")); err != nil {
		t.Fatal(err)
	}
	listing, err := operator.List(ctx, scope)
	if err != nil || len(listing.Items) != 0 || listing.Items == nil {
		t.Fatalf("missing logs dir = %+v, %v", listing, err)
	}
	if _, err := operator.Read(ctx, scope, ReadRequest{Name: "access.log"}); operationCode(t, err) != CodeNotFound {
		t.Fatal("read without logs dir must be not-found")
	}

	// A missing site directory is a typed not-found, never a *PathError.
	gone := sitefs.Scope{SiteID: "site_2", Slug: "gone", RootPath: filepath.Join(operator.SiteRoot, "gone"), UnixUser: "nexa_gone"}
	if _, err := operator.List(ctx, gone); operationCode(t, err) != CodeNotFound {
		t.Fatal("missing site dir must be not-found")
	}

	// Discovery stops at the entry cap.
	for i := 0; i < ListMaxEntries+10; i++ {
		seedLog(t, scope, fmt.Sprintf("bulk-%04d.log", i), "x\n")
	}
	listing, err = operator.List(ctx, scope)
	if err != nil || len(listing.Items) != ListMaxEntries {
		t.Fatalf("capped listing = %d entries, %v", len(listing.Items), err)
	}
}

func TestDownloadStreams(t *testing.T) {
	operator, scope := newTestOperator(t)
	content := strings.Repeat("log-line\n", 1024)
	seedLog(t, scope, "tasks/abc.log", content)

	reader, entry, err := operator.Download(context.Background(), scope, "tasks/abc.log")
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if entry.Name != "tasks/abc.log" || entry.Size != int64(len(content)) {
		t.Fatalf("entry = %+v", entry)
	}
	downloaded, err := io.ReadAll(reader)
	if err != nil || string(downloaded) != content {
		t.Fatalf("downloaded %d bytes, %v", len(downloaded), err)
	}
}

func TestReadDefaultsAndCaps(t *testing.T) {
	operator, scope := newTestOperator(t)
	lines := make([]string, 0, 300)
	for i := 0; i < 300; i++ {
		lines = append(lines, fmt.Sprintf("line-%03d", i))
	}
	seedLog(t, scope, "many.log", strings.Join(lines, "\n")+"\n")

	// TailLines defaults to 200 and MaxBytes caps requests at 256 KiB.
	result, err := operator.Read(context.Background(), scope, ReadRequest{Name: "many.log", MaxBytes: 100 * ReadMaxBytes, TailLines: 0})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Lines) != ReadDefaultTail || result.Lines[0] != "line-100" || result.Lines[199] != "line-299" {
		t.Fatalf("default tail = %d lines, first %q", len(result.Lines), result.Lines[0])
	}
	result, err = operator.Read(context.Background(), scope, ReadRequest{Name: "many.log", TailLines: ReadMaxTail + 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Lines) != 300 {
		t.Fatalf("capped tail = %d lines", len(result.Lines))
	}
}
