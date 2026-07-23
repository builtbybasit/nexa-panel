import { afterEach, describe, expect, it, vi } from 'vitest'

import { activateDomain, createDomain, deleteDomain, listDomains } from './api'

afterEach(() => vi.unstubAllGlobals())

describe('domains API', () => {
  it('lists, creates, and activates routing identities', async () => {
    const created = {
      domain: { id: 'domain-1', hostname: 'www.example.com' },
      job: { id: 3, state: 'queued' },
    }
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(Response.json({ items: [] }))
      .mockResolvedValueOnce(Response.json(created, { status: 202 }))
      .mockResolvedValueOnce(Response.json({ id: 4, state: 'queued' }, { status: 202 }))
    vi.stubGlobal('fetch', fetchMock)

    await expect(listDomains()).resolves.toEqual([])
    await expect(
      createDomain({ siteId: 'site-1', hostname: 'www.example.com', kind: 'alias' }),
    ).resolves.toEqual(created)
    await activateDomain('domain-1')

    expect(fetchMock).toHaveBeenNthCalledWith(3, '/api/v1/domains/domain-1/activate', {
      method: 'POST',
      credentials: 'same-origin',
      headers: { Accept: 'application/json' },
    })
  })

  it('enqueues a remove job', async () => {
    const fetchMock = vi.fn().mockResolvedValue(Response.json({ id: 8, state: 'queued' }, { status: 202 }))
    vi.stubGlobal('fetch', fetchMock)
    await expect(deleteDomain('domain-1')).resolves.toEqual({ id: 8, state: 'queued' })
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/domains/domain-1', {
      method: 'DELETE',
      credentials: 'same-origin',
      headers: { Accept: 'application/json' },
    })
  })

  it('surfaces the primary-protected guard message', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        Response.json(
          { code: 'domain_primary_protected', message: 'The primary domain is removed by deleting its site.' },
          { status: 409 },
        ),
      ),
    )
    await expect(deleteDomain('domain-1')).rejects.toThrow('The primary domain is removed by deleting its site.')
  })
})
