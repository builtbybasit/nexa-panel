// Pure derivation and validation helpers for the site-creation wizard. The
// backend derives the real values from the slug it receives; everything here
// only has to agree with those rules closely enough to preview them.

function slugify(v: string): string {
  return v
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
    .slice(0, 32)
}

/** Append -2, -3, … until the candidate is free, respecting the length cap. */
function uniquify(base: string, taken: Set<string>, maxLength: number, separator: string, fallback: string): string {
  let candidate = base
  let n = 2
  while (taken.has(candidate)) {
    const suffix = separator + n++
    const trimmed = base.slice(0, maxLength - suffix.length).replace(new RegExp(`\\${separator}+$`), '')
    candidate = (trimmed || fallback) + suffix
  }
  return candidate
}

/** Silent slug for the panel: derived from the name (or domain), kept unique. */
export function deriveSlug(name: string, domain: string, taken: Set<string>): string {
  let base = slugify(name) || slugify(domain) || 'site'
  if (!/^[a-z]/.test(base)) base = ('site-' + base).slice(0, 32)
  base = base.replace(/-+$/, '') || 'site'
  if (base.length < 2) base = base + '-1'
  return uniquify(base, taken, 32, '-', 'site')
}

// Database identifiers must satisfy every engine we ship (MySQL caps users at
// 32 chars, so stay comfortably below) and the panel's own naming rule:
// lowercase letters, digits, underscores, starting with a letter.
const DB_IDENTIFIER_MAX = 24

/** `example-com` → `example_com`, valid for both engines and unique. */
export function deriveDbIdentifier(slug: string, taken: Set<string>): string {
  let base = slug.replaceAll('-', '_').replace(/[^a-z0-9_]/g, '').slice(0, DB_IDENTIFIER_MAX)
  if (!/^[a-z]/.test(base)) base = ('db_' + base).slice(0, DB_IDENTIFIER_MAX)
  base = base.replace(/_+$/, '')
  if (base.length < 2) base = 'db_1'
  return uniquify(base, taken, DB_IDENTIFIER_MAX, '_', 'db')
}

// Each label 1–63 chars, alphanumeric boundaries, at least two labels.
const HOSTNAME_RE = /^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)+$/

/** '' when the domain is acceptable, otherwise the error to show inline. */
export function validateDomain(raw: string, existingDomains: string[]): string {
  const domain = raw.trim().toLowerCase()
  if (!domain) return 'Enter a primary domain.'
  if (domain.length > 253 || !HOSTNAME_RE.test(domain)) {
    return 'Enter a valid hostname, for example portal.example.com.'
  }
  if (existingDomains.some((existing) => existing.toLowerCase() === domain)) {
    return 'A site with this domain already exists.'
  }
  return ''
}

/** Panel naming rule for database names and logins. */
export const DB_NAME_RULE = /^[a-z][a-z0-9_]+$/

/**
 * Backend-derived previews (never sent — the backend derives all three from
 * the slug). Shown so nothing about the new site feels like a surprise.
 */
export function sitePreviews(slug: string): { unixUser: string; rootPath: string; socketPath: string } {
  return {
    unixUser: 'nexa_' + slug.replaceAll('-', '_'),
    rootPath: '/srv/nexa/sites/' + slug,
    socketPath: '/run/php/nexa-' + slug + '.sock',
  }
}
