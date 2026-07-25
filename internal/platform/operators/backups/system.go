package backups

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" driver for the VACUUM snapshot
)

// The panel's own state lives beside the agent's other managed data. Both files
// are credential-equivalent: control.db holds every user, grant, and encrypted
// secret, and master.key is the AES key that decrypts them. They are agent-owned
// paths and are NEVER accepted from a request, closing any arbitrary-file
// exfiltration path (mirrors the "staging root is agent-owned" rule).
const (
	defaultStateDBPath   = "/var/lib/nexa-panel/control.db"
	defaultMasterKeyPath = "/var/lib/nexa-panel/master.key"
)

// System backup archive layout. The archive contains exactly these two members;
// the extractor rejects anything else so a tampered archive cannot drop files
// elsewhere on a fresh node during restore.
const (
	systemArchiveName    = "nexa-panel-system.tar.gz"
	systemControlDBEntry = "control.db"
	systemMasterKeyEntry = "master.key"
	// maxSystemMemberBytes bounds each member on extraction so a hostile archive
	// cannot exhaust the disk of a node being restored.
	maxSystemMemberBytes = 4 << 30 // 4 GiB
)

// SystemRunRequest is one panel-state ("system") backup execution. Unlike a plan
// run it selects no sites or databases: the source is always the agent-owned
// control.db + master.key. StagingRoot is rejected exactly like RunRequest.
type SystemRunRequest struct {
	Account     Account `json:"account"`
	CopyName    string  `json:"copyName"` // control-panel generated timestamp so copies sort chronologically
	Limit       int     `json:"limit"`
	StagingRoot string  `json:"stagingRoot"`
}

// RunSystem snapshots control.db + master.key into a single archive and ships it
// to the account off-node, mirroring Run's assemble→upload→verify→prune ordering
// so an unverified upload can never evict an older copy. control.db is captured
// with SQLite's VACUUM INTO (a hot-backup that only needs a read transaction on
// the live WAL database) — never a raw copy of the live file.
func (h *HostOperator) RunSystem(ctx context.Context, request SystemRunRequest) (Manifest, error) {
	if err := ValidateSystemRunRequest(request); err != nil {
		return Manifest{}, err
	}
	base, env, err := rcloneRemote(request.Account)
	if err != nil {
		return Manifest{}, err
	}
	stateDBPath := h.stateDBPath
	if stateDBPath == "" {
		stateDBPath = defaultStateDBPath
	}
	masterKeyPath := h.masterKeyPath
	if masterKeyPath == "" {
		masterKeyPath = defaultMasterKeyPath
	}
	stagingRoot := h.stagingRoot
	if stagingRoot == "" {
		stagingRoot = defaultStagingRoot
	}

	staging := filepath.Join(stagingRoot, "system", request.CopyName)
	if err := os.MkdirAll(filepath.Dir(staging), 0o700); err != nil {
		return Manifest{}, fmt.Errorf("prepare staging directory: %w", err)
	}
	if err := os.Mkdir(staging, 0o700); err != nil {
		return Manifest{}, fmt.Errorf("prepare staging directory: %w", err)
	}
	// Guarantee 0700 regardless of umask: the archive is credential-equivalent.
	if err := os.Chmod(staging, 0o700); err != nil {
		return Manifest{}, fmt.Errorf("secure staging directory: %w", err)
	}
	defer os.RemoveAll(staging)

	// Assemble the two source files under a private subdirectory, then pack them
	// into the single archive that is the only file uploaded from staging.
	payload := filepath.Join(staging, "payload")
	if err := os.Mkdir(payload, 0o700); err != nil {
		return Manifest{}, fmt.Errorf("prepare snapshot directory: %w", err)
	}
	snapshotPath := filepath.Join(payload, systemControlDBEntry)
	if err := snapshotControlDB(ctx, stateDBPath, snapshotPath); err != nil {
		return Manifest{}, err
	}
	keyCopyPath := filepath.Join(payload, systemMasterKeyEntry)
	if err := copyFileStrict(masterKeyPath, keyCopyPath); err != nil {
		return Manifest{}, fmt.Errorf("stage master key: %w", err)
	}

	archivePath := filepath.Join(staging, systemArchiveName)
	if err := writeSystemArchive(archivePath, []systemMember{
		{name: systemControlDBEntry, source: snapshotPath},
		{name: systemMasterKeyEntry, source: keyCopyPath},
	}); err != nil {
		return Manifest{}, err
	}
	// Drop the plaintext staging copies as soon as they are packed so only the
	// single archive remains to be uploaded.
	if err := os.RemoveAll(payload); err != nil {
		return Manifest{}, fmt.Errorf("clean snapshot directory: %w", err)
	}

	size, checksum, err := artifactMetadata(archivePath)
	if err != nil {
		return Manifest{}, fmt.Errorf("inspect system archive: %w", err)
	}

	// remote layout: <account path>/system/<copyName>/
	planDir := joinRemote(base, "system")
	copyDir := joinRemote(planDir, request.CopyName)
	if _, err := h.runner.Run(ctx, h.binary, []string{"copy", staging, copyDir}, env); err != nil {
		return Manifest{}, fmt.Errorf("upload system backup: %w", err)
	}
	if _, err := h.runner.Run(ctx, h.binary, []string{"check", staging, copyDir, "--download"}, env); err != nil {
		cleanupContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		_, _ = h.runner.Run(cleanupContext, h.binary, []string{"purge", copyDir}, env)
		cancel()
		return Manifest{}, fmt.Errorf("verify uploaded system backup: %w", err)
	}

	pruned := h.prune(ctx, planDir, request.CopyName, request.Limit, env)

	return Manifest{
		CopyName:         request.CopyName,
		RemotePath:       strings.TrimPrefix(copyDir, remoteName+":"),
		SizeBytes:        size,
		Entries:          []CopyEntry{{Name: systemArchiveName, SizeBytes: size, SHA256: checksum}},
		IntegrityChecked: true,
		Pruned:           pruned,
	}, nil
}

