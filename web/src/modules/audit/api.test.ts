import { afterEach, describe, expect, it, vi } from 'vitest'

import { listAuditEvents } from './api'

// Spelled out rather than imported: this is the wire contract the server
// matches on, and the point of the assertion is that the call reaches it.
const backgroundHeaderName = 'X-Nexa-Background'

afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

describe('audit API', () => {
  it('loads append-only events', async () => {
    const events = [
      {
        id: 1,
        occurredAt: '2026-07-16T00:00:00Z',
        action: 'job.succeeded',
        subject: 'job:4',
        metadata: { kind: 'node.probe.apply' },
      },
    ]
    const fetchMock = vi.fn().mockResolvedValue(Response.json({ items: events }))
    vi.stubGlobal('fetch', fetchMock)

    await expect(listAuditEvents()).resolves.toEqual(events)
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/audit/events?limit=100', {
      credentials: 'same-origin',
      headers: { Accept: 'application/json' },
    })
  })

  // The view polls this every 10s, so a raw fetch here would renew the session's
  // idle timeout for as long as a tab stays open.
  it('goes through the shared helper, so the poll carries the background marker', async () => {
    vi.stubGlobal('document', { cookie: '' })
    const fetchMock = vi.fn().mockResolvedValue(Response.json({ items: [] }))
    vi.stubGlobal('fetch', fetchMock)
    vi.useFakeTimers()
    vi.setSystemTime(Date.now() + 10 * 60_000)

    await listAuditEvents()

    const headers = (fetchMock.mock.calls[0]?.[1] as RequestInit).headers as Record<string, string>
    expect(headers[backgroundHeaderName]).toBe('1')
  })
})
