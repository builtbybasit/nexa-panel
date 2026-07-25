package backups

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// defaultStagingRoot is where a copy is assembled locally before it is shipped
// to the (possibly remote) account. It lives under the agent's state tree.
const defaultStagingRoot = "/var/lib/nexa-panel/backups/staging"

// SiteTarget is a resolved site to back up: its document root is archived whole.
type SiteTarget struct {
	Slug     string `json:"slug"`
	RootPath string `json:"rootPath"`
	UnixUser string `json:"unixUser"`
}

// DatabaseTarget is a resolved database to dump. Engine selects the dump tool;
// Version/Port/Socket carry the connection details the control panel read from
// the owning engine instance.
type DatabaseTarget struct {
	Engine  string `json:"engine"` // "postgres" | "mysql" | "mariadb"
	Name    string `json:"name"`
	Version string `json:"version"` // postgres binary series (e.g. "16"); unused for MySQL
	Port    int    `json:"port"`
	Socket  string `json:"socket"` // postgres socket dir, or MySQL socket file
}

// RunRequest is one backup execution. Copies for a plan are grouped under a
// per-plan directory (PlanID) so retention only ever prunes that plan's copies,
// even when several plans share one account path.
type RunRequest struct {
	Account     Account          `json:"account"`
	PlanID      string           `json:"planId"`
	CopyName    string           `json:"copyName"` // timestamp dir, control-panel generated so it sorts chronologically
	Limit       int              `json:"limit"`
	Sites       []SiteTarget     `json:"sites"`
	Databases   []DatabaseTarget `json:"databases"`
	StagingRoot string           `json:"stagingRoot"`
}

type CopyEntry struct {
	Name      string `json:"name"`
	SizeBytes int64  `json:"sizeBytes"`
	SHA256    string `json:"sha256"`
}

// Manifest is the result of a run: what was written and where.
type Manifest struct {
	CopyName         string      `json:"copyName"`
	RemotePath       string      `json:"remotePath"`
	SizeBytes        int64       `json:"sizeBytes"`
	Entries          []CopyEntry `json:"entries"`
	IntegrityChecked bool        `json:"integrityChecked"`
	Pruned           []string    `json:"pruned"`
}

// Run assembles one backup copy locally, ships it to the account with rclone,
// then prunes the plan's oldest copies beyond the retention limit.
func (h *HostOperator) Run(ctx context.Context, request RunRequest) (Manifest, error) {
	siteRoot := h.siteRoot
	if siteRoot == "" {
		siteRoot = defaultSiteRoot
	}
	if err := validateRunRequest(request, siteRoot); err != nil {
		return Manifest{}, err
	}
	base, env, err := rcloneRemote(request.Account)
	if err != nil {
		return Manifest{}, err
	}
	stagingRoot := h.stagingRoot
	if stagingRoot == "" {
		stagingRoot = defaultStagingRoot
	}
	staging := filepath.Join(stagingRoot, request.PlanID, request.CopyName)
	if err := os.MkdirAll(filepath.Dir(staging), 0o700); err != nil {
		return Manifest{}, fmt.Errorf("prepare staging directory: %w", err)
	}
	if err := os.Mkdir(staging, 0o700); err != nil {
		return Manifest{}, fmt.Errorf("prepare staging directory: %w", err)
	}
	defer os.RemoveAll(staging)

	entries, err := h.assemble(ctx, staging, request)
	if err != nil {
		return Manifest{}, err
	}
	if len(entries) == 0 {
		return Manifest{}, fmt.Errorf("the plan selected nothing to back up")
	}

	// remote layout: <account path>/<planId>/<copyName>/
	planDir := joinRemote(base, request.PlanID)
	copyDir := joinRemote(planDir, request.CopyName)
	if _, err := h.runner.Run(ctx, h.binary, []string{"copy", staging, copyDir}, env); err != nil {
		return Manifest{}, fmt.Errorf("upload backup copy: %w", err)
	}
	// Copy verifies transfer hashes when the backend exposes them, but a
	// download check also covers remotes without compatible hash support. Do
	// this before retention so an unverified upload can never evict an older
	// copy. A failed copy is purged best-effort and is not recorded as usable.
	if _, err := h.runner.Run(ctx, h.binary, []string{"check", staging, copyDir, "--download"}, env); err != nil {
		cleanupContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		_, _ = h.runner.Run(cleanupContext, h.binary, []string{"purge", copyDir}, env)
		cancel()
		return Manifest{}, fmt.Errorf("verify uploaded backup copy: %w", err)
	}

	pruned := h.prune(ctx, planDir, request.CopyName, request.Limit, env)

	var total int64
	for _, entry := range entries {
		total += entry.SizeBytes
	}
	return Manifest{
		CopyName: request.CopyName, RemotePath: strings.TrimPrefix(copyDir, remoteName+":"),
		SizeBytes: total, Entries: entries, IntegrityChecked: true, Pruned: pruned,
	}, nil
}

