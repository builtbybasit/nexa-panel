package backups

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// seedControlDB creates a small SQLite database with one row so a snapshot can be
// proven to be a real, consistent copy (not a raw file copy of a WAL database).
func seedControlDB(t *testing.T, path string) {
	t.Helper()
	dsn := (&url.URL{Scheme: "file", Path: path}).String() + "?_pragma=journal_mode(WAL)"
	database, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err := database.Exec("CREATE TABLE widgets (id INTEGER PRIMARY KEY, name TEXT)"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec("INSERT INTO widgets (name) VALUES ('alpha')"); err != nil {
		t.Fatal(err)
	}
}

func newSystemOperator(t *testing.T, runner Runner, stateDB, masterKey string) *HostOperator {
	t.Helper()
	operator, err := NewHostOperator(runner, HostConfig{
		StagingRoot: t.TempDir(), StateDBPath: stateDB, MasterKeyPath: masterKey,
	})
	if err != nil {
		t.Fatal(err)
	}
	return operator
}

func TestSnapshotControlDBProducesConsistentCopy(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "control.db")
	seedControlDB(t, source)
	dest := filepath.Join(dir, "snapshot.db")

	if err := snapshotControlDB(context.Background(), source, dest); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	info, err := os.Stat(dest)
	if err != nil {
		t.Fatalf("snapshot missing: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("snapshot perms = %o, want 600", info.Mode().Perm())
	}
	// The snapshot must itself be an openable SQLite DB carrying the seeded row.
	dsn := (&url.URL{Scheme: "file", Path: dest}).String() + "?mode=ro"
	database, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var name string
	if err := database.QueryRow("SELECT name FROM widgets WHERE id = 1").Scan(&name); err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	if name != "alpha" {
		t.Fatalf("snapshot row = %q, want alpha", name)
	}
}

func TestRunSystemUploadsVerifiesAndPrunes(t *testing.T) {
	dir := t.TempDir()
	stateDB := filepath.Join(dir, "control.db")
	seedControlDB(t, stateDB)
	masterKey := filepath.Join(dir, "master.key")
	if err := os.WriteFile(masterKey, []byte("0123456789abcdef0123456789abcdef"), 0o600); err != nil {
		t.Fatal(err)
	}

	// downloadEntries makes the fake `rclone copy` into staging populate a file so
	// the download verify step has something to check, mirroring the run harness.
	runner := &fakeRunner{}
	operator := newSystemOperator(t, runner, stateDB, masterKey)

	manifest, err := operator.RunSystem(context.Background(), SystemRunRequest{
		Account:  Account{Type: TypeLocal, Path: "/srv/backups"},
		CopyName: "2026-07-21_000000000000000Z_abcd1234",
		Limit:    5,
	})
	if err != nil {
		t.Fatalf("RunSystem: %v", err)
	}
	if !manifest.IntegrityChecked {
		t.Fatal("manifest.IntegrityChecked = false, want true")
	}
	if len(manifest.Entries) != 1 || manifest.Entries[0].Name != systemArchiveName {
		t.Fatalf("entries = %+v, want a single %s", manifest.Entries, systemArchiveName)
	}
	if manifest.Entries[0].SHA256 == "" || manifest.SizeBytes == 0 {
		t.Fatalf("manifest missing size/checksum: %+v", manifest)
	}
	// copy must precede the download check, which must precede prune (lsf).
	copyIndex := findCall(runner.calls, "rclone", "copy")
	checkIndex := findCall(runner.calls, "rclone", "check")
	pruneIndex := findCall(runner.calls, "rclone", "lsf")
	if copyIndex < 0 || checkIndex < 0 {
		t.Fatalf("expected copy then check, calls: %v", runner.calls)
	}
	if !(copyIndex < checkIndex) {
		t.Fatalf("copy must precede check: %v", runner.calls)
	}
	if pruneIndex >= 0 && pruneIndex < checkIndex {
		t.Fatalf("prune must follow verify: %v", runner.calls)
	}
	// The remote layout is <base>/system/<copyName>/.
	if got := runner.calls[copyIndex]; got[len(got)-1] != "nexa:/srv/backups/system/2026-07-21_000000000000000Z_abcd1234" {
		t.Fatalf("copy destination = %q", got[len(got)-1])
	}
}

func TestRunSystemPurgesOnFailedVerify(t *testing.T) {
	dir := t.TempDir()
	stateDB := filepath.Join(dir, "control.db")
	seedControlDB(t, stateDB)
	masterKey := filepath.Join(dir, "master.key")
	if err := os.WriteFile(masterKey, []byte("key"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{commandErrors: map[string]error{"check": errors.New("remote data differs")}}
	operator := newSystemOperator(t, runner, stateDB, masterKey)

	if _, err := operator.RunSystem(context.Background(), SystemRunRequest{
		Account:  Account{Type: TypeLocal, Path: "/srv/backups"},
		CopyName: "copy_1", Limit: 5,
	}); err == nil {
		t.Fatal("expected an error when the verify step fails")
	}
	if findCall(runner.calls, "rclone", "purge") < 0 {
		t.Fatalf("expected a best-effort purge after a failed verify: %v", runner.calls)
	}
	if findCall(runner.calls, "rclone", "lsf") >= 0 {
		t.Fatalf("prune must not run after a failed verify: %v", runner.calls)
	}
}

func TestValidateSystemRunRequest(t *testing.T) {
	if err := ValidateSystemRunRequest(SystemRunRequest{CopyName: "copy_1", StagingRoot: "/"}); err == nil {
		t.Fatal("expected a supplied staging root to be rejected")
	}
	if err := ValidateSystemRunRequest(SystemRunRequest{CopyName: "../escape"}); err == nil {
		t.Fatal("expected a bad copy name to be rejected")
	}
	if err := ValidateSystemRunRequest(SystemRunRequest{CopyName: "copy_1", Limit: -1}); err == nil {
		t.Fatal("expected a negative limit to be rejected")
	}
	if err := ValidateSystemRunRequest(SystemRunRequest{CopyName: "copy_1", Limit: 5}); err != nil {
		t.Fatalf("valid request rejected: %v", err)
	}
}

func TestExtractSystemArchiveRoundTrip(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	if err := os.Mkdir(src, 0o700); err != nil {
		t.Fatal(err)
	}
	controlSrc := filepath.Join(src, systemControlDBEntry)
	keySrc := filepath.Join(src, systemMasterKeyEntry)
	if err := os.WriteFile(controlSrc, []byte("db-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keySrc, []byte("key-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(dir, systemArchiveName)
	if err := writeSystemArchive(archive, []systemMember{
		{name: systemControlDBEntry, source: controlSrc},
		{name: systemMasterKeyEntry, source: keySrc},
	}); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(archive); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("archive perms = %v (err %v), want 600", func() any {
			if err == nil {
				return info.Mode().Perm()
			}
			return "missing"
		}(), err)
	}
	names := tarMembers(t, archive)
	slices.Sort(names)
	if !slices.Equal(names, []string{systemControlDBEntry, systemMasterKeyEntry}) {
		t.Fatalf("archive members = %v, want exactly control.db + master.key", names)
	}

	stateDest := filepath.Join(dir, "restored-control.db")
	keyDest := filepath.Join(dir, "restored-master.key")
	if err := ExtractSystemArchive(archive, stateDest, keyDest); err != nil {
		t.Fatalf("extract: %v", err)
	}
	for _, dest := range []string{stateDest, keyDest} {
		info, err := os.Stat(dest)
		if err != nil {
			t.Fatalf("restored file missing: %v", err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("%s perms = %o, want 600", dest, info.Mode().Perm())
		}
	}
	if got, _ := os.ReadFile(stateDest); string(got) != "db-bytes" {
		t.Fatalf("restored control.db = %q", got)
	}
}

func TestExtractSystemArchiveRejectsUnexpectedMembers(t *testing.T) {
	dir := t.TempDir()

	// An archive with a traversing/extra member must be refused wholesale.
	hostile := filepath.Join(dir, "hostile.tar.gz")
	writeRawArchive(t, hostile, map[string]string{
		systemControlDBEntry: "db", systemMasterKeyEntry: "key", "../etc/passwd": "root",
	})
	if err := ExtractSystemArchive(hostile, filepath.Join(dir, "s"), filepath.Join(dir, "k")); err == nil {
		t.Fatal("expected an archive with an extra/traversing member to be rejected")
	}

	// An archive missing a required member must also be refused.
	partial := filepath.Join(dir, "partial.tar.gz")
	writeRawArchive(t, partial, map[string]string{systemControlDBEntry: "db"})
	if err := ExtractSystemArchive(partial, filepath.Join(dir, "s2"), filepath.Join(dir, "k2")); err == nil {
		t.Fatal("expected an archive missing master.key to be rejected")
	}
}

// --- test helpers ---

func findCall(calls [][]string, name, sub string) int {
	for index, call := range calls {
		if len(call) >= 2 && call[0] == name && call[1] == sub {
			return index
		}
	}
	return -1
}

func tarMembers(t *testing.T, archivePath string) []string {
	t.Helper()
	file, err := os.Open(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	defer gzipReader.Close()
	reader := tar.NewReader(gzipReader)
	names := []string{}
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, header.Name)
	}
	return names
}

func writeRawArchive(t *testing.T, archivePath string, members map[string]string) {
	t.Helper()
	var buffer bytes.Buffer
	gzipWriter := gzip.NewWriter(&buffer)
	tarWriter := tar.NewWriter(gzipWriter)
	for name, content := range members {
		if err := tarWriter.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tarWriter.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(archivePath, buffer.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}
