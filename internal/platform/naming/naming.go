// Package naming is the single source of truth for the identifier shapes the
// panel validates everywhere and the deterministic values it derives from a
// managed site's slug. Centralising the slug/hostname/php-version shapes and the
// slug→unix-user derivation keeps a security invariant — "the identifier a slug
// implies" — from drifting between the modules that assign these values and the
// operators that re-validate them on the node.
package naming

import (
	"regexp"
	"strings"
)

// siteSlugPattern is the shape the sites module assigns and every operator
// re-validates: a lowercase letter followed by 1–31 more lowercase letters,
// digits, or hyphens.
var siteSlugPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{1,31}$`)

// ValidSiteSlug reports whether slug is a well-formed managed-site slug.
func ValidSiteSlug(slug string) bool {
	return siteSlugPattern.MatchString(slug)
}

// SiteUnixUser returns the operating-system account a site with the given slug
// owns. Hyphens become underscores because unix usernames disallow them; the
// "nexa_" prefix namespaces every managed account.
func SiteUnixUser(slug string) string {
	return "nexa_" + strings.ReplaceAll(slug, "-", "_")
}

// hostnamePattern matches a multi-label DNS hostname: each label is 1–63 chars,
// starts and ends alphanumeric, and may contain interior hyphens.
var hostnamePattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+$`)

// ValidHostname reports whether host is a well-formed multi-label DNS hostname.
// It does not bound the total length; a caller enforcing the 253-octet DNS limit
// must still check len(host) itself.
func ValidHostname(host string) bool {
	return hostnamePattern.MatchString(host)
}

// phpVersionShapePattern gates the "major.minor" shape only (e.g. "8.3"). It is
// deliberately not a floor: whether a branch is actually supported or installed
// is decided by the caller, so this stays a pure syntactic gate. Callers that
// must enforce the supported-version floor use their own stricter pattern.
var phpVersionShapePattern = regexp.MustCompile(`^[0-9]{1,2}\.[0-9]{1,2}$`)

// ValidPHPVersionShape reports whether version has the major.minor shape.
func ValidPHPVersionShape(version string) bool {
	return phpVersionShapePattern.MatchString(version)
}
