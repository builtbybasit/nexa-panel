import { afterEach, describe, expect, it, vi } from 'vitest'

import { activateSite, createSite, getSitePlan, listRuntimes, listSites, rollbackSite } from './api'

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
})
