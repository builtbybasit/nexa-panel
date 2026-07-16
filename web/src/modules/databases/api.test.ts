import { afterEach, describe, expect, it, vi } from 'vitest'
import { applyPlan, createBackup, createInstance, listInstances, revealCredential } from './api'

afterEach(() => vi.unstubAllGlobals())

describe('PostgreSQL API', () => {
  it('discovers, provisions, backs up, applies reviewed plans, and reveals credentials explicitly', async () => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(Response.json({ items: [] }))
      .mockResolvedValueOnce(Response.json({ instance: { id: 'postgresql_18_nexa_main' }, job: { id: 1, state: 'queued' } }, { status: 202 }))
      .mockResolvedValueOnce(Response.json({ restorePoint: { id: 'restore_1' }, job: { id: 2, state: 'queued' } }, { status: 202 }))
      .mockResolvedValueOnce(Response.json({ id: 3, state: 'queued' }, { status: 202 }))
      .mockResolvedValueOnce(Response.json({ credential: 'one-time-secret' }))
    vi.stubGlobal('fetch', fetchMock)
    await expect(listInstances()).resolves.toEqual([])
    await createInstance({ version: '18', cluster: 'nexa_main' })
    await createBackup('database_1')
    await applyPlan('restore-points', 'restore_1')
    await expect(revealCredential('role_1')).resolves.toBe('one-time-secret')
    expect(fetchMock).toHaveBeenLastCalledWith('/api/v1/postgresql/roles/role_1/credential', expect.objectContaining({ method: 'POST', credentials: 'same-origin' }))
  })
})
