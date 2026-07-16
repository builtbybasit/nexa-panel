import { afterEach, describe, expect, it, vi } from 'vitest'

import { downloadUrl, listLogs, LogsRequestError, readLog, subscribeToLogStream, type LogStreamEvent } from './api'

const getInit = { credentials: 'same-origin', headers: { Accept: 'application/json' } }

class FakeEventSource {
  static readonly CONNECTING = 0
  static readonly OPEN = 1
  static readonly CLOSED = 2
  static instances: FakeEventSource[] = []

  readyState = FakeEventSource.CONNECTING
  closed = false
  onerror: (() => void) | null = null
  private listeners = new Map<string, ((event: { data: string }) => void)[]>()

  constructor(readonly url: string) {
    FakeEventSource.instances.push(this)
  }

  addEventListener(type: string, listener: (event: { data: string }) => void) {
    this.listeners.set(type, [...(this.listeners.get(type) ?? []), listener])
  }

  close() {
    this.closed = true
    this.readyState = FakeEventSource.CLOSED
  }

  emit(type: string, data: string) {
    for (const listener of this.listeners.get(type) ?? []) listener({ data })
  }
}

afterEach(() => {
  vi.unstubAllGlobals()
  FakeEventSource.instances = []
})

describe('logs API', () => {
  it('lists discovered log files and unwraps the items envelope', async () => {
    const item = { name: 'tasks/abc.log', size: 2048, modifiedAt: '2026-07-16T00:00:00Z' }
    const fetchMock = vi.fn().mockResolvedValue(Response.json({ items: [item] }))
    vi.stubGlobal('fetch', fetchMock)

    await expect(listLogs('site_1')).resolves.toEqual([item])
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/sites/site_1/logs', getInit)
  })

  it('reads a tail with the name, tail count, and filter pinned in the query string', async () => {
    const read = { lines: ['GET /api ok'], nextOffset: 4096, size: 4096, rotated: false, truncated: false }
    const fetchMock = vi.fn().mockResolvedValue(Response.json(read))
    vi.stubGlobal('fetch', fetchMock)

    await expect(readLog('site_1', { name: 'tasks/abc.log', tail: 500, filter: 'GET /api' })).resolves.toEqual(read)
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/sites/site_1/logs/read?name=tasks%2Fabc.log&tail=500&filter=GET%20%2Fapi', getInit)
  })

  it('reads incrementally with offset and max, omitting the tail parameter', async () => {
    const read = { lines: [], nextOffset: 1024, size: 1024, rotated: true, truncated: false }
    const fetchMock = vi.fn().mockResolvedValue(Response.json(read))
    vi.stubGlobal('fetch', fetchMock)

    await expect(readLog('site_1', { name: 'access.log', offset: 1024, max: 65536 })).resolves.toEqual(read)
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/sites/site_1/logs/read?name=access.log&offset=1024&max=65536', getInit)
  })

  it('omits empty optional parameters from the read query', async () => {
    const fetchMock = vi.fn().mockResolvedValue(Response.json({ lines: [], nextOffset: 0, size: 0, rotated: false, truncated: false }))
    vi.stubGlobal('fetch', fetchMock)

    await readLog('site_1', { name: 'error.log', filter: '' })
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/sites/site_1/logs/read?name=error.log', getInit)
  })

  it('surfaces the server error envelope as a typed error', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(Response.json({ code: 'invalid_name', message: 'Log names cannot escape logs/.' }, { status: 422 })),
    )
    await expect(readLog('site_1', { name: '../secret', tail: 200 })).rejects.toEqual(
      new LogsRequestError('Log names cannot escape logs/.', 422, 'invalid_name'),
    )
  })

  it('builds a same-origin streaming download URL', () => {
    expect(downloadUrl('site_1', 'tasks/abc.log')).toBe('/api/v1/sites/site_1/logs/download?name=tasks%2Fabc.log')
  })

  it('opens the SSE stream with the name and filter pinned in the URL', () => {
    vi.stubGlobal('EventSource', FakeEventSource)

    const stop = subscribeToLogStream('site_1', 'tasks/abc.log', 'GET /api', { onLines: () => undefined })
    expect(FakeEventSource.instances[0]?.url).toBe('/api/v1/sites/site_1/logs/stream?name=tasks%2Fabc.log&filter=GET%20%2Fapi')
    stop()
    expect(FakeEventSource.instances[0]?.closed).toBe(true)

    subscribeToLogStream('site_1', 'access.log', '', { onLines: () => undefined })
    expect(FakeEventSource.instances[1]?.url).toBe('/api/v1/sites/site_1/logs/stream?name=access.log')
  })

  it('delivers parsed lines events and reports only terminal stream errors', () => {
    vi.stubGlobal('EventSource', FakeEventSource)

    const received: LogStreamEvent[] = []
    const onError = vi.fn()
    subscribeToLogStream('site_1', 'access.log', '', { onLines: (event) => received.push(event), onError })
    const source = FakeEventSource.instances[0]
    if (!source) throw new Error('EventSource was not constructed')

    source.emit('lines', JSON.stringify({ lines: ['first', 'second'], rotated: false }))
    source.emit('lines', JSON.stringify({ lines: [], rotated: true }))
    expect(received).toEqual([
      { lines: ['first', 'second'], rotated: false },
      { lines: [], rotated: true },
    ])

    // Transient drop: the browser reconnects (readyState CONNECTING) and resumes via Last-Event-ID.
    source.readyState = FakeEventSource.CONNECTING
    source.onerror?.()
    expect(onError).not.toHaveBeenCalled()
    expect(source.closed).toBe(false)

    // Terminal close: the wrapper closes the source and reports once.
    source.readyState = FakeEventSource.CLOSED
    source.onerror?.()
    expect(onError).toHaveBeenCalledTimes(1)
    expect(source.closed).toBe(true)
  })
})
