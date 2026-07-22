import { apiRequest } from '@/shared/api/request'

import type { Job } from '../jobs/api'

export type SiteStatus = 'draft' | 'planning' | 'plan_ready' | 'activating' | 'active' | 'rolling_back' | 'rolled_back' | 'deleting' | 'failed'

// These sub-types are structural members of SiteSettings; consumers only ever
// name SiteSettings itself, so they stay file-local (the deadcode gate rejects
// exports nothing imports).
type FpmProcessManager = 'ondemand' | 'dynamic' | 'static'

/** nginx `charset` token. '' means no charset directive is emitted at all. */
type SiteCharset =
  | ''
  | 'off'
  | 'utf-8'
  | 'iso-8859-1'
  | 'iso-8859-2'
  | 'iso-8859-15'
  | 'windows-1251'
  | 'windows-1252'
  | 'koi8-r'
  | 'koi8-u'
  | 'big5'
  | 'euc-jp'
  | 'euc-kr'
  | 'gb2312'
  | 'shift_jis'

interface SiteBasicAuth {
  enabled: boolean
  realm: string
  username: string
  /** Write-only: sent as plaintext, always "" on read (the server never returns the hash). */
  password: string
}

interface SiteRateLimit {
  enabled: boolean
  requestsPerSecond: number
  burst: number
}

type LogRotationFrequency = 'hourly' | 'daily' | 'weekly'

interface SiteLogRotation {
  enabled: boolean
  /** Rotated copies kept before the oldest is discarded (logrotate `rotate N`). */
  keepFiles: number
  frequency: LogRotationFrequency
}

export interface SiteSettings {
  hsts: boolean
  hstsMaxAge: number
  http2: boolean
  http3: boolean
  /** Redirect HTTP to HTTPS with a 301. Only has an effect once the site has a certificate. */
  httpsRedirect: boolean
  /** Served document root, relative to the site's public/ directory. '' serves public/ itself. */
  subdirectory: string
  /** Nginx charset directive. '' emits no directive (the default); 'off' emits `charset off;`. */
  charset: SiteCharset
  gzip: boolean
  gzipLevel: number
  staticCacheDays: number
  clientMaxBodyMb: number
  pmMode: FpmProcessManager
  pmMaxChildren: number
  pmMaxRequests: number
  accessLog: boolean
  errorLog: boolean
  /** Ordered nginx `index` list. The first .php entry is also the front controller `location /` falls back to. */
  indexFiles: string[]
  /** PHP-FPM chdir, relative to the SITE ROOT — not to public/, unlike `subdirectory`. '' chdirs to the site root. */
  workingDirectory: string
  basicAuth: SiteBasicAuth
  rateLimit: SiteRateLimit
  logRotation: SiteLogRotation
}

// Concrete defaults for every field. The server serializes settings with
// `omitempty`, so a never-tuned site returns `{}` and individually-defaulted
// fields are absent — read paths must deep-merge over these to get values to
// render (see mergeSiteSettings).
const DEFAULT_SITE_SETTINGS: SiteSettings = {
  hsts: false,
  hstsMaxAge: 15552000,
  http2: true,
  http3: false,
  httpsRedirect: true,
  subdirectory: '',
  charset: '',
  gzip: true,
  gzipLevel: 5,
  staticCacheDays: 30,
  clientMaxBodyMb: 64,
  pmMode: 'ondemand',
  pmMaxChildren: 3,
  pmMaxRequests: 500,
  accessLog: true,
  errorLog: true,
  indexFiles: ['index.php', 'index.html'],
  workingDirectory: '',
  basicAuth: { enabled: false, realm: 'Restricted', username: '', password: '' },
  rateLimit: { enabled: false, requestsPerSecond: 10, burst: 20 },
  logRotation: { enabled: false, keepFiles: 14, frequency: 'daily' },
}

// The API can omit any field (omitempty), including individual keys of the
// nested basicAuth/rateLimit objects, so the read shape is partial all the way down.
export type PartialSiteSettings = Partial<Omit<SiteSettings, 'basicAuth' | 'rateLimit' | 'logRotation'>> & {
  basicAuth?: Partial<SiteBasicAuth>
  rateLimit?: Partial<SiteRateLimit>
  logRotation?: Partial<SiteLogRotation>
}

