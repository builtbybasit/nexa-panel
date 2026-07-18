package backups

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// SiteRestoreTarget restores one site archive from a copy into a destination
// document root. Clear wipes the destination's current contents first.
type SiteRestoreTarget struct {
	Entry  string     `json:"entry"`
	Target SiteTarget `json:"target"`
	Clear  bool       `json:"clear"`
}

// DatabaseRestoreTarget restores one database dump from a copy into a
// destination database. Clear drops the destination's objects first (PostgreSQL
// only; a mysqldump already recreates its tables).
type DatabaseRestoreTarget struct {
	Entry  string         `json:"entry"`
	Target DatabaseTarget `json:"target"`
	Clear  bool           `json:"clear"`
}

type RestoreRequest struct {
	Account     Account                 `json:"account"`
	PlanID      string                  `json:"planId"`
	CopyName    string                  `json:"copyName"`
	Sites       []SiteRestoreTarget     `json:"sites"`
	Databases   []DatabaseRestoreTarget `json:"databases"`
	StagingRoot string                  `json:"stagingRoot"`
}

// DeleteRequest removes one stored copy directory from an account.
type DeleteRequest struct {
	Account  Account `json:"account"`
	PlanID   string  `json:"planId"`
	CopyName string  `json:"copyName"`
}

// Restore downloads a copy from its account and replays the selected site and
// database artifacts into their chosen destinations. The command construction
// mirrors the PostgreSQL/MySQL operators' restore paths.
func (h *HostOperator) Restore(ctx context.Context, request RestoreRequest) error {
	base, env, err := rcloneRemote(request.Account)
	if err != nil {
		return err
	}
	if request.CopyName == "" || strings.ContainsAny(request.CopyName, "/\\") {
		return fmt.Errorf("invalid copy name %q", request.CopyName)
	}
	stagingRoot := request.StagingRoot
	if stagingRoot == "" {
		stagingRoot = defaultStagingRoot
	}
	staging := filepath.Join(stagingRoot, "restore", request.PlanID, request.CopyName)
	if err := os.MkdirAll(staging, 0o700); err != nil {
		return fmt.Errorf("prepare restore directory: %w", err)
	}
	defer os.RemoveAll(filepath.Join(stagingRoot, "restore", request.PlanID))

	copyDir := joinRemote(joinRemote(base, request.PlanID), request.CopyName)
	if _, err := h.runner.Run(ctx, h.binary, []string{"copy", copyDir, staging}, env); err != nil {
		return fmt.Errorf("download backup copy: %w", err)
	}

	for _, site := range request.Sites {
		if err := h.restoreSite(ctx, staging, site); err != nil {
			return err
		}
	}
	for _, database := range request.Databases {
		if err := h.restoreDatabase(ctx, staging, database); err != nil {
			return err
		}
	}
	return nil
}

func (h *HostOperator) restoreSite(ctx context.Context, staging string, site SiteRestoreTarget) error {
	archive, err := safeEntry(staging, site.Entry)
	if err != nil {
		return err
	}
	if site.Target.RootPath == "" {
		return fmt.Errorf("restore destination for %s is missing a document root", site.Entry)
	}
	if site.Clear {
		// Empty the destination without removing the root itself (which is a
		// managed mount point with its own ownership).
		if _, err := h.runner.Run(ctx, "find", []string{site.Target.RootPath, "-mindepth", "1", "-delete"}, nil); err != nil {
			return fmt.Errorf("clear %s: %w", site.Target.RootPath, err)
		}
	}
	if _, err := h.runner.Run(ctx, "tar", []string{"-xzf", archive, "-C", site.Target.RootPath}, nil); err != nil {
		return fmt.Errorf("extract %s: %w", site.Entry, err)
	}
	if site.Target.UnixUser != "" {
		if _, err := h.runner.Run(ctx, "chown", []string{"-R", site.Target.UnixUser + ":" + site.Target.UnixUser, site.Target.RootPath}, nil); err != nil {
			return fmt.Errorf("restore ownership of %s: %w", site.Target.RootPath, err)
		}
	}
	return nil
}

func (h *HostOperator) restoreDatabase(ctx context.Context, staging string, database DatabaseRestoreTarget) error {
	dump, err := safeEntry(staging, database.Entry)
	if err != nil {
		return err
	}
	switch database.Target.Engine {
	case "postgres":
		program := filepath.Join("/usr/lib/postgresql", database.Target.Version, "bin", "pg_restore")
		args := []string{"-u", "postgres", "--", program, "--host", database.Target.Socket, "--port", strconv.Itoa(database.Target.Port),
			"--username", "postgres", "--dbname", database.Target.Name, "--no-owner", "--no-privileges"}
		if database.Clear {
			args = append(args, "--clean", "--if-exists")
		}
		args = append(args, dump)
		if _, err := h.runner.Run(ctx, "runuser", args, nil); err != nil {
			return fmt.Errorf("restore PostgreSQL database %s: %w", database.Target.Name, err)
		}
		return nil
	case "mysql", "mariadb":
		// The mysqldump was taken with --databases, so it carries its own
		// CREATE DATABASE / USE and (by default) DROP TABLE statements; the
		// client just replays the script from stdin.
		client := "mysql"
		if database.Target.Engine == "mariadb" {
			client = "mariadb"
		}
		args := []string{"--protocol=socket", "--socket=" + database.Target.Socket, "--user=root"}
		if err := h.runner.RunFromFile(ctx, client, args, []string{"MYSQL_HISTFILE=/dev/null"}, dump); err != nil {
			return fmt.Errorf("restore %s database %s: %w", database.Target.Engine, database.Target.Name, err)
		}
		return nil
	default:
		return fmt.Errorf("unsupported database engine %q", database.Target.Engine)
	}
}

// DeleteCopy purges one copy directory from its account.
func (h *HostOperator) DeleteCopy(ctx context.Context, request DeleteRequest) error {
	base, env, err := rcloneRemote(request.Account)
	if err != nil {
		return err
	}
	if request.CopyName == "" || strings.ContainsAny(request.CopyName, "/\\") {
		return fmt.Errorf("invalid copy name %q", request.CopyName)
	}
	copyDir := joinRemote(joinRemote(base, request.PlanID), request.CopyName)
	if _, err := h.runner.Run(ctx, h.binary, []string{"purge", copyDir}, env); err != nil {
		return fmt.Errorf("delete backup copy: %w", err)
	}
	return nil
}

// safeEntry resolves a copy entry name to a path inside staging, rejecting any
// name that tries to escape the directory (defence in depth; the control plane
// already checks the entry belongs to the copy).
func safeEntry(staging, entry string) (string, error) {
	if entry == "" || strings.ContainsAny(entry, "/\\") || entry == ".." {
		return "", fmt.Errorf("invalid copy entry %q", entry)
	}
	return filepath.Join(staging, entry), nil
}
