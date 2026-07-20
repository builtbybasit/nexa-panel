import { afterEach, describe, expect, it, vi } from 'vitest'

import { applyCertificate, createCertificate, listCertificates, prepareCertificate } from './api'

afterEach(() => vi.unstubAllGlobals())

describe('certificates API', () => {
  it('creates, plans, and applies certificate operations', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(Response.json({ items: [] }))
      .mockImplementation(() =>
        Promise.resolve(Response.json({ id: 2, state: 'queued' }, { status: 202 })),
      )
    vi.stubGlobal('fetch', fetchMock)

    await expect(listCertificates()).resolves.toEqual([])
    await createCertificate('site-1', 'admin@example.com')
    await prepareCertificate('cert-1', 'renew')
    await applyCertificate('cert-1')

    expect(fetchMock).toHaveBeenLastCalledWith('/api/v1/certificates/cert-1/apply', {
      method: 'POST',
      credentials: 'same-origin',
      headers: { Accept: 'application/json' },
    })
  })
})
