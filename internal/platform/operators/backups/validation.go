package backups

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/nexa-panel/nexa-panel/internal/platform/naming"
)

const (
	defaultSiteRoot    = "/srv/nexa/sites"
	maxRetentionCopies = 1000
	maxBackupTargets   = 128
)

var (
	backupTokenPattern     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)
	databaseNamePattern    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_$]{0,63}$`)
	postgresVersionPattern = regexp.MustCompile(`^[0-9]{1,2}$`)
	sha256Pattern          = regexp.MustCompile(`^[a-fA-F0-9]{64}$`)
)

// ValidateRunRequest validates the untrusted control-plane payload before the
// privileged agent creates files or invokes a command.
func ValidateRunRequest(request RunRequest) error {
	return validateRunRequest(request, defaultSiteRoot)
}

func validateRunRequest(request RunRequest, siteRoot string) error {
	if request.StagingRoot != "" {
		return errors.New("backup staging root is agent-owned and must not be supplied")
	}
	if err := validateBackupToken("plan ID", request.PlanID); err != nil {
		return err
	}
	if err := validateBackupToken("copy name", request.CopyName); err != nil {
		return err
	}
	if request.Limit < 0 || request.Limit > maxRetentionCopies {
		return fmt.Errorf("backup retention limit must be between 0 and %d", maxRetentionCopies)
	}
	if len(request.Sites) == 0 && len(request.Databases) == 0 {
		return errors.New("backup request must select at least one target")
	}
	if len(request.Sites)+len(request.Databases) > maxBackupTargets {
		return fmt.Errorf("backup request exceeds the %d-target limit", maxBackupTargets)
	}
	entries := make(map[string]struct{}, len(request.Sites)+len(request.Databases))
	for index, site := range request.Sites {
		if err := validateSiteTarget(site, siteRoot); err != nil {
			return fmt.Errorf("site target %d: %w", index, err)
		}
		entry := "site-" + site.Slug + ".tar.gz"
		if _, duplicate := entries[entry]; duplicate {
			return fmt.Errorf("site target %d duplicates backup entry %s", index, entry)
		}
		entries[entry] = struct{}{}
	}
	for index, database := range request.Databases {
		if err := validateDatabaseTarget(database); err != nil {
			return fmt.Errorf("database target %d: %w", index, err)
		}
		entry := databaseBackupEntry(database)
		if _, duplicate := entries[entry]; duplicate {
			return fmt.Errorf("database target %d duplicates backup entry %s", index, entry)
		}
		entries[entry] = struct{}{}
	}
	return nil
}

// ValidateRestoreRequest applies the same trust boundary to destructive
// restore targets. In particular, Clear may only ever operate on a derived
// managed-site root.
func ValidateRestoreRequest(request RestoreRequest) error {
	return validateRestoreRequest(request, defaultSiteRoot)
}

func validateRestoreRequest(request RestoreRequest, siteRoot string) error {
	if request.StagingRoot != "" {
		return errors.New("backup staging root is agent-owned and must not be supplied")
	}
	if err := validateBackupToken("plan ID", request.PlanID); err != nil {
		return err
	}
	if err := validateBackupToken("copy name", request.CopyName); err != nil {
		return err
	}
	if len(request.Sites) == 0 && len(request.Databases) == 0 {
		return errors.New("restore request must select at least one target")
	}
	if len(request.Sites)+len(request.Databases) > maxBackupTargets {
		return fmt.Errorf("restore request exceeds the %d-target limit", maxBackupTargets)
	}
	entries := make(map[string]struct{}, len(request.Sites)+len(request.Databases))
	destinations := make(map[string]struct{}, len(request.Sites)+len(request.Databases))
	for index, site := range request.Sites {
		if _, err := safeEntry(".", site.Entry); err != nil {
			return fmt.Errorf("site restore target %d: %w", index, err)
		}
		if !strings.HasPrefix(site.Entry, "site-") || !strings.HasSuffix(site.Entry, ".tar.gz") {
			return fmt.Errorf("site restore target %d does not reference a site archive", index)
		}
		if err := validateSiteTarget(site.Target, siteRoot); err != nil {
			return fmt.Errorf("site restore target %d is not a managed site: %w", index, err)
		}
		if site.SHA256 != "" && !sha256Pattern.MatchString(site.SHA256) {
			return fmt.Errorf("site restore target %d has an invalid SHA-256", index)
		}
		if _, duplicate := entries[site.Entry]; duplicate {
			return fmt.Errorf("restore entry %s is selected more than once", site.Entry)
		}
		if _, duplicate := destinations[site.Target.RootPath]; duplicate {
			return fmt.Errorf("site restore destination %s is selected more than once", site.Target.RootPath)
		}
		entries[site.Entry] = struct{}{}
		destinations[site.Target.RootPath] = struct{}{}
	}
	for index, database := range request.Databases {
		if _, err := safeEntry(".", database.Entry); err != nil {
			return fmt.Errorf("database restore target %d: %w", index, err)
		}
		if !strings.HasPrefix(database.Entry, "db-") {
			return fmt.Errorf("database restore target %d does not reference a database dump", index)
		}
		if err := validateDatabaseTarget(database.Target); err != nil {
			return fmt.Errorf("database restore target %d: %w", index, err)
		}
		if database.SHA256 != "" && !sha256Pattern.MatchString(database.SHA256) {
			return fmt.Errorf("database restore target %d has an invalid SHA-256", index)
		}
		destination := database.Target.Engine + ":" + database.Target.Socket + ":" + database.Target.Name
		if _, duplicate := entries[database.Entry]; duplicate {
			return fmt.Errorf("restore entry %s is selected more than once", database.Entry)
		}
		if _, duplicate := destinations[destination]; duplicate {
			return fmt.Errorf("database restore destination %s is selected more than once", database.Target.Name)
		}
		entries[database.Entry] = struct{}{}
		destinations[destination] = struct{}{}
	}
	return nil
}

func databaseBackupEntry(database DatabaseTarget) string {
	if database.Engine == "postgres" {
		return "db-postgres-" + database.Name + ".dump"
	}
	return "db-" + database.Engine + "-" + database.Name + ".sql"
}

func ValidateDeleteRequest(request DeleteRequest) error {
	if err := validateBackupToken("plan ID", request.PlanID); err != nil {
		return err
	}
	return validateBackupToken("copy name", request.CopyName)
}

func validateBackupToken(label, value string) error {
	if !backupTokenPattern.MatchString(value) {
		return fmt.Errorf("backup %s is invalid", label)
	}
	return nil
}

func validateSiteTarget(site SiteTarget, siteRoot string) error {
	if !naming.ValidSiteSlug(site.Slug) {
		return errors.New("slug is not a managed site slug")
	}
	if site.RootPath != filepath.Join(siteRoot, site.Slug) {
		return errors.New("root path does not match the managed site layout")
	}
	wantUser := naming.SiteUnixUser(site.Slug)
	if site.UnixUser != wantUser {
		return errors.New("unix user does not match the managed site layout")
	}
	return nil
}

func validateDatabaseTarget(database DatabaseTarget) error {
	if !databaseNamePattern.MatchString(database.Name) {
		return errors.New("database name is invalid")
	}
	switch database.Engine {
	case "postgres":
		if !postgresVersionPattern.MatchString(database.Version) {
			return errors.New("PostgreSQL version is invalid")
		}
		if database.Port < 1 || database.Port > 65535 {
			return errors.New("PostgreSQL port is invalid")
		}
		if !pathWithinRuntime(database.Socket, "/run/postgresql", "/var/run/postgresql") {
			return errors.New("PostgreSQL socket path is outside the managed runtime directory")
		}
	case "mysql", "mariadb":
		if !pathWithinRuntime(database.Socket, "/run/mysqld", "/var/run/mysqld") {
			return errors.New("MySQL-family socket path is outside the managed runtime directory")
		}
	default:
		return errors.New("database engine is unsupported")
	}
	return nil
}

func pathWithinRuntime(candidate string, roots ...string) bool {
	if !filepath.IsAbs(candidate) || filepath.Clean(candidate) != candidate {
		return false
	}
	for _, root := range roots {
		relative, err := filepath.Rel(root, candidate)
		if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}