// ValidateSystemRunRequest applies the trust boundary to the untrusted
// control-panel payload before the privileged agent creates files or uploads.
func ValidateSystemRunRequest(request SystemRunRequest) error {
	if request.StagingRoot != "" {
		return errors.New("backup staging root is agent-owned and must not be supplied")
	}
	if err := validateBackupToken("copy name", request.CopyName); err != nil {
		return err
	}
	if request.Limit < 0 || request.Limit > maxRetentionCopies {
		return fmt.Errorf("backup retention limit must be between 0 and %d", maxRetentionCopies)
	}
	return nil
}

// snapshotControlDB writes a consistent copy of the live SQLite control database
// to destPath using VACUUM INTO. The source is opened read-only so the snapshot
// can never mutate the live database, and VACUUM INTO needs only a read
// transaction, so it is safe while the API holds the WAL database open. destPath
// must not already exist (SQLite refuses to overwrite), which the fresh staging
// directory guarantees.
func snapshotControlDB(ctx context.Context, sourcePath, destPath string) error {
	if _, err := os.Stat(sourcePath); err != nil {
		return fmt.Errorf("open control database: %w", err)
	}
	dsn := (&url.URL{Scheme: "file", Path: sourcePath}).String() +
		"?_pragma=busy_timeout(5000)&mode=ro"
	database, err := sql.Open("sqlite", dsn)
	if err != nil {
		return fmt.Errorf("open control database: %w", err)
	}
	defer database.Close()
	database.SetMaxOpenConns(1)

	// destPath is agent-owned (staging + a token-validated copy name), so no
	// untrusted input reaches this statement; the single-quote doubling is
	// belt-and-braces against a quote in a configured staging root.
	quoted := "'" + strings.ReplaceAll(destPath, "'", "''") + "'"
	if _, err := database.ExecContext(ctx, "VACUUM INTO "+quoted); err != nil {
		return fmt.Errorf("snapshot control database: %w", err)
	}
	if err := os.Chmod(destPath, 0o600); err != nil {
		return fmt.Errorf("secure control database snapshot: %w", err)
	}
	return nil
}

// copyFileStrict copies a regular file to dest, creating it 0600 and forcing the
// mode after write so the credential-bearing master key is never group/world
// readable regardless of umask.
func copyFileStrict(source, dest string) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("source is not a regular file")
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Chmod(0o600); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

type systemMember struct {
	name   string
	source string
}

