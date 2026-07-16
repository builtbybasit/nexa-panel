package files

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nexa-panel/nexa-panel/internal/platform/operators/sitefs"
)

type recordingOwnership struct{ paths []string }

func (o *recordingOwnership) Chown(path, _ string) error {
	o.paths = append(o.paths, path)
	return nil
}

func (o *recordingOwnership) chowned(path string) bool {
	for _, candidate := range o.paths {
		if candidate == path {
			return true
		}
	}
	return false
}

func newTestOperator(t *testing.T) (*HostOperator, sitefs.Scope, *recordingOwnership) {
	t.Helper()
	siteRoot := t.TempDir()
	scope := sitefs.Scope{SiteID: "site_1", Slug: "demo", RootPath: filepath.Join(siteRoot, "demo"), UnixUser: "nexa_demo"}
	for _, directory := range []string{"public", "private", "tmp", "backups", "logs"} {
		if err := os.MkdirAll(filepath.Join(scope.RootPath, directory), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	owner := &recordingOwnership{}
	operator, err := NewHostOperator(siteRoot, owner)
	if err != nil {
		t.Fatal(err)
	}
	return operator, scope, owner
}

func seedFile(t *testing.T, scope sitefs.Scope, relative, content string) string {
	t.Helper()
	full := filepath.Join(scope.RootPath, filepath.FromSlash(relative))
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

func etagOf(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

func TestScopeAndPathConfinement(t *testing.T) {
	operator, scope, _ := newTestOperator(t)
	ctx := context.Background()
	seedFile(t, scope, "public/index.php", "<?php\n")

	for name, raw := range map[string]string{"traversal": "../other", "absolute": "/etc/passwd", "nul": "public/a\x00b", "deep traversal": "public/../../escape"} {
		if _, err := operator.List(ctx, scope, raw); err == nil || operationCode(t, err) != CodeInvalid {
			t.Errorf("List(%s %q) error = %v, want invalid", name, raw, err)
		}
	}
	if listing, err := operator.List(ctx, scope, ""); err != nil || len(listing.Entries) == 0 {
		t.Fatalf("List(root) = %+v, %v", listing, err)
	}

	badScope := scope
	badScope.RootPath = "/etc"
	if _, err := operator.List(ctx, badScope, ""); err == nil || operationCode(t, err) != CodeInvalid {
		t.Fatalf("tampered scope error = %v, want invalid", err)
	}

	seedFile(t, scope, "public/ölçü-ünïcode.txt", "unicode content")
	read, err := operator.Read(ctx, scope, "public/ölçü-ünïcode.txt")
	if err != nil || read.Content != "unicode content" {
		t.Fatalf("Read(unicode) = %+v, %v", read, err)
	}
}

func TestSymlinkRules(t *testing.T) {
	operator, scope, _ := newTestOperator(t)
	ctx := context.Background()
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.MkdirAll(outside, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(scope.RootPath, "public", "escape")); err != nil {
		t.Fatal(err)
	}

	if _, err := operator.Read(ctx, scope, "public/escape/secret.txt"); err == nil {
		t.Fatal("reading through an escaping symlink should fail")
	}
	if _, err := operator.List(ctx, scope, "public/escape"); err == nil {
		t.Fatal("listing through an escaping symlink should fail")
	}
	if _, err := operator.Write(ctx, scope, "public/escape/evil.txt", "x", ""); err == nil {
		t.Fatal("writing through an escaping symlink should fail")
	}

	entry, err := operator.Stat(ctx, scope, "public/escape")
	if err != nil || entry.Kind != "symlink" {
		t.Fatalf("Stat(symlink) = %+v, %v", entry, err)
	}
	if _, err := operator.Write(ctx, scope, "public/escape", "x", ""); err == nil {
		t.Fatal("writing onto a symlink should fail")
	}
	if err := operator.Delete(ctx, scope, "public/escape", false); err != nil {
		t.Fatalf("deleting the symlink itself should succeed: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(outside, "secret.txt")); err != nil {
		t.Fatal("symlink target must survive symlink deletion")
	}
}

func TestWriteZoneEnforcement(t *testing.T) {
	operator, scope, _ := newTestOperator(t)
	ctx := context.Background()
	seedFile(t, scope, "logs/access.log", "log line\n")

	blocked := []string{"logs/access.log", "logs/new.log", "public", "private", "tmp", "backups", "logs", "root.txt", "."}
	for _, target := range blocked {
		if _, err := operator.Write(ctx, scope, target, "x", ""); err == nil || operationCode(t, err) != CodeInvalid {
			t.Errorf("Write(%q) error = %v, want write-zone rejection", target, err)
		}
	}
	if _, err := operator.Mkdir(ctx, scope, "newdir"); err == nil || operationCode(t, err) != CodeInvalid {
		t.Fatalf("Mkdir at root depth error = %v, want invalid", err)
	}
	if err := operator.Delete(ctx, scope, "logs/access.log", false); err == nil || operationCode(t, err) != CodeInvalid {
		t.Fatalf("Delete(logs) error = %v, want invalid", err)
	}

	// Reads remain allowed everywhere, including logs/.
	if read, err := operator.Read(ctx, scope, "logs/access.log"); err != nil || read.Content != "log line\n" {
		t.Fatalf("Read(logs) = %+v, %v", read, err)
	}
	if _, err := operator.List(ctx, scope, "logs"); err != nil {
		t.Fatalf("List(logs) = %v", err)
	}
}

func TestWriteETagLifecycle(t *testing.T) {
	operator, scope, owner := newTestOperator(t)
	ctx := context.Background()

	created, err := operator.Write(ctx, scope, "public/config.php", "v1", "")
	if err != nil || created.ETag != etagOf("v1") {
		t.Fatalf("create Write = %+v, %v", created, err)
	}
	if !owner.chowned(filepath.Join(scope.RootPath, "public", "config.php")) {
		t.Fatal("created file was not chowned")
	}

	if _, err := operator.Write(ctx, scope, "public/config.php", "v2", ""); operationCode(t, err) != CodeExistsConflict {
		t.Fatalf("blind overwrite error = %v, want exists conflict", err)
	}
	if _, err := operator.Write(ctx, scope, "public/config.php", "v2", etagOf("stale")); operationCode(t, err) != CodeETagConflict {
		t.Fatalf("stale etag error = %v, want etag conflict", err)
	}
	updated, err := operator.Write(ctx, scope, "public/config.php", "v2", created.ETag)
	if err != nil || updated.ETag != etagOf("v2") {
		t.Fatalf("etag Write = %+v, %v", updated, err)
	}
	if content, err := os.ReadFile(filepath.Join(scope.RootPath, "public", "config.php")); err != nil || string(content) != "v2" {
		t.Fatalf("file content = %q, %v", content, err)
	}
	if _, err := operator.Write(ctx, scope, "public/missing.php", "x", etagOf("v1")); operationCode(t, err) != CodeETagConflict {
		t.Fatalf("etag on missing file error = %v, want etag conflict", err)
	}
	if _, err := operator.Write(ctx, scope, "public/too-big.txt", strings.Repeat("a", writeMaxBytes+1), ""); operationCode(t, err) != CodeInvalid {
		t.Fatalf("oversized write error = %v, want invalid", err)
	}
}

func TestReadBinaryAndTruncation(t *testing.T) {
	operator, scope, _ := newTestOperator(t)
	ctx := context.Background()

	seedFile(t, scope, "public/binary.dat", "PNG\x00binary")
	binary, err := operator.Read(ctx, scope, "public/binary.dat")
	if err != nil || !binary.Binary || binary.Content != "" {
		t.Fatalf("Read(binary) = %+v, %v", binary, err)
	}

	large := strings.Repeat("x", readMaxBytes+10)
	seedFile(t, scope, "public/large.txt", large)
	truncated, err := operator.Read(ctx, scope, "public/large.txt")
	if err != nil || !truncated.Truncated || truncated.Content != "" || truncated.Size != int64(len(large)) {
		t.Fatalf("Read(large) = truncated=%v content=%d size=%d, %v", truncated.Truncated, len(truncated.Content), truncated.Size, err)
	}
	if truncated.ETag != etagOf(large) {
		t.Fatal("etag must cover the full content even when truncated")
	}

	if _, err := operator.Read(ctx, scope, "public"); operationCode(t, err) != CodeInvalid {
		t.Fatalf("Read(directory) error = %v, want invalid", err)
	}
	if _, err := operator.Read(ctx, scope, "public/absent.txt"); operationCode(t, err) != CodeNotFound {
		t.Fatalf("Read(absent) error = %v, want not found", err)
	}
}

func TestListOrderingAndTruncation(t *testing.T) {
	operator, scope, _ := newTestOperator(t)
	ctx := context.Background()
	seedFile(t, scope, "public/b.txt", "b")
	seedFile(t, scope, "public/a.txt", "a")
	if err := os.Mkdir(filepath.Join(scope.RootPath, "public", "zdir"), 0o750); err != nil {
		t.Fatal(err)
	}
	listing, err := operator.List(ctx, scope, "public")
	if err != nil || len(listing.Entries) != 3 {
		t.Fatalf("List = %+v, %v", listing, err)
	}
	if listing.Entries[0].Name != "zdir" || listing.Entries[0].Kind != "dir" || listing.Entries[1].Name != "a.txt" || listing.Entries[2].Name != "b.txt" {
		t.Fatalf("ordering = %s, %s, %s; want dirs first then names", listing.Entries[0].Name, listing.Entries[1].Name, listing.Entries[2].Name)
	}

	for index := range listMaxEntries + 1 {
		seedFile(t, scope, fmt.Sprintf("private/f%05d", index), "")
	}
	truncated, err := operator.List(ctx, scope, "private")
	if err != nil || !truncated.Truncated || len(truncated.Entries) != listMaxEntries {
		t.Fatalf("List(overfull) entries=%d truncated=%v, %v", len(truncated.Entries), truncated.Truncated, err)
	}
}

func TestMkdirCreatesAndChownsComponents(t *testing.T) {
	operator, scope, owner := newTestOperator(t)
	ctx := context.Background()
	entry, err := operator.Mkdir(ctx, scope, "public/a/b/c")
	if err != nil || entry.Kind != "dir" || entry.Name != "c" {
		t.Fatalf("Mkdir = %+v, %v", entry, err)
	}
	for _, created := range []string{"public/a", "public/a/b", "public/a/b/c"} {
		if !owner.chowned(filepath.Join(scope.RootPath, filepath.FromSlash(created))) {
			t.Errorf("created directory %s was not chowned", created)
		}
	}
	if _, err := operator.Mkdir(ctx, scope, "public/a/b/c"); err != nil {
		t.Fatalf("Mkdir on existing directory = %v, want success", err)
	}
	seedFile(t, scope, "public/file.txt", "x")
	if _, err := operator.Mkdir(ctx, scope, "public/file.txt/sub"); operationCode(t, err) != CodeInvalid {
		t.Fatalf("Mkdir through file error = %v, want invalid", err)
	}
}

func TestMoveSemantics(t *testing.T) {
	operator, scope, _ := newTestOperator(t)
	ctx := context.Background()
	seedFile(t, scope, "public/from.txt", "payload")
	seedFile(t, scope, "public/existing.txt", "other")

	if _, err := operator.Move(ctx, scope, "public/from.txt", "public/existing.txt", false); operationCode(t, err) != CodeExistsConflict {
		t.Fatalf("move onto existing error = %v, want exists conflict", err)
	}
	entry, err := operator.Move(ctx, scope, "public/from.txt", "public/existing.txt", true)
	if err != nil || entry.Name != "existing.txt" {
		t.Fatalf("overwrite move = %+v, %v", entry, err)
	}
	if content, _ := os.ReadFile(filepath.Join(scope.RootPath, "public", "existing.txt")); string(content) != "payload" {
		t.Fatalf("moved content = %q", content)
	}
	if _, err := os.Lstat(filepath.Join(scope.RootPath, "public", "from.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("source must be gone after move")
	}

	if err := os.Mkdir(filepath.Join(scope.RootPath, "public", "dir"), 0o750); err != nil {
		t.Fatal(err)
	}
	if _, err := operator.Move(ctx, scope, "public/dir", "public/existing.txt", true); operationCode(t, err) != CodeInvalid {
		t.Fatalf("dir onto file error = %v, want invalid", err)
	}
	if _, err := operator.Move(ctx, scope, "public/dir", "public/dir/sub", false); operationCode(t, err) != CodeInvalid {
		t.Fatalf("dir into itself error = %v, want invalid", err)
	}
	if _, err := operator.Move(ctx, scope, "public/existing.txt", "logs/evil.txt", false); operationCode(t, err) != CodeInvalid {
		t.Fatalf("move outside write zone error = %v, want invalid", err)
	}
	if _, err := operator.Move(ctx, scope, "public/missing.txt", "public/target.txt", false); operationCode(t, err) != CodeNotFound {
		t.Fatalf("move missing error = %v, want not found", err)
	}
}

func TestCopyTreeSkipsSpecialAndEnforcesCaps(t *testing.T) {
	operator, scope, owner := newTestOperator(t)
	ctx := context.Background()
	seedFile(t, scope, "public/app/a.txt", "alpha")
	seedFile(t, scope, "public/app/sub/b.txt", "beta")
	if err := os.Symlink("/etc", filepath.Join(scope.RootPath, "public", "app", "link")); err != nil {
		t.Fatal(err)
	}

	result, err := operator.Copy(ctx, scope, "public/app", "backups/app-copy")
	if err != nil {
		t.Fatalf("Copy = %v", err)
	}
	if result.Copied != 4 || result.Skipped != 1 || result.Bytes != int64(len("alpha")+len("beta")) {
		t.Fatalf("Copy result = %+v", result)
	}
	if content, err := os.ReadFile(filepath.Join(scope.RootPath, "backups", "app-copy", "sub", "b.txt")); err != nil || string(content) != "beta" {
		t.Fatalf("copied content = %q, %v", content, err)
	}
	if !owner.chowned(filepath.Join(scope.RootPath, "backups", "app-copy", "a.txt")) {
		t.Fatal("copied file was not chowned")
	}
	if _, err := os.Lstat(filepath.Join(scope.RootPath, "backups", "app-copy", "link")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("symlink must not be copied")
	}

	if _, err := operator.Copy(ctx, scope, "public/app", "backups/app-copy"); operationCode(t, err) != CodeExistsConflict {
		t.Fatalf("copy onto existing error = %v, want exists conflict", err)
	}
	if _, err := operator.Copy(ctx, scope, "public/app", "public/app/nested"); operationCode(t, err) != CodeInvalid {
		t.Fatalf("copy into itself error = %v, want invalid", err)
	}

	sparse := filepath.Join(scope.RootPath, "public", "huge.bin")
	if err := os.WriteFile(sparse, nil, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(sparse, copyMaxBytes+1); err != nil {
		t.Fatal(err)
	}
	if _, err := operator.Copy(ctx, scope, "public/huge.bin", "backups/huge.bin"); operationCode(t, err) != CodeInvalid {
		t.Fatalf("copy above byte cap error = %v, want invalid", err)
	}

	// Copy may read from outside the write zone as long as it lands inside it.
	seedFile(t, scope, "logs/old.log", "history")
	if _, err := operator.Copy(ctx, scope, "logs/old.log", "backups/old.log"); err != nil {
		t.Fatalf("copy from logs = %v", err)
	}
}

func TestDeleteRecursiveGuard(t *testing.T) {
	operator, scope, _ := newTestOperator(t)
	ctx := context.Background()
	seedFile(t, scope, "public/dir/inner.txt", "x")

	if err := operator.Delete(ctx, scope, "public/dir", false); err == nil || operationCode(t, err) != CodeInvalid {
		t.Fatalf("non-recursive delete of full directory error = %v, want invalid", err)
	}
	if err := operator.Delete(ctx, scope, "public/dir", true); err != nil {
		t.Fatalf("recursive delete = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(scope.RootPath, "public", "dir")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("directory must be removed")
	}
	if err := operator.Delete(ctx, scope, "public/dir", true); operationCode(t, err) != CodeNotFound {
		t.Fatalf("delete missing error = %v, want not found", err)
	}
}

func TestUploadLifecycle(t *testing.T) {
	operator, scope, owner := newTestOperator(t)
	ctx := context.Background()

	upload, err := operator.UploadBegin(ctx, scope, "public/media.bin", 10, false)
	if err != nil || len(upload.UploadID) != 32 {
		t.Fatalf("UploadBegin = %+v, %v", upload, err)
	}
	if err := operator.UploadChunk(ctx, scope, upload.UploadID, 0, strings.NewReader("01234")); err != nil {
		t.Fatalf("first chunk = %v", err)
	}

	err = operator.UploadChunk(ctx, scope, upload.UploadID, 3, strings.NewReader("xx"))
	var offsetErr *OperationError
	if !errors.As(err, &offsetErr) || offsetErr.Code != CodeOffsetConflict || offsetErr.Offset != 5 {
		t.Fatalf("offset mismatch error = %+v", err)
	}

	if _, err := operator.UploadCommit(ctx, scope, upload.UploadID); operationCode(t, err) != CodeInvalid {
		t.Fatalf("premature commit error = %v, want invalid", err)
	}
	if err := operator.UploadChunk(ctx, scope, upload.UploadID, 5, strings.NewReader("56789overflow")); operationCode(t, err) != CodeInvalid {
		t.Fatalf("overflow chunk error = %v, want invalid", err)
	}
	if err := operator.UploadChunk(ctx, scope, upload.UploadID, 5, strings.NewReader("56789")); err != nil {
		t.Fatalf("final chunk = %v", err)
	}
	entry, err := operator.UploadCommit(ctx, scope, upload.UploadID)
	if err != nil || entry.Name != "media.bin" || entry.Size != 10 {
		t.Fatalf("commit = %+v, %v", entry, err)
	}
	if content, err := os.ReadFile(filepath.Join(scope.RootPath, "public", "media.bin")); err != nil || string(content) != "0123456789" {
		t.Fatalf("uploaded content = %q, %v", content, err)
	}
	if !owner.chowned(filepath.Join(scope.RootPath, "public", "media.bin")) {
		t.Fatal("committed upload was not chowned")
	}
	if leftovers, err := filepath.Glob(filepath.Join(scope.RootPath, "tmp", ".nexa-upload-*")); err != nil || len(leftovers) != 0 {
		t.Fatalf("staging leftovers = %v, %v", leftovers, err)
	}

	if _, err := operator.UploadBegin(ctx, scope, "public/media.bin", 4, false); operationCode(t, err) != CodeExistsConflict {
		t.Fatalf("begin onto existing error = %v, want exists conflict", err)
	}
	overwrite, err := operator.UploadBegin(ctx, scope, "public/media.bin", 3, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := operator.UploadChunk(ctx, scope, overwrite.UploadID, 0, strings.NewReader("new")); err != nil {
		t.Fatal(err)
	}
	if _, err := operator.UploadCommit(ctx, scope, overwrite.UploadID); err != nil {
		t.Fatalf("overwrite commit = %v", err)
	}

	if _, err := operator.UploadBegin(ctx, scope, "logs/evil.bin", 4, false); operationCode(t, err) != CodeInvalid {
		t.Fatalf("upload outside write zone error = %v, want invalid", err)
	}
	if _, err := operator.UploadBegin(ctx, scope, "public/too-big.bin", uploadMaxBytes+1, false); operationCode(t, err) != CodeInvalid {
		t.Fatalf("oversized upload error = %v, want invalid", err)
	}
	if err := operator.UploadChunk(ctx, scope, "not-an-id", 0, strings.NewReader("x")); operationCode(t, err) != CodeInvalid {
		t.Fatalf("bad upload id error = %v, want invalid", err)
	}
	if err := operator.UploadChunk(ctx, scope, strings.Repeat("f", 32), 0, strings.NewReader("x")); operationCode(t, err) != CodeNotFound {
		t.Fatalf("unknown upload id error = %v, want not found", err)
	}
}

func TestUploadAbortAndStaleCleanup(t *testing.T) {
	operator, scope, _ := newTestOperator(t)
	ctx := context.Background()

	upload, err := operator.UploadBegin(ctx, scope, "public/abort.bin", 8, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := operator.UploadAbort(ctx, scope, upload.UploadID); err != nil {
		t.Fatalf("abort = %v", err)
	}
	if err := operator.UploadAbort(ctx, scope, upload.UploadID); err != nil {
		t.Fatalf("abort must be idempotent: %v", err)
	}
	if err := operator.UploadChunk(ctx, scope, upload.UploadID, 0, strings.NewReader("x")); operationCode(t, err) != CodeNotFound {
		t.Fatalf("chunk after abort error = %v, want not found", err)
	}

	stale, err := operator.UploadBegin(ctx, scope, "public/stale.bin", 8, false)
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := operator.UploadBegin(ctx, scope, "public/fresh.bin", 8, false)
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * uploadStaleAfter)
	for _, suffix := range []string{"", ".meta"} {
		if err := os.Chtimes(filepath.Join(scope.RootPath, "tmp", ".nexa-upload-"+stale.UploadID+suffix), old, old); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := operator.UploadBegin(ctx, scope, "public/trigger.bin", 8, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(scope.RootPath, "tmp", ".nexa-upload-"+stale.UploadID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("stale staging file must be cleaned up")
	}
	if _, err := os.Lstat(filepath.Join(scope.RootPath, "tmp", ".nexa-upload-"+fresh.UploadID)); err != nil {
		t.Fatal("fresh staging file must survive cleanup")
	}
}

func TestDownloadStreamsRegularFilesOnly(t *testing.T) {
	operator, scope, _ := newTestOperator(t)
	ctx := context.Background()
	seedFile(t, scope, "public/asset.css", "body{}")

	reader, entry, err := operator.Download(ctx, scope, "public/asset.css")
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	content, err := io.ReadAll(reader)
	if err != nil || string(content) != "body{}" {
		t.Fatalf("downloaded = %q, %v", content, err)
	}
	if entry.Name != "asset.css" || entry.Size != int64(len("body{}")) || entry.Kind != "file" {
		t.Fatalf("entry = %+v", entry)
	}

	if _, _, err := operator.Download(ctx, scope, "public"); operationCode(t, err) != CodeInvalid {
		t.Fatalf("download directory error = %v, want invalid", err)
	}
	if _, _, err := operator.Download(ctx, scope, "public/missing"); operationCode(t, err) != CodeNotFound {
		t.Fatalf("download missing error = %v, want not found", err)
	}
}

func TestArchiveExtractRoundTrip(t *testing.T) {
	operator, scope, _ := newTestOperator(t)
	ctx := context.Background()
	seedFile(t, scope, "public/app/index.php", "<?php echo 1;\n")
	seedFile(t, scope, "public/app/assets/site.css", "body{}\n")
	if err := os.Symlink("/etc", filepath.Join(scope.RootPath, "public", "app", "link")); err != nil {
		t.Fatal(err)
	}

	result, err := operator.Archive(ctx, scope, []string{"public/app", "logs"}, "backups/app.tar.gz")
	if err != nil {
		t.Fatalf("Archive = %v", err)
	}
	// public/app + 2 files + assets dir + logs dir; the symlink is skipped.
	if result.Entries != 5 || result.Bytes != int64(len("<?php echo 1;\n")+len("body{}\n")) {
		t.Fatalf("Archive result = %+v", result)
	}
	if result.Entry.Name != "app.tar.gz" || result.Entry.Kind != "file" {
		t.Fatalf("Archive entry = %+v", result.Entry)
	}

	if _, err := operator.Archive(ctx, scope, []string{"public/app"}, "backups/app.tar.gz"); operationCode(t, err) != CodeExistsConflict {
		t.Fatalf("archive onto existing error = %v, want exists conflict", err)
	}
	if _, err := operator.Archive(ctx, scope, []string{"public/app"}, "backups/app.zip"); operationCode(t, err) != CodeInvalid {
		t.Fatalf("non tar.gz target error = %v, want invalid", err)
	}
	if _, err := operator.Archive(ctx, scope, []string{"public/absent"}, "backups/absent.tar.gz"); operationCode(t, err) != CodeNotFound {
		t.Fatalf("archive of missing path error = %v, want not found", err)
	}

	extracted, err := operator.Extract(ctx, scope, "backups/app.tar.gz", "private/restore")
	if err != nil {
		t.Fatalf("Extract = %v", err)
	}
	if extracted.Written != 5 || extracted.Skipped != 0 {
		t.Fatalf("Extract result = %+v", extracted)
	}
	for relative, want := range map[string]string{"private/restore/public/app/index.php": "<?php echo 1;\n", "private/restore/public/app/assets/site.css": "body{}\n"} {
		content, err := os.ReadFile(filepath.Join(scope.RootPath, filepath.FromSlash(relative)))
		if err != nil || string(content) != want {
			t.Fatalf("extracted %s = %q, %v", relative, content, err)
		}
	}
}

func buildTar(t *testing.T, build func(*tar.Writer)) []byte {
	t.Helper()
	var buffer bytes.Buffer
	compressor := gzip.NewWriter(&buffer)
	writer := tar.NewWriter(compressor)
	build(writer)
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := compressor.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func TestExtractRejectsHostileEntries(t *testing.T) {
	operator, scope, _ := newTestOperator(t)
	ctx := context.Background()

	hostile := buildTar(t, func(writer *tar.Writer) {
		for _, entry := range []struct{ name, content string }{{"../evil.txt", "evil"}, {"/abs/evil.txt", "evil"}, {"ok.txt", "fine"}} {
			_ = writer.WriteHeader(&tar.Header{Name: entry.name, Typeflag: tar.TypeReg, Size: int64(len(entry.content)), Mode: 0o644})
			_, _ = writer.Write([]byte(entry.content))
		}
		_ = writer.WriteHeader(&tar.Header{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd", Mode: 0o777})
	})
	seedFile(t, scope, "backups/hostile.tar.gz", string(hostile))

	result, err := operator.Extract(ctx, scope, "backups/hostile.tar.gz", "public/unpacked")
	if err != nil {
		t.Fatalf("Extract = %v", err)
	}
	if result.Written != 1 || result.Skipped != 3 {
		t.Fatalf("Extract result = %+v, want 1 written and 3 skipped", result)
	}
	if content, err := os.ReadFile(filepath.Join(scope.RootPath, "public", "unpacked", "ok.txt")); err != nil || string(content) != "fine" {
		t.Fatalf("safe entry = %q, %v", content, err)
	}
	if _, err := os.Lstat(filepath.Join(scope.RootPath, "..", "evil.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("hostile entry escaped the site root")
	}
}

func TestExtractBombCaps(t *testing.T) {
	operator, scope, _ := newTestOperator(t)
	ctx := context.Background()

	// The header declares a huge entry without body bytes; the size check must
	// fire before any content is read or written.
	var oversized bytes.Buffer
	compressor := gzip.NewWriter(&oversized)
	writer := tar.NewWriter(compressor)
	if err := writer.WriteHeader(&tar.Header{Name: "bomb.bin", Typeflag: tar.TypeReg, Size: extractMaxBytes + 1, Mode: 0o644}); err != nil {
		t.Fatal(err)
	}
	if err := compressor.Close(); err != nil {
		t.Fatal(err)
	}
	seedFile(t, scope, "backups/bytes-bomb.tar.gz", oversized.String())
	if _, err := operator.Extract(ctx, scope, "backups/bytes-bomb.tar.gz", "public/bomb"); operationCode(t, err) != CodeInvalid {
		t.Fatalf("byte bomb error = %v, want invalid", err)
	}
	if _, err := os.Lstat(filepath.Join(scope.RootPath, "public", "bomb", "bomb.bin")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("oversized entry must be rejected before writing")
	}

	crowded := buildTar(t, func(writer *tar.Writer) {
		for index := range extractMaxEntries + 1 {
			_ = writer.WriteHeader(&tar.Header{Name: fmt.Sprintf("f%06d", index), Typeflag: tar.TypeReg, Size: 0, Mode: 0o644})
		}
	})
	seedFile(t, scope, "backups/entries-bomb.tar.gz", string(crowded))
	if _, err := operator.Extract(ctx, scope, "backups/entries-bomb.tar.gz", "public/crowded"); operationCode(t, err) != CodeInvalid {
		t.Fatalf("entry bomb error = %v, want invalid", err)
	}

	if _, err := operator.Extract(ctx, scope, "backups/bytes-bomb.tar.gz", "logs/unpacked"); operationCode(t, err) != CodeInvalid {
		t.Fatalf("extract outside write zone error = %v, want invalid", err)
	}
}

func TestExtractZip(t *testing.T) {
	operator, scope, _ := newTestOperator(t)
	ctx := context.Background()

	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	safe, err := writer.Create("nested/ok.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := safe.Write([]byte("zipped")); err != nil {
		t.Fatal(err)
	}
	hostile, err := writer.Create("../evil.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := hostile.Write([]byte("evil")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	seedFile(t, scope, "backups/site.zip", buffer.String())

	result, err := operator.Extract(ctx, scope, "backups/site.zip", "public/unzipped")
	if err != nil {
		t.Fatalf("Extract(zip) = %v", err)
	}
	if result.Written != 1 || result.Skipped != 1 {
		t.Fatalf("Extract(zip) result = %+v", result)
	}
	if content, err := os.ReadFile(filepath.Join(scope.RootPath, "public", "unzipped", "nested", "ok.txt")); err != nil || string(content) != "zipped" {
		t.Fatalf("unzipped content = %q, %v", content, err)
	}

	seedFile(t, scope, "backups/not-archive.rar", "junk")
	if _, err := operator.Extract(ctx, scope, "backups/not-archive.rar", "public/x"); operationCode(t, err) != CodeInvalid {
		t.Fatalf("unsupported archive error = %v, want invalid", err)
	}
}

func TestDirectorySize(t *testing.T) {
	operator, scope, _ := newTestOperator(t)
	ctx := context.Background()
	seedFile(t, scope, "public/a.txt", "12345")
	seedFile(t, scope, "public/sub/b.txt", "123")
	seedFile(t, scope, "public/sub/deep/c.txt", "1")

	result, err := operator.DirectorySize(ctx, scope, "public")
	if err != nil {
		t.Fatal(err)
	}
	if result.Bytes != 9 || result.Files != 3 || result.Dirs != 2 || result.Truncated {
		t.Fatalf("DirectorySize = %+v", result)
	}
	if _, err := operator.DirectorySize(ctx, scope, "public/a.txt"); operationCode(t, err) != CodeInvalid {
		t.Fatalf("size of file error = %v, want invalid", err)
	}
	if _, err := operator.DirectorySize(ctx, scope, "absent"); operationCode(t, err) != CodeNotFound {
		t.Fatalf("size of missing error = %v, want not found", err)
	}
}
