import { afterEach, describe, expect, it, vi } from 'vitest'

import { getJob, listJobs, submitDiagnostics, subscribeToJob, type Job } from './api'

class FakeEventSource {
  static readonly CONNECTING = 0
  static readonly OPEN = 1
  static readonly CLOSED = 2
  static instances: FakeEventSource[] = []

  readyState = FakeEventSource.CONNECTING
  onerror: (() => void) | null = null
  closed = false
  listeners = new Map<string, (event: MessageEvent<string>) => void>()

  constructor(readonly url: string) {
    FakeEventSource.instances.push(this)
  }

  addEventListener(name: string, listener: (event: MessageEvent<string>) => void) {
    this.listeners.set(name, listener)
  }

  close() {
    this.closed = true
    this.readyState = FakeEventSource.CLOSED
  }
}

const job: Job = {
  id: 12,
  kind: 'platform.diagnostics',
  state: 'queued',
  progress: 0,
  request: { delayMilliseconds: 150 },
  createdAt: '2026-07-16T00:00:00Z',
  updatedAt: '2026-07-16T00:00:00Z',
}

afterEach(() => {
  FakeEventSource.instances = []
  vi.unstubAllGlobals()
})

describe('jobs API', () => {
  it('lists durable jobs', async () => {
    const fetchMock = vi.fn().mockResolvedValue(Response.json({ items: [job] }))
    vi.stubGlobal('fetch', fetchMock)

    await expect(listJobs()).resolves.toEqual([job])
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/jobs?limit=50', {
      credentials: 'same-origin',
      headers: { Accept: 'application/json' },
    })
  })

  it('loads a single durable job with its result', async () => {
    const fetchMock = vi.fn().mockResolvedValue(Response.json({ ...job, state: 'succeeded', result: { bytes: 42 } }))
    vi.stubGlobal('fetch', fetchMock)

    await expect(getJob(12)).resolves.toEqual({ ...job, state: 'succeeded', result: { bytes: 42 } })
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/jobs/12', {
      credentials: 'same-origin',
      headers: { Accept: 'application/json' },
    })
  })

  it('submits a diagnostic job', async () => {
    const fetchMock = vi.fn().mockResolvedValue(Response.json(job, { status: 202 }))
    vi.stubGlobal('fetch', fetchMock)

    await expect(submitDiagnostics(150)).resolves.toEqual(job)
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/jobs/diagnostics', {
      method: 'POST',
      body: JSON.stringify({ delayMilliseconds: 150 }),
      credentials: 'same-origin',
      headers: { Accept: 'application/json', 'Content-Type': 'application/json' },
    })
  })

  it('lets EventSource reconnect after a transient job-stream interruption', () => {
    vi.stubGlobal('EventSource', FakeEventSource)
    const disconnected = vi.fn()
    const reconnecting = vi.fn()
    const stop = subscribeToJob(12, vi.fn(), disconnected, reconnecting)
    const source = FakeEventSource.instances[0]
    if (!source) throw new Error('EventSource was not constructed')

    source.readyState = FakeEventSource.CONNECTING
    source.onerror?.()

    expect(source.closed).toBe(false)
    expect(disconnected).not.toHaveBeenCalled()
    expect(reconnecting).toHaveBeenCalledOnce()

    source.readyState = FakeEventSource.CLOSED
    source.onerror?.()
    expect(disconnected).toHaveBeenCalledOnce()
    stop()
  })
})
