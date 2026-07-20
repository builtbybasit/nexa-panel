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

// userIniPath is where PHP-FPM reads a site's per-directory overrides: the .user.ini
// at the document root, which is the site's public/ directory.
func userIniPath(rootPath string) string {
	return filepath.Join(rootPath, "public", ".user.ini")
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
	overrides, err := readININil(userIniPath(scope.RootPath))
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
	path := userIniPath(scope.RootPath)
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
