package backups

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const restoreRecoveryTimeout = 30 * time.Minute

type restoreActivation struct {
	description string
	commit      func(context.Context) error
	rollback    func(context.Context) error
}

// SiteRestoreTarget restores one site archive from a copy into a destination
// document root. Clear wipes the destination's current contents first.
type SiteRestoreTarget struct {
	Entry  string     `json:"entry"`
	SHA256 string     `json:"sha256"`
	Target SiteTarget `json:"target"`
	Clear  bool       `json:"clear"`
}

// DatabaseRestoreTarget restores one database dump from a copy into a
// destination database. Every engine creates a rollback point before mutation;
// PostgreSQL uses an isolated database-name swap, while Clear requests an exact
// MySQL-family reset before replaying the dump.
type DatabaseRestoreTarget struct {
	Entry  string         `json:"entry"`
	SHA256 string         `json:"sha256"`
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
	siteRoot := h.siteRoot
	if siteRoot == "" {
		siteRoot = defaultSiteRoot
	}
	if err := validateRestoreRequest(request, siteRoot); err != nil {
		return err
	}
	base, env, err := rcloneRemote(request.Account)
	if err != nil {
		return err
	}
	stagingRoot := h.stagingRoot
	if stagingRoot == "" {
		stagingRoot = defaultStagingRoot
	}
	staging := filepath.Join(stagingRoot, "restore", request.PlanID, request.CopyName)
	if err := os.MkdirAll(filepath.Dir(staging), 0o700); err != nil {
		return fmt.Errorf("prepare restore directory: %w", err)
	}
	if err := os.Mkdir(staging, 0o700); err != nil {
		return fmt.Errorf("prepare restore directory: %w", err)
	}
	defer os.RemoveAll(staging)

	copyDir := joinRemote(joinRemote(base, request.PlanID), request.CopyName)
	if _, err := h.runner.Run(ctx, h.binary, []string{"copy", copyDir, staging}, env); err != nil {
		return fmt.Errorf("download backup copy: %w", err)
	}
	// Verify every requested artifact before touching a live destination. New
	// copies carry a trusted SHA-256 recorded immediately after creation;
	// checksum-less legacy copies are still confined to regular files and can
	// only reach this boundary after explicit control-plane confirmation.
	for _, site := range request.Sites {
		if err := verifyStagedEntry(staging, site.Entry, site.SHA256); err != nil {
			return fmt.Errorf("verify site artifact %s: %w", site.Entry, err)
		}
	}
	for _, database := range request.Databases {
		if err := verifyStagedEntry(staging, database.Entry, database.SHA256); err != nil {
			return fmt.Errorf("verify database artifact %s: %w", database.Entry, err)
		}
	}

	activations := make([]restoreActivation, 0, len(request.Sites)+len(request.Databases))
	for _, site := range request.Sites {
		activation, err := h.restoreSite(ctx, staging, site)
		if err != nil {
			return rollbackRestoreActivations(ctx, activations, err)
		}
		activations = append(activations, activation)
	}
	for _, database := range request.Databases {
		activation, err := h.restoreDatabase(ctx, staging, database)
		if err != nil {
			return rollbackRestoreActivations(ctx, activations, err)
		}
		activations = append(activations, activation)
	}
	return commitRestoreActivations(ctx, activations)
}

