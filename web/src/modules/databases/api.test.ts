import { afterEach, describe, expect, it, vi } from 'vitest'
import { createDatabase, createUser, listServers, restoreBackup, setUserPassword } from './api'

afterEach(() => vi.unstubAllGlobals())

describe('Databases API', () => {
  it('lists servers and creates users and databases with one-click direct requests', async () => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(Response.json({ items: [] }))
      .mockResolvedValueOnce(Response.json({ user: { id: 'user_1' }, job: { id: 1, state: 'queued' } }, { status: 202 }))
      .mockResolvedValueOnce(Response.json({ database: { id: 'database_1' }, job: { id: 2, state: 'queued' } }, { status: 202 }))
      .mockResolvedValueOnce(Response.json({ user: { id: 'user_1' }, job: { id: 3, state: 'queued' } }, { status: 202 }))
      .mockResolvedValueOnce(Response.json({ restorePoint: { id: 'restore_1' }, job: { id: 4, state: 'queued' } }, { status: 202 }))
    vi.stubGlobal('fetch', fetchMock)
    await expect(listServers()).resolves.toEqual([])
    await createUser({ serverId: 'mysql', name: 'app_user', host: 'localhost', password: 'generated-secret-1' })
    expect(fetchMock).toHaveBeenLastCalledWith('/api/v1/databases/users', expect.objectContaining({ method: 'POST' }))
    await createDatabase({ serverId: 'mysql', name: 'app_db', ownerUserId: 'user_1' })
    expect(fetchMock).toHaveBeenLastCalledWith('/api/v1/databases', expect.objectContaining({ method: 'POST' }))
    await setUserPassword('user_1', 'generated-secret-2')
    expect(fetchMock).toHaveBeenLastCalledWith('/api/v1/databases/users/user_1/password', expect.objectContaining({ method: 'POST', credentials: 'same-origin' }))
    await restoreBackup('restore_1')
    expect(fetchMock).toHaveBeenLastCalledWith('/api/v1/databases/restore-points/restore_1/restore', expect.objectContaining({ method: 'POST' }))
  })
})
