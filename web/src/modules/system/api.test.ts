import { afterEach, describe, expect, it, vi } from 'vitest'

import { getSystemOverview, type SystemOverview } from './api'

const overview: SystemOverview = {
  observedAt: '2026-07-16T00:00:00Z',
  memory: {
    supported: true,
    totalBytes: 2_147_483_648,
    availableBytes: 1_073_741_824,
    swapTotalBytes: 1_073_741_824,
    swapFreeBytes: 1_073_741_824,
    usedPercent: 50,
    profile: 'compact',
  },
  podman: {
    available: true,
    version: '6.0.1',
    path: '/usr/bin/podman',
  },
  warnings: [],
}

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('getSystemOverview', () => {
  it('returns the observed node state', async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(JSON.stringify(overview), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      }),
    )
    vi.stubGlobal('fetch', fetchMock)

    await expect(getSystemOverview()).resolves.toEqual(overview)
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/system/overview', {
      headers: { Accept: 'application/json' },
    })
  })

  it('rejects unsuccessful responses', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(null, { status: 503 })))

    await expect(getSystemOverview()).rejects.toThrow('status 503')
  })
})
