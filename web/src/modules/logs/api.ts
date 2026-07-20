export interface LogFile {
  /** Path relative to the site's logs/ directory, e.g. `access.log` or `tasks/abc.log`. */
  name: string
  size: number
  modifiedAt: string
}

export interface LogRead {
  lines: string[]
  /** Byte offset to resume incremental reads from. */
  nextOffset: number
  size: number
  /** True when the file shrank since the requested offset (rotation). */
  rotated: boolean
  /** True when the server hit its per-read byte budget. */
  truncated: boolean
}

export interface ReadLogParams {
  name: string
  /** Tail mode: number of trailing lines to return. Omit `offset` for tail mode. */
  tail?: number
  /** Case-insensitive substring filter applied server-side per line. */
  filter?: string
  /** Incremental mode: byte offset to read from (from a prior `nextOffset`). */
  offset?: number
  /** Byte budget for the read. */
  max?: number
}

class LogsRequestError extends Error {
  constructor(
    message: string,
    readonly status: number,
    readonly code?: string,
  ) {
    super(message)
  }
}

function base(siteId: string): string {
  return `/api/v1/sites/${encodeURIComponent(siteId)}/logs`
}

function request<T>(path: string): Promise<T> {
  return apiRequest<T>(path, undefined, {
    errorPrefix: 'Logs request',
    createError: (message, status, code) => new LogsRequestError(message, status, code),
  })
}

export async function listLogs(siteId: string): Promise<LogFile[]> {
  return (await request<{ items: LogFile[] }>(base(siteId))).items
}

export function readLog(siteId: string, params: ReadLogParams): Promise<LogRead> {
  const query = [`name=${encodeURIComponent(params.name)}`]
  if (params.tail !== undefined) query.push(`tail=${params.tail}`)
  if (params.filter) query.push(`filter=${encodeURIComponent(params.filter)}`)
  if (params.offset !== undefined) query.push(`offset=${params.offset}`)
  if (params.max !== undefined) query.push(`max=${params.max}`)
  return request(`${base(siteId)}/read?${query.join('&')}`)
}

/** Same-origin streaming download URL for a plain `<a href>` (server sets Content-Disposition). */
export function downloadUrl(siteId: string, name: string): string {
  return `${base(siteId)}/download?name=${encodeURIComponent(name)}`
}

/** One `lines` SSE event from the live stream. */
interface LogStreamEvent {
  lines: string[]
  /** True when the file was rotated; the stream restarts from the new file. */
  rotated: boolean
}

export interface LogStreamHandlers {
  onLines: (event: LogStreamEvent) => void
  /** Called once after the stream fails terminally; the source is closed first. */
  onError?: () => void
}

/**
 * Live-tail a log over SSE. Transient drops reconnect automatically and
 * resume from the last byte offset (the server frames each event with
 * `id: <offset>`, which the browser echoes back as Last-Event-ID); only a
 * terminally closed stream invokes `onError`. Returns a stop function.
 */
export function subscribeToLogStream(siteId: string, name: string, filter: string, handlers: LogStreamHandlers): () => void {
  const query = [`name=${encodeURIComponent(name)}`]
  if (filter) query.push(`filter=${encodeURIComponent(filter)}`)
  const source = new EventSource(`${base(siteId)}/stream?${query.join('&')}`)
  source.addEventListener('lines', (rawEvent) => {
    const event = rawEvent as MessageEvent<string>
    handlers.onLines(JSON.parse(event.data) as LogStreamEvent)
  })
  source.onerror = () => {
    if (source.readyState !== EventSource.CLOSED) return
    source.close()
    handlers.onError?.()
  }
  return () => source.close()
}
import { apiRequest } from '@/shared/api/request'
