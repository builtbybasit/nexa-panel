import { afterEach, describe, expect, it, vi } from 'vitest'

import { applySystemUpdate, getSystemOverview, getSystemUpdates, type SystemOverview, type SystemUpdateAvailability } from './api'

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

// Spelled out rather than imported: this is the wire contract the server
// matches on, and the point of the assertion is that the call reaches it.
const backgroundHeaderName = 'X-Nexa-Background'

afterEach(() => {
  vi.useRealTimers()
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
      credentials: 'same-origin',
      headers: { Accept: 'application/json' },
    })
  })

  it('rejects unsuccessful responses', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(null, { status: 503 })))

    await expect(getSystemOverview()).rejects.toThrow('status 503')
  })

  // The top bar and the sidebar both poll this every 15s, so a raw fetch here
  // would renew the session's idle timeout on every authenticated page.
  it('goes through the shared helper, so the poll carries the background marker', async () => {
    vi.stubGlobal('document', { cookie: '' })
    const fetchMock = vi.fn().mockResolvedValue(Response.json(overview))
    vi.stubGlobal('fetch', fetchMock)
    vi.useFakeTimers()
    vi.setSystemTime(Date.now() + 10 * 60_000)

    await getSystemOverview()

    const headers = (fetchMock.mock.calls[0]?.[1] as RequestInit).headers as Record<string, string>
    expect(headers[backgroundHeaderName]).toBe('1')
  })
})

describe('getSystemUpdates', () => {
  it('returns installed and available versions', async () => {
    const availability: SystemUpdateAvailability = {
      installedVersion: '0.1.0',
      latest: { version: '0.2.0', tag: 'v0.2.0' },
      updateAvailable: true,
      checkedAt: '2026-07-21T00:00:00Z',
    }
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(new Response(JSON.stringify(availability), { status: 200, headers: { 'Content-Type': 'application/json' } })),
    )

    await expect(getSystemUpdates()).resolves.toEqual(availability)
  })
})

describe('applySystemUpdate', () => {
  it('posts the target version and returns the queued job', async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ job: { id: 7 } }), { status: 202, headers: { 'Content-Type': 'application/json' } }),
    )
    vi.stubGlobal('fetch', fetchMock)

    await expect(applySystemUpdate('0.2.0')).resolves.toEqual({ job: { id: 7 } })
    const call = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(call[0]).toBe('/api/v1/system/updates/apply')
    expect(call[1].method).toBe('POST')
    expect(JSON.parse(call[1].body as string)).toEqual({ version: '0.2.0' })
  })
})
