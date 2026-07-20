import { afterEach, describe, expect, it, vi } from 'vitest'

import { launchTool, listTools, prepareChange } from './api'

afterEach(() => vi.unstubAllGlobals())

describe('Admin Tools API', () => {
  it('plans deployment and launches without putting a secret in the URL', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(Response.json({ items: [{ kind: 'pgadmin', memoryMb: 256 }] }))
      .mockResolvedValueOnce(
        Response.json({ tool: { kind: 'pgadmin' }, job: { id: 1, state: 'queued' } }, { status: 202 }),
      )
      .mockResolvedValueOnce(Response.json({ url: '/tools/pgadmin/' }, { status: 201 }))
    vi.stubGlobal('fetch', fetchMock)

    await expect(listTools()).resolves.toHaveLength(1)
    await prepareChange('pgadmin', 'tool.deploy')
    await expect(
      launchTool('pgadmin', {
        sourceEngine: 'postgresql',
        databaseId: 'db-1',
        accountId: 'role-1',
      }),
    ).resolves.toEqual({ url: '/tools/pgadmin/' })

    expect(fetchMock).toHaveBeenLastCalledWith(
      '/api/v1/admin-tools/pgadmin/launch',
      expect.objectContaining({ method: 'POST' }),
    )
    expect(fetchMock.mock.calls.at(-1)?.[0]).not.toContain('token')
  })
})
