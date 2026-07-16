import { afterEach, describe, expect, it, vi } from 'vitest'

import { listAuditEvents } from './api'

afterEach(() => vi.unstubAllGlobals())

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
})