// writeSystemArchive packs the given members into a gzip-compressed tar at
// archivePath, created 0600. Member names are fixed, single-segment file names
// (no directories), so the archive is trivially traversal-free.
func writeSystemArchive(archivePath string, members []systemMember) error {
	out, err := os.OpenFile(archivePath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("create system archive: %w", err)
	}
	if err := out.Chmod(0o600); err != nil {
		out.Close()
		return fmt.Errorf("secure system archive: %w", err)
	}
	gzipWriter := gzip.NewWriter(out)
	tarWriter := tar.NewWriter(gzipWriter)

	for _, member := range members {
		if err := writeArchiveMember(tarWriter, member); err != nil {
			tarWriter.Close()
			gzipWriter.Close()
			out.Close()
			return err
		}
	}
	if err := tarWriter.Close(); err != nil {
		gzipWriter.Close()
		out.Close()
		return fmt.Errorf("finalize system archive: %w", err)
	}
	if err := gzipWriter.Close(); err != nil {
		out.Close()
		return fmt.Errorf("finalize system archive: %w", err)
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return fmt.Errorf("flush system archive: %w", err)
	}
	return out.Close()
}

func writeArchiveMember(tarWriter *tar.Writer, member systemMember) error {
	info, err := os.Lstat(member.source)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("archive member %q is not a regular file", member.name)
	}
	header := &tar.Header{
		Name:     member.name,
		Mode:     0o600,
		Size:     info.Size(),
		Typeflag: tar.TypeReg,
		ModTime:  info.ModTime(),
	}
	if err := tarWriter.WriteHeader(header); err != nil {
		return fmt.Errorf("write archive header %q: %w", member.name, err)
	}
	file, err := os.Open(member.source)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := io.Copy(tarWriter, file); err != nil {
		return fmt.Errorf("write archive member %q: %w", member.name, err)
	}
	return nil
}

// ExtractSystemArchive restores a LOCAL system archive onto a node: it extracts
// exactly control.db -> stateDest and master.key -> keyDest, both written 0600.
// It rejects any archive that contains an unexpected member, a traversing name,
// a non-regular entry, or that is missing either required file — so a tampered
// archive can never drop files elsewhere on a fresh node. Fetching the archive
// from the remote is a documented `rclone copy` step (see the restore runbook);
// this stays self-contained because restore cannot read the (encrypted) storage
// account — control.db is exactly what is being restored.
func ExtractSystemArchive(archivePath, stateDest, keyDest string) error {
	archive, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("open system archive: %w", err)
	}
	defer archive.Close()
	compressed, err := gzip.NewReader(archive)
	if err != nil {
		return fmt.Errorf("open compressed system archive: %w", err)
	}
	defer compressed.Close()

	destinations := map[string]string{
		systemControlDBEntry: stateDest,
		systemMasterKeyEntry: keyDest,
	}
	seen := make(map[string]struct{}, len(destinations))

	reader := tar.NewReader(compressed)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read system archive: %w", err)
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != 0 {
			return fmt.Errorf("system archive member %q is not a regular file", header.Name)
		}
		if strings.ContainsAny(header.Name, "/\\") || header.Name == ".." || header.Name == "." || header.Name == "" {
			return fmt.Errorf("system archive contains an unsafe member name %q", header.Name)
		}
		dest, ok := destinations[header.Name]
		if !ok {
			return fmt.Errorf("system archive contains an unexpected member %q", header.Name)
		}
		if _, duplicate := seen[header.Name]; duplicate {
			return fmt.Errorf("system archive contains a duplicate member %q", header.Name)
		}
		if header.Size < 0 || header.Size > maxSystemMemberBytes {
			return fmt.Errorf("system archive member %q exceeds the size limit", header.Name)
		}
		if err := writeExtractedMember(dest, reader, header.Size); err != nil {
			return fmt.Errorf("extract %q: %w", header.Name, err)
		}
		seen[header.Name] = struct{}{}
	}
	for name := range destinations {
		if _, ok := seen[name]; !ok {
			return fmt.Errorf("system archive is missing required member %q", name)
		}
	}
	return nil
}

func writeExtractedMember(dest string, reader io.Reader, size int64) error {
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if err := out.Chmod(0o600); err != nil {
		out.Close()
		return err
	}
	if _, err := io.CopyN(out, reader, size); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
