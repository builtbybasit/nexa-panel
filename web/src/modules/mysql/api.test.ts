import { afterEach, describe, expect, it, vi } from 'vitest'

import { createAccount, listEngines, revealCredential } from './api'

afterEach(() => vi.unstubAllGlobals())

describe('MySQL-family API', () => {
  it('creates an account without returning its one-time credential', async () => {
    const createResponse = { account: { id: 'account_1' }, job: { id: 1, state: 'queued' } }
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(Response.json({ items: [{ id: 'mysql', kind: 'mysql' }] }))
      .mockResolvedValueOnce(Response.json(createResponse, { status: 202 }))
      .mockResolvedValueOnce(Response.json({ credential: 'one-time' }))
    vi.stubGlobal('fetch', fetchMock)

    await expect(listEngines()).resolves.toHaveLength(1)
    await expect(createAccount({ engineId: 'mysql', name: 'app_user' })).resolves.toEqual(
      createResponse,
    )
    expect(createResponse).not.toHaveProperty('credential')
    await expect(revealCredential('account_1')).resolves.toBe('one-time')

    expect(fetchMock).toHaveBeenLastCalledWith(
      '/api/v1/mysql-family/accounts/account_1/credential',
      expect.objectContaining({ method: 'POST' }),
    )
  })
})