func (h *HostOperator) restoreSite(ctx context.Context, staging string, site SiteRestoreTarget) (restoreActivation, error) {
	archive, err := safeEntry(staging, site.Entry)
	if err != nil {
		return restoreActivation{}, err
	}
	if site.Target.RootPath == "" {
		return restoreActivation{}, fmt.Errorf("restore destination for %s is missing a document root", site.Entry)
	}
	targetInfo, err := os.Lstat(site.Target.RootPath)
	if err != nil {
		return restoreActivation{}, fmt.Errorf("inspect site restore destination: %w", err)
	}
	if !targetInfo.IsDir() || targetInfo.Mode()&os.ModeSymlink != 0 {
		return restoreActivation{}, fmt.Errorf("site restore destination %s is not a real directory", site.Target.RootPath)
	}
	parent := filepath.Dir(site.Target.RootPath)
	token, err := restoreRandomToken()
	if err != nil {
		return restoreActivation{}, err
	}
	prepared := filepath.Join(parent, ".nexa-restore-"+site.Target.Slug+"-"+token)
	previous := filepath.Join(parent, ".nexa-previous-"+site.Target.Slug+"-"+token)
	if err := os.Mkdir(prepared, 0o700); err != nil {
		return restoreActivation{}, fmt.Errorf("prepare atomic site restore: %w", err)
	}
	preparedExists := true
	defer func() {
		if preparedExists {
			_ = os.RemoveAll(prepared)
		}
	}()
	if !site.Clear {
		if _, err := h.runner.Run(ctx, "cp", []string{"-a", "--", site.Target.RootPath + "/.", prepared}, nil); err != nil {
			return restoreActivation{}, fmt.Errorf("stage current site contents: %w", err)
		}
	}
	if err := extractSiteArchive(archive, prepared); err != nil {
		return restoreActivation{}, fmt.Errorf("extract %s safely: %w", site.Entry, err)
	}
	if err := validateStagedSiteTree(prepared); err != nil {
		return restoreActivation{}, fmt.Errorf("validate restored site tree: %w", err)
	}
	if err := os.Chmod(prepared, targetInfo.Mode().Perm()); err != nil {
		return restoreActivation{}, fmt.Errorf("preserve site root permissions: %w", err)
	}
	if site.Target.UnixUser != "" {
		if _, err := h.runner.Run(ctx, "chown", []string{"-R", "--", site.Target.UnixUser + ":" + site.Target.UnixUser, prepared}, nil); err != nil {
			return restoreActivation{}, fmt.Errorf("prepare restored ownership of %s: %w", site.Target.RootPath, err)
		}
	}

	// Publish by directory rename. The original tree remains at a unique
	// recovery path until the replacement is active and the parent directory is
	// durable, so a failed second rename can be rolled back without data loss.
	if err := os.Rename(site.Target.RootPath, previous); err != nil {
		return restoreActivation{}, fmt.Errorf("retain current site for atomic restore: %w", err)
	}
	if err := syncParentDirectory(parent); err != nil {
		rollbackErr := os.Rename(previous, site.Target.RootPath)
		return restoreActivation{}, errors.Join(fmt.Errorf("persist site restore preparation: %w", err), rollbackErr)
	}
	if err := os.Rename(prepared, site.Target.RootPath); err != nil {
		rollbackErr := os.Rename(previous, site.Target.RootPath)
		if rollbackErr == nil {
			rollbackErr = syncParentDirectory(parent)
		}
		return restoreActivation{}, errors.Join(fmt.Errorf("activate restored site: %w", err), rollbackErr)
	}
	preparedExists = false
	if err := syncParentDirectory(parent); err != nil {
		activation := siteRestoreActivation(site.Target.RootPath, previous, parent)
		return restoreActivation{}, errors.Join(
			fmt.Errorf("persist activated site restore: %w", err),
			activation.rollback(context.WithoutCancel(ctx)),
		)
	}
	return siteRestoreActivation(site.Target.RootPath, previous, parent), nil
}

func siteRestoreActivation(target, previous, parent string) restoreActivation {
	return restoreActivation{
		description: "site " + filepath.Base(target),
		commit: func(context.Context) error {
			if err := os.RemoveAll(previous); err != nil {
				return fmt.Errorf("remove site recovery tree %s: %w", previous, err)
			}
			return syncParentDirectory(parent)
		},
		rollback: func(context.Context) error {
			failed := previous + "-failed-new"
			if err := os.Rename(target, failed); err != nil {
				return fmt.Errorf("retain failed restored site: %w", err)
			}
			if err := os.Rename(previous, target); err != nil {
				repairErr := os.Rename(failed, target)
				return errors.Join(fmt.Errorf("restore original site tree: %w", err), repairErr)
			}
			if err := syncParentDirectory(parent); err != nil {
				return err
			}
			if err := os.RemoveAll(failed); err != nil {
				return fmt.Errorf("remove failed restored site tree: %w", err)
			}
			return syncParentDirectory(parent)
		},
	}
}

func (h *HostOperator) restoreDatabase(ctx context.Context, staging string, database DatabaseRestoreTarget) (restoreActivation, error) {
	dump, err := safeEntry(staging, database.Entry)
	if err != nil {
		return restoreActivation{}, err
	}
	switch database.Target.Engine {
	case "postgres":
		return h.preparePostgresRestore(ctx, database.Target, dump)
	case "mysql", "mariadb":
		return h.prepareMySQLRestore(ctx, staging, database, dump)
	default:
		return restoreActivation{}, fmt.Errorf("unsupported database engine %q", database.Target.Engine)
	}
}

// DeleteCopy purges one copy directory from its account.
func (h *HostOperator) DeleteCopy(ctx context.Context, request DeleteRequest) error {
	if err := ValidateDeleteRequest(request); err != nil {
		return err
	}
	base, env, err := rcloneRemote(request.Account)
	if err != nil {
		return err
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

func verifyStagedEntry(staging, entry, expectedSHA256 string) error {
	path, err := safeEntry(staging, entry)
	if err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("downloaded artifact is not a regular file")
	}
	if expectedSHA256 == "" {
		return nil
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return err
	}
	actual := hex.EncodeToString(digest.Sum(nil))
	if actual != strings.ToLower(expectedSHA256) {
		return fmt.Errorf("SHA-256 mismatch")
	}
	return nil
}