// assemble writes each target's artifact into staging and returns their sizes.
func (h *HostOperator) assemble(ctx context.Context, staging string, request RunRequest) ([]CopyEntry, error) {
	entries := []CopyEntry{}
	for _, site := range request.Sites {
		name := "site-" + site.Slug + ".tar.gz"
		out := filepath.Join(staging, name)
		// Archive the document root whole. The agent is root, so it can read a
		// site user's files; -C makes paths inside the archive relative.
		if _, err := h.runner.Run(ctx, "tar", []string{"-czf", out, "-C", site.RootPath, "."}, nil); err != nil {
			return nil, fmt.Errorf("archive site %s: %w", site.Slug, err)
		}
		size, checksum, err := artifactMetadata(out)
		if err != nil {
			return nil, fmt.Errorf("inspect site archive %s: %w", site.Slug, err)
		}
		entries = append(entries, CopyEntry{Name: name, SizeBytes: size, SHA256: checksum})
	}
	for _, database := range request.Databases {
		name, err := h.dumpDatabase(ctx, staging, database)
		if err != nil {
			return nil, err
		}
		size, checksum, err := artifactMetadata(filepath.Join(staging, name))
		if err != nil {
			return nil, fmt.Errorf("inspect database dump %s: %w", database.Name, err)
		}
		entries = append(entries, CopyEntry{Name: name, SizeBytes: size, SHA256: checksum})
	}
	return entries, nil
}

// dumpDatabase writes one database dump into staging and returns its file name.
// The command construction mirrors the existing postgres/mysql operators so the
// dumps are byte-compatible with those engines' restore paths.
func (h *HostOperator) dumpDatabase(ctx context.Context, staging string, database DatabaseTarget) (string, error) {
	switch database.Engine {
	case "postgres":
		name := "db-postgres-" + database.Name + ".dump"
		out := filepath.Join(staging, name)
		program := filepath.Join("/usr/lib/postgresql", database.Version, "bin", "pg_dump")
		args := []string{
			"-u", "postgres", "--", program, "--host", database.Socket, "--port", strconv.Itoa(database.Port),
			"--username", "postgres", "--format", "custom", "--no-owner", "--no-privileges", "--file", out, database.Name,
		}
		if _, err := h.runner.Run(ctx, "runuser", args, nil); err != nil {
			return "", fmt.Errorf("dump PostgreSQL database %s: %w", database.Name, err)
		}
		return name, nil
	case "mysql", "mariadb":
		tool := "mysqldump"
		if database.Engine == "mariadb" {
			tool = "mariadb-dump"
		}
		name := "db-" + database.Engine + "-" + database.Name + ".sql"
		out := filepath.Join(staging, name)
		args := []string{
			"--protocol=socket", "--socket=" + database.Socket, "--user=root", "--single-transaction",
			"--routines", "--events", "--triggers", "--hex-blob", "--default-character-set=utf8mb4",
		}
		if database.Engine == "mysql" {
			args = append(args, "--set-gtid-purged=OFF")
		}
		args = append(args, "--databases", database.Name)
		if err := h.runner.RunToFile(ctx, tool, args, []string{"MYSQL_HISTFILE=/dev/null"}, out); err != nil {
			return "", fmt.Errorf("dump %s database %s: %w", database.Engine, database.Name, err)
		}
		return name, nil
	default:
		return "", fmt.Errorf("unsupported database engine %q", database.Engine)
	}
}

// prune deletes the plan's copy directories older than the retention limit,
// keeping the newest Limit (which includes the copy just written). Failures are
// swallowed: a copy that succeeded must not be reported as failed because
// cleanup of an old one hiccuped.
func (h *HostOperator) prune(ctx context.Context, planDir, keepName string, limit int, env []string) []string {
	if limit < 1 {
		return nil
	}
	output, err := h.runner.Run(ctx, h.binary, []string{"lsf", "--dirs-only", planDir}, env)
	if err != nil {
		return nil
	}
	names := []string{}
	for _, line := range strings.Split(output, "\n") {
		name := strings.TrimRight(strings.TrimSpace(line), "/")
		if name != "" {
			names = append(names, name)
		}
	}
	// Newest first; copy names are timestamps so lexical order is chronological.
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	pruned := []string{}
	for index, name := range names {
		if index < limit || name == keepName {
			continue
		}
		if _, err := h.runner.Run(ctx, h.binary, []string{"purge", joinRemote(planDir, name)}, env); err == nil {
			pruned = append(pruned, name)
		}
	}
	return pruned
}

func joinRemote(base, segment string) string {
	return base + "/" + strings.TrimPrefix(path.Clean("/"+segment), "/")
}

func artifactMetadata(path string) (int64, string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return 0, "", err
	}
	if !info.Mode().IsRegular() {
		return 0, "", fmt.Errorf("artifact is not a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return 0, "", err
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return 0, "", err
	}
	return info.Size(), hex.EncodeToString(digest.Sum(nil)), nil
}
