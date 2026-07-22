package php

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// slugPattern matches the sites module's own slug shape. It is one half of the
// per-site confinement: the .user.ini path is derived only from a slug of this
// shape whose root path resolves back to sitesRoot/slug.
var slugPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{1,31}$`)

const (
	releaseDirName  = "app"
	currentLinkName = "current"
	publicDirName   = "public"
	userIniName     = ".user.ini"
)

// userIniPath is where PHP-FPM reads a site's per-directory overrides: the
// .user.ini in the directory nginx serves as DOCUMENT_ROOT. PHP scans
// user_ini.filename from the executing script's directory up to DOCUMENT_ROOT
// and no further, so a copy anywhere else is simply never read.
//
// In standard mode that directory is {root}/public. In deployer mode the vhost
// serves {root}/app/current/public, and {root}/public is a *sibling* of the
// release tree rather than an ancestor of it — writing there would leave an
// override the panel reports as active and PHP never applies. The override
// therefore lives in the release the site is serving right now, which does mean
// a deploy that publishes a new release replaces it; every read here opens the
// same path it writes, so what the page shows is always what PHP reads.
func userIniPath(scope SiteScope) (string, error) {
	if scope.DeploymentMode != DeploymentModeDeployer {
		return filepath.Join(scope.RootPath, publicDirName, userIniName), nil
	}
	served, err := currentReleaseDocumentRoot(scope.RootPath)
	if err != nil {
		return "", err
	}
	return filepath.Join(served, userIniName), nil
}

// currentReleaseDocumentRoot resolves {root}/app/current/public, refusing a path
// that leaves the release tree. That check is not decoration: `current` (and
// everything below it) is owned and re-pointed by the site account on every
// deploy, while this code runs as root. Following it blindly would let a site
// aim a root-owned write at any directory on the host. It is the same
// containment nginx gets from `disable_symlinks if_not_owner from={root}/app`,
// asserted by path here because this write does not go through nginx.
func currentReleaseDocumentRoot(rootPath string) (string, error) {
	releaseRoot := filepath.Join(rootPath, releaseDirName)
	resolvedRoot, err := filepath.EvalSymlinks(releaseRoot)
	if err != nil {
		return "", fmt.Errorf("this site has no release tree yet: %w", err)
	}
	served, err := filepath.EvalSymlinks(filepath.Join(releaseRoot, currentLinkName, publicDirName))
	if err != nil {
		return "", fmt.Errorf("resolve the site's current release: %w", err)
	}
	if served != resolvedRoot && !strings.HasPrefix(served, resolvedRoot+string(filepath.Separator)) {
		return "", errors.New("the site's current release points outside its release tree")
	}
	return served, nil
}

// expectedUnixUser mirrors how the sites module derives a site's unix user, so a
// scope whose user does not match its slug is rejected before any write.
func expectedUnixUser(slug string) string {
	return "nexa_" + strings.ReplaceAll(slug, "-", "_")
}

// normalizeSiteScope is the per-site security boundary. The slug must be
// well-shaped, the branch installed, the root path exactly sitesRoot/slug, and
// the unix user the one that slug implies. Only then is a .user.ini path built.
func (o *HostOperator) normalizeSiteScope(ctx context.Context, scope SiteScope) (SiteScope, error) {
	scope.Slug = strings.TrimSpace(scope.Slug)
	if !slugPattern.MatchString(scope.Slug) {
		return SiteScope{}, fmt.Errorf("site slug %q is invalid", scope.Slug)
	}
	version, err := o.normalizeVersion(ctx, scope.Version)
	if err != nil {
		return SiteScope{}, err
	}
	scope.Version = version
	expectedRoot := filepath.Join(o.sitesRoot, scope.Slug)
	if filepath.Clean(scope.RootPath) != expectedRoot {
		return SiteScope{}, fmt.Errorf("site %q root path is outside the sites root", scope.Slug)
	}
	scope.RootPath = expectedRoot
	if strings.TrimSpace(scope.UnixUser) != expectedUnixUser(scope.Slug) {
		return SiteScope{}, fmt.Errorf("site %q unix user does not match its slug", scope.Slug)
	}
	scope.UnixUser = expectedUnixUser(scope.Slug)
	// An unknown mode is refused rather than defaulted: guessing would silently
	// resolve the override to a directory PHP does not scan, which is exactly
	// the failure this field exists to prevent.
	switch strings.TrimSpace(scope.DeploymentMode) {
	case "", DeploymentModeStandard:
		scope.DeploymentMode = DeploymentModeStandard
	case DeploymentModeDeployer:
		scope.DeploymentMode = DeploymentModeDeployer
	default:
		return SiteScope{}, fmt.Errorf("site %q deployment mode is invalid", scope.Slug)
	}
	return scope, nil
}

// editableViaUserIni reports whether a directive can take effect from a .user.ini
// file. PHP honours only PHP_INI_ALL, PHP_INI_PERDIR, and PHP_INI_USER directives
// per-directory; PHP_INI_SYSTEM directives must be set branch-wide, so surfacing
// them here would let an operator save a value that silently never applies.
func editableViaUserIni(access string) bool {
	switch access {
	case "PHP_INI_ALL", "PHP_INI_PERDIR", "PHP_INI_USER":
		return true
	default:
		return false
	}
}

// SiteSettings reports the directives a site may override, each carrying the
// branch-wide effective value overlaid with any per-site .user.ini override.
// Directives a .user.ini cannot influence are excluded so the editor never
// offers a change that would not take effect.
func (o *HostOperator) SiteSettings(ctx context.Context, scope SiteScope) ([]Directive, error) {
	scope, err := o.normalizeSiteScope(ctx, scope)
	if err != nil {
		return nil, err
	}
	global, err := o.Settings(ctx, scope.Version)
	if err != nil {
		return nil, err
	}
	path, err := userIniPath(scope)
	if err != nil {
		return nil, err
	}
	overrides, err := readININil(path)
	if err != nil {
		return nil, err
	}
	directives := make([]Directive, 0, len(global))
	for _, directive := range global {
		if !editableViaUserIni(directive.Access) {
			continue
		}
		override, ok := overrides[directive.Name]
		if ok {
			directive.Value = override
			directive.Managed = true
		} else {
			directive.Managed = false
		}
		directives = append(directives, directive)
	}
	return directives, nil
}

// writeUserIni rewrites a site's .user.ini from the full desired override set and
// hands ownership to the site's unix user. An empty set removes the file so the
// site falls back cleanly to the branch-wide settings.
func (o *HostOperator) writeUserIni(scope SiteScope, values map[string]string) error {
	path, err := userIniPath(scope)
	if err != nil {
		return err
	}
	if len(values) == 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove site .user.ini: %w", err)
		}
		return nil
	}
	var builder strings.Builder
	builder.WriteString("; Managed by Nexa Panel. Per-site PHP overrides for " + scope.Slug + ".\n")
	for _, key := range sortedKeys(values) {
		builder.WriteString(key)
		builder.WriteString(" = ")
		builder.WriteString(values[key])
		builder.WriteByte('\n')
	}
	if err := os.WriteFile(path, []byte(builder.String()), 0o644); err != nil {
		return fmt.Errorf("write site .user.ini: %w", err)
	}
	if err := o.owner.Chown(path, scope.UnixUser); err != nil {
		return fmt.Errorf("set site .user.ini ownership: %w", err)
	}
	return nil
}