/** Deep-merge a sparse settings object (as received from the API) over the defaults. */
export function mergeSiteSettings(partial?: PartialSiteSettings): SiteSettings {
  const source = partial ?? {}
  return {
    ...DEFAULT_SITE_SETTINGS,
    ...source,
    // Copied, never aliased: the defaults object is module-level state and an
    // editable draft must not be able to mutate it.
    indexFiles: [...(source.indexFiles ?? DEFAULT_SITE_SETTINGS.indexFiles)],
    basicAuth: { ...DEFAULT_SITE_SETTINGS.basicAuth, ...(source.basicAuth ?? {}) },
    rateLimit: { ...DEFAULT_SITE_SETTINGS.rateLimit, ...(source.rateLimit ?? {}) },
    logRotation: { ...DEFAULT_SITE_SETTINGS.logRotation, ...(source.logRotation ?? {}) },
  }
}

export interface Site {
  id: string
  slug: string
  displayName: string
  primaryDomain: string
  phpVersion: string
  unixUser: string
  rootPath: string
  socketPath: string
  status: SiteStatus
  // Serialized with omitempty: `{}` (or sparse) for a never-tuned site.
  // Deep-merge over DEFAULT_SITE_SETTINGS before rendering.
  settings: SiteSettings
  lastJobId?: number
  failure?: string
  createdAt: string
  updatedAt: string
}

export interface Runtime {
  engine: 'php'
  version: string
  installed: boolean
  enabled: boolean
  supportStatus: string
}

export interface CreateSiteRequest {
  slug: string
  displayName: string
  primaryDomain: string
  phpVersion: string
}

interface SiteArtifact {
  kind: 'site-root' | 'php-fpm-pool' | 'nginx-site' | 'nginx-ratelimit' | 'nginx-htpasswd' | 'logrotate'
  path: string
  mode: number
  content: string
}

export interface SitePlan {
  site: Site
  artifacts: SiteArtifact[]
  warnings: string[]
}

function request<T>(path: string, init?: RequestInit): Promise<T> {
  return apiRequest<T>(path, init, 'Sites request')
}

export async function listSites(): Promise<Site[]> {
  return (await request<{ items: Site[] }>('/api/v1/sites')).items
}

export async function listRuntimes(): Promise<Runtime[]> {
  return (await request<{ items: Runtime[] }>('/api/v1/runtimes')).items
}

export function createSite(input: CreateSiteRequest): Promise<{ site: Site; job: Job }> {
  return request('/api/v1/sites', { method: 'POST', body: JSON.stringify(input) })
}

export function getSitePlan(siteId: string): Promise<{ plan: SitePlan; expiresAt: string }> {
  return request(`/api/v1/sites/${encodeURIComponent(siteId)}/plan`)
}

export function activateSite(siteId: string): Promise<Job> {
  return request(`/api/v1/sites/${encodeURIComponent(siteId)}/activate`, { method: 'POST' })
}

export function rollbackSite(siteId: string): Promise<Job> {
  return request(`/api/v1/sites/${encodeURIComponent(siteId)}/rollback`, { method: 'POST' })
}
export function prepareSitePlan(siteId: string): Promise<Job> {
  return request(`/api/v1/sites/${encodeURIComponent(siteId)}/plan`, { method: 'POST' })
}

// Persists per-site Nginx/PHP-FPM settings. The server answers 202 + a Job
// (when the site is `active` or `plan_ready`, a job re-applies/re-plans) or
// 200 + the updated Site (draft/rolled_back/failed persist only). Callers
// discriminate on a numeric `id` + `state` to decide whether to follow a job.
// basicAuth.password is plaintext and write-only; sending "" while enabled with
// the same username keeps the existing password.
export function updateSiteSettings(siteId: string, settings: SiteSettings): Promise<Job | Site> {
  return request(`/api/v1/sites/${encodeURIComponent(siteId)}/settings`, {
    method: 'PATCH',
    body: JSON.stringify(settings),
  })
}

// Enqueues a `site.delete` job (202). The 409 guards (site_busy,
// site_has_dependents) reject before a job is queued, so the caller's
// runner surfaces the server message as a queue-time failure.
export function deleteSite(siteId: string): Promise<Job> {
  return request(`/api/v1/sites/${encodeURIComponent(siteId)}`, { method: 'DELETE' })
}
