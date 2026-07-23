import { afterEach, describe, expect, it, vi } from 'vitest'

import {
  activateSite,
  createSite,
  deleteSite,
  getSitePlan,
  listRuntimes,
  listSites,
  mergeSiteSettings,
  rollbackSite,
  updateSiteSettings,
} from './api'

// The exact default contract, owned by the test so any drift in api.ts fails
// here. mergeSiteSettings({}) must reproduce it for a never-tuned site.
const EXPECTED_DEFAULT_SETTINGS = {
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

afterEach(() => vi.unstubAllGlobals())

describe('sites API', () => {
  it('lists sites and discovered runtimes', async () => {
    const fetchMock = vi.fn().mockResolvedValueOnce(Response.json({ items: [{ id: 'site_1', slug: 'demo' }] })).mockResolvedValueOnce(
      Response.json({ items: [{ engine: 'php', version: '8.4', installed: true, enabled: true }] }),
    )
    vi.stubGlobal('fetch', fetchMock)
    await expect(listSites()).resolves.toEqual([{ id: 'site_1', slug: 'demo' }])
    await expect(listRuntimes()).resolves.toEqual([{ engine: 'php', version: '8.4', installed: true, enabled: true }])
  })

  it('creates a site and loads its generated plan', async () => {
    const input = { slug: 'demo-site', displayName: 'Demo Site', primaryDomain: 'demo.example.com', phpVersion: '8.4' }
    const response = { site: { id: 'site_1', ...input, status: 'planning' }, job: { id: 7, state: 'queued' } }
    const plan = { plan: { site: response.site, artifacts: [], warnings: [] }, expiresAt: '2026-07-16T01:00:00Z' }
    const fetchMock = vi.fn().mockResolvedValueOnce(Response.json(response, { status: 202 })).mockResolvedValueOnce(Response.json(plan))
    vi.stubGlobal('fetch', fetchMock)
    await expect(createSite(input)).resolves.toEqual(response)
    await expect(getSitePlan('site_1')).resolves.toEqual(plan)
    expect(fetchMock).toHaveBeenNthCalledWith(1, '/api/v1/sites', {
      method: 'POST', body: JSON.stringify(input), credentials: 'same-origin',
      headers: { Accept: 'application/json', 'Content-Type': 'application/json' },
    })
  })

  it('surfaces safe validation errors', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(Response.json({ message: 'PHP 8.9 is not installed.' }, { status: 422 })))
    await expect(createSite({ slug: 'demo', displayName: 'Demo', primaryDomain: 'demo.test', phpVersion: '8.9' })).rejects.toThrow('PHP 8.9 is not installed.')
  })

  it('queues activation and rollback', async () => {
    const fetchMock = vi.fn().mockImplementation(() => Promise.resolve(Response.json({ id: 9, state: 'queued' }, { status: 202 })))
    vi.stubGlobal('fetch', fetchMock)
    await activateSite('site_1')
    await rollbackSite('site_1')
    expect(fetchMock).toHaveBeenNthCalledWith(1, '/api/v1/sites/site_1/activate', {
      method: 'POST', credentials: 'same-origin', headers: { Accept: 'application/json' },
    })
    expect(fetchMock).toHaveBeenNthCalledWith(2, '/api/v1/sites/site_1/rollback', {
      method: 'POST', credentials: 'same-origin', headers: { Accept: 'application/json' },
    })
  })

  it('enqueues a delete job and returns it', async () => {
    const fetchMock = vi.fn().mockResolvedValue(Response.json({ id: 11, state: 'queued' }, { status: 202 }))
    vi.stubGlobal('fetch', fetchMock)
    await expect(deleteSite('site_1')).resolves.toEqual({ id: 11, state: 'queued' })
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/sites/site_1', {
      method: 'DELETE', credentials: 'same-origin', headers: { Accept: 'application/json' },
    })
  })

  it('PATCHes site settings with the settings body', async () => {
    const settings = { ...mergeSiteSettings({}), gzip: false, pmMaxChildren: 12 }
    const fetchMock = vi.fn().mockResolvedValue(Response.json({ id: 42, state: 'queued' }, { status: 202 }))
    vi.stubGlobal('fetch', fetchMock)
    await expect(updateSiteSettings('site_1', settings)).resolves.toEqual({ id: 42, state: 'queued' })
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/sites/site_1/settings', {
      method: 'PATCH',
      body: JSON.stringify(settings),
      credentials: 'same-origin',
      headers: { Accept: 'application/json', 'Content-Type': 'application/json' },
    })
  })

  it('fills a sparse settings read with the exact defaults (drift guard)', () => {
    // A never-tuned site serializes as `{}` (omitempty); the merge fills every
    // field, and this asserts the full default contract in one shot.
    expect(mergeSiteSettings({})).toEqual(EXPECTED_DEFAULT_SETTINGS)
  })

  it('deep-merges nested settings objects field-by-field', () => {
    const merged = mergeSiteSettings({ gzip: false, basicAuth: { enabled: true }, logRotation: { enabled: true } })
    expect(merged.gzip).toBe(false)
    expect(merged.basicAuth).toEqual({ enabled: true, realm: 'Restricted', username: '', password: '' })
    expect(merged.rateLimit).toEqual(EXPECTED_DEFAULT_SETTINGS.rateLimit)
    expect(merged.logRotation).toEqual({ enabled: true, keepFiles: 14, frequency: 'daily' })
    expect(mergeSiteSettings({ httpsRedirect: false }).httpsRedirect).toBe(false)
  })

  it('does not alias the default index list into merged settings', () => {
    const merged = mergeSiteSettings({})
    merged.indexFiles.push('app.php')
    expect(mergeSiteSettings({}).indexFiles).toEqual(['index.php', 'index.html'])
  })

  it('surfaces the dependents guard message on a blocked delete', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        Response.json(
          { code: 'site_has_dependents', message: 'Remove www.example.com and its TLS certificate first.' },
          { status: 409 },
        ),
      ),
    )
    await expect(deleteSite('site_1')).rejects.toThrow('Remove www.example.com and its TLS certificate first.')
  })
})
